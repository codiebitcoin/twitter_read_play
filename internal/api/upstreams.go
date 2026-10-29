package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"twitter_read_play/internal/model"

	"twitter_read_play/internal/twitter"
	"twitter_read_play/internal/upstream"
)

func (s *Server) listUpstreams(w http.ResponseWriter, r *http.Request) {
	s.json(w, s.Upstreams.List())
}

func (s *Server) listUpstreamAPIs(w http.ResponseWriter, r *http.Request) {
	s.json(w, upstream.APIs)
}

func (s *Server) upsertUpstream(w http.ResponseWriter, r *http.Request) {
	var u model.Upstream
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		s.err(w, 400, err)
		return
	}
	if id := r.PathValue("id"); id != "" {
		u.ID = id
	}
	if err := s.Upstreams.Upsert(&u); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, u)
}

func (s *Server) deleteUpstream(w http.ResponseWriter, r *http.Request) {
	if err := s.Upstreams.Delete(r.PathValue("id")); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// sampleHandle 各平台的测试用示例账号（可用性检查要拉真实数据，不能拿 "X" 硬套微博）。
func sampleHandle(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "weibo":
		return "人民日报"
	case "xiaohongshu":
		return "butterzhu"
	case "bilibili":
		return "2" // 站长 mid，公开空间动态
	case "rss":
		return "https://hnrss.org/frontpage"
	}
	return "X"
}

// testErrorMessage 把测试失败的原始错误包成一句人话（含平台/账号与处理建议）。
func testErrorMessage(u *model.Upstream, handle string, err error) string {
	who := fmt.Sprintf("%s/%s", u.API, handle)
	if u.Platform != "" {
		who = fmt.Sprintf("%s(%s)/%s", u.API, u.Platform, handle)
	}
	if twitter.IsQuotaError(err) {
		return fmt.Sprintf("测试 %s 失败：%v（Key 无效或额度/授权问题，需先修好再重试）", who, err)
	}
	return fmt.Sprintf("测试 %s 失败：%v（检查 Base URL / Key / 网络，微博与小红书较慢，可稍后重试）", who, err)
}

// testUpstream 拉 1 条帖子验证上游可用性（key/额度/网络）。
func (s *Server) testUpstream(w http.ResponseWriter, r *http.Request) {
	u, err := s.Upstreams.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, err)
		return
	}
	fallbackKey, _ := s.Store.GetSetting("socialdata_api_key")
	p, err := upstream.ProviderFor(u, fallbackKey)
	if err != nil {
		s.json(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// 按平台取一个真实示例账号（social_read 的微博/小红书路径拿 "X" 只会 404）
	handle := sampleHandle(u.Platform)
	timeout := 15 * time.Second
	if u.API == "socialread" {
		timeout = 60 * time.Second // 小红书要现起浏览器、微博要走反爬，15s 不够
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	start := time.Now()
	tweets, err := p.FetchLatest(ctx, handle, 1)
	if err != nil {
		s.json(w, map[string]any{"ok": false, "error": testErrorMessage(u, handle, err), "handle": handle, "ms": time.Since(start).Milliseconds()})
		return
	}
	if len(tweets) == 0 {
		s.json(w, map[string]any{"ok": false, "handle": handle, "tweets": 0, "ms": time.Since(start).Milliseconds(),
			"error": fmt.Sprintf("测试 %s/%s 成功连通但返回 0 条：确认账号名是否存在（微博用昵称，小红书用小红书号）", u.API, handle)})
		return
	}
	s.json(w, map[string]any{"ok": true, "tweets": len(tweets), "handle": handle, "ms": time.Since(start).Milliseconds()})
}
