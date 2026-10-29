package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"twitter_read_play/internal/source"
)

// ---- 请求 / 响应 ----

type inspectRequest struct {
	Domain           source.Domain `json:"domain"`
	Raw              string        `json:"raw"`
	Platform         string        `json:"platform"`
	ChannelID        string        `json:"channel_id"`
	ExpectedPlatform string        `json:"expected_platform"`
	Refresh          bool          `json:"refresh"`
}

type inspectAttempt struct {
	ChannelID    string `json:"channel_id"`
	AdapterID    string `json:"adapter_id"`
	OK           bool   `json:"ok"`
	ErrorKind    string `json:"error_kind,omitempty"`
	Error        string `json:"error,omitempty"`
	Milliseconds int64  `json:"ms,omitempty"`
	Cached       bool   `json:"cached,omitempty"`
}

type inspectStatus struct {
	Attempted bool             `json:"attempted"`
	OK        bool             `json:"ok"`
	Cached    bool             `json:"cached,omitempty"`
	Error     string           `json:"error,omitempty"`
	ErrorKind string           `json:"error_kind,omitempty"`
	Tried     []inspectAttempt `json:"tried,omitempty"`
}

type inspectResolved struct {
	ChannelID string `json:"channel_id"`
	AdapterID string `json:"adapter_id"`
	Score     int    `json:"score"`
}

type inspectResponse struct {
	Mode     source.Mode            `json:"mode"`
	Target   source.Target          `json:"target"`
	ViewURL  string                 `json:"view_url,omitempty"`
	Account  *source.AccountProfile `json:"account,omitempty"`
	Resolved *inspectResolved       `json:"resolved,omitempty"`
	Inspect  inspectStatus          `json:"inspect"`
	Warnings []string               `json:"warnings,omitempty"`
	Rejected []source.Rejection     `json:"rejected,omitempty"`
}

type inspectCacheEntry struct {
	Profile   source.AccountProfile
	ErrorKind source.InspectErrorKind
	Error     string
	Expires   time.Time
}

// ---- handler ----

// inspectSource POST /api/source/inspect
// 静态解析一定会返回；详情识别失败只影响 account 字段，不影响 target/view_url。
func (s *Server) inspectSource(w http.ResponseWriter, r *http.Request) {
	var in inspectRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	if in.Domain == "" {
		in.Domain = source.DomainFeed
	}
	t, err := source.Parse(in.Domain, in.Raw, source.Platform(in.Platform))
	if err != nil {
		if s.writeTargetError(w, err) {
			return
		}
		s.err(w, http.StatusBadRequest, err)
		return
	}

	resp := inspectResponse{Mode: s.serverMode(), Target: t}
	resp.ViewURL, _ = t.ViewURL()
	if in.ExpectedPlatform != "" {
		if perr := source.ValidatePlatform(in.ExpectedPlatform, t); perr != nil {
			resp.Warnings = append(resp.Warnings, perr.Error())
		}
	}
	if s.Channels == nil || s.Adapters == nil {
		resp.Inspect = inspectStatus{Attempted: true, ErrorKind: string(source.InspectUnsupported), Error: "渠道模型未初始化"}
		s.json(w, resp)
		return
	}

	channels := s.Channels.ForDomain(in.Domain, s.Adapters)
	cands, rejected, rerr := s.Adapters.Candidates(source.ResolveRequest{
		Domain: in.Domain, Target: t, ChannelID: in.ChannelID,
		Mode: s.serverMode(), Intent: source.IntentInspect, MaxCandidates: 3,
	}, channels)
	resp.Rejected = rejected
	if rerr != nil || len(cands) == 0 {
		resp.Inspect = inspectStatus{
			Attempted: true, ErrorKind: string(source.InspectUnsupported),
			Error: "当前没有能识别账号详情的渠道",
		}
		resp.Warnings = append(resp.Warnings, "目标已识别；当前没有能识别账号详情的渠道")
		s.json(w, resp)
		return
	}

	// 同一目标串行：重复点击时第一个请求填缓存，后续直接命中，避免打爆上游。
	lock := s.inspectLock(fmt.Sprintf("%s|%s|%s|%s|%s", t.Domain, t.Platform, t.Kind, t.ID, in.ChannelID))
	lock.Lock()
	defer lock.Unlock()
	if r.Context().Err() != nil {
		resp.Inspect = inspectStatus{Attempted: true, ErrorKind: string(source.InspectTimeout), Error: "请求已取消"}
		s.json(w, resp)
		return
	}

	// 全局并发上限：识别可能触发浏览器/计费接口，最多同时 2 个。
	sem := s.inspectSemaphore()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-r.Context().Done():
		resp.Inspect = inspectStatus{Attempted: true, ErrorKind: string(source.InspectTimeout), Error: "请求已取消"}
		s.json(w, resp)
		return
	}

	deadline := time.Now().Add(20 * time.Second)
	attempts := make([]inspectAttempt, 0, len(cands))
	var bestAttempt inspectAttempt
	bestRank := -1

	for _, cand := range cands {
		key := inspectCacheKey(t, cand.Channel.AdapterID)
		if !in.Refresh {
			if e, ok := s.inspectCacheGet(key); ok {
				if e.ErrorKind != "" {
					a := inspectAttempt{
						ChannelID: cand.Channel.ID, AdapterID: cand.Channel.AdapterID,
						ErrorKind: string(e.ErrorKind), Error: e.Error, Cached: true,
					}
					attempts = append(attempts, a)
					if rank := inspectRank(e.ErrorKind); rank > bestRank {
						bestRank, bestAttempt = rank, a
					}
					continue
				}
				p := e.Profile
				resp.Account = &p
				resp.Resolved = &inspectResolved{
					ChannelID: cand.Channel.ID, AdapterID: cand.Channel.AdapterID, Score: cand.Score,
				}
				resp.Inspect = inspectStatus{Attempted: true, OK: true, Cached: true, Tried: attempts}
				s.json(w, resp)
				return
			}
		}

		inspector, ok := source.AsInspector(cand.Adapter)
		if !ok {
			a := inspectAttempt{
				ChannelID: cand.Channel.ID, AdapterID: cand.Channel.AdapterID,
				ErrorKind: string(source.InspectUnsupported), Error: "适配器不支持账号详情识别",
			}
			attempts = append(attempts, a)
			if rank := inspectRank(source.InspectUnsupported); rank > bestRank {
				bestRank, bestAttempt = rank, a
			}
			continue
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		per := 8 * time.Second
		if remaining < per {
			per = remaining
		}
		ctx, cancel := context.WithTimeout(r.Context(), per)
		start := time.Now()
		p, err := inspector.InspectAccount(ctx, cand.Channel, t)
		cancel()
		ms := time.Since(start).Milliseconds()

		if err == nil {
			if p.Source == "" {
				p.Source = cand.Channel.AdapterID
			}
			if p.Platform == "" {
				p.Platform = t.Platform
			}
			if p.Kind == "" {
				p.Kind = t.Kind
			}
			if p.CanonicalURL == "" {
				p.CanonicalURL = resp.ViewURL
			}
			if p.FetchedAt.IsZero() {
				p.FetchedAt = time.Now()
			}
			s.inspectCachePut(key, inspectCacheEntry{Profile: p, Expires: time.Now().Add(10 * time.Minute)})
			resp.Account = &p
			resp.Resolved = &inspectResolved{
				ChannelID: cand.Channel.ID, AdapterID: cand.Channel.AdapterID, Score: cand.Score,
			}
			attempts = append(attempts, inspectAttempt{
				ChannelID: cand.Channel.ID, AdapterID: cand.Channel.AdapterID,
				OK: true, Milliseconds: ms,
			})
			resp.Inspect = inspectStatus{Attempted: true, OK: true, Tried: attempts}
			s.json(w, resp)
			return
		}

		kind := source.InspectKindOf(err)
		a := inspectAttempt{
			ChannelID: cand.Channel.ID, AdapterID: cand.Channel.AdapterID,
			ErrorKind: string(kind), Error: err.Error(), Milliseconds: ms,
		}
		attempts = append(attempts, a)
		s.inspectCachePut(key, inspectCacheEntry{
			ErrorKind: kind, Error: err.Error(), Expires: time.Now().Add(time.Minute),
		})
		if rank := inspectRank(kind); rank > bestRank {
			bestRank, bestAttempt = rank, a
		}
	}

	if bestRank >= 0 {
		resp.Inspect = inspectStatus{
			Attempted: true, Error: bestAttempt.Error,
			ErrorKind: bestAttempt.ErrorKind, Tried: attempts,
		}
		resp.Warnings = append(resp.Warnings, "目标已识别；详情识别失败："+bestAttempt.Error)
	} else {
		resp.Inspect = inspectStatus{
			Attempted: true, ErrorKind: string(source.InspectUpstream),
			Error: "识别未完成", Tried: attempts,
		}
	}
	s.json(w, resp)
}

// writeTargetError 格式不支持时返回结构化 400；返回 false 表示不是 UnsupportedTargetError。
func (s *Server) writeTargetError(w http.ResponseWriter, err error) bool {
	var ute *source.UnsupportedTargetError
	if !errors.As(err, &ute) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":     err.Error(),
		"supported": ute.Supported,
		"examples":  ute.Examples,
	})
	return true
}

// ---- 缓存 / 锁 ----

func inspectCacheKey(t source.Target, adapterID string) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s", t.Domain, t.Platform, t.Kind, t.ID, adapterID)
}

func inspectRank(k source.InspectErrorKind) int {
	switch k {
	case source.InspectNeedLogin:
		return 5
	case source.InspectRateLimited:
		return 4
	case source.InspectNotFound:
		return 3
	case source.InspectTimeout:
		return 2
	case source.InspectUpstream:
		return 1
	default:
		return 0
	}
}

func (s *Server) inspectLock(key string) *sync.Mutex {
	s.inspectMu.Lock()
	defer s.inspectMu.Unlock()
	if s.inspectLocks == nil {
		s.inspectLocks = map[string]*sync.Mutex{}
	}
	if s.inspectLocks[key] == nil {
		s.inspectLocks[key] = &sync.Mutex{}
	}
	return s.inspectLocks[key]
}

func (s *Server) inspectSemaphore() chan struct{} {
	s.inspectMu.Lock()
	defer s.inspectMu.Unlock()
	if s.inspectSem == nil {
		s.inspectSem = make(chan struct{}, 2)
	}
	return s.inspectSem
}

func (s *Server) inspectCacheGet(key string) (inspectCacheEntry, bool) {
	s.inspectMu.Lock()
	defer s.inspectMu.Unlock()
	e, ok := s.inspectCache[key]
	if !ok {
		return inspectCacheEntry{}, false
	}
	if time.Now().After(e.Expires) {
		delete(s.inspectCache, key)
		return inspectCacheEntry{}, false
	}
	return e, true
}

func (s *Server) inspectCachePut(key string, e inspectCacheEntry) {
	s.inspectMu.Lock()
	defer s.inspectMu.Unlock()
	if s.inspectCache == nil {
		s.inspectCache = map[string]inspectCacheEntry{}
	}
	if len(s.inspectCache) >= 256 {
		now := time.Now()
		for k, v := range s.inspectCache {
			if now.After(v.Expires) {
				delete(s.inspectCache, k)
			}
		}
		if len(s.inspectCache) >= 256 {
			for k := range s.inspectCache {
				delete(s.inspectCache, k)
				break
			}
		}
	}
	s.inspectCache[key] = e
}
