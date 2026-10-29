package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/source"
)

type channelUsage struct {
	RoleID   string `json:"role_id"`
	RoleName string `json:"role_name"`
	Domain   string `json:"domain"`
	Target   string `json:"target,omitempty"`
}

type channelView struct {
	source.Channel
	Adapter source.Info            `json:"adapter"`
	Ready   bool                   `json:"ready"`
	Inspect bool                   `json:"inspect"`
	Missing []source.CredentialKey `json:"missing,omitempty"`
	Usage   []channelUsage         `json:"usage,omitempty"`
}

// listChannels GET /api/channels?domain=feed|live
func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	if s.Channels == nil || s.Adapters == nil {
		s.json(w, map[string]any{"channels": []any{}, "adapters": []any{}, "mode": s.serverMode()})
		return
	}
	domain := source.Domain(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain"))))
	var list []source.Channel
	if domain == source.DomainFeed || domain == source.DomainLive {
		list = s.Channels.ForDomain(domain, s.Adapters)
	} else {
		list = s.Channels.List()
	}
	views := make([]channelView, 0, len(list))
	for _, ch := range list {
		v := channelView{Channel: ch}
		if a, ok := s.Adapters.Get(ch.AdapterID); ok {
			v.Adapter = a.Info()
			v.Missing = source.CheckCredentials(ch, a)
			v.Ready = len(v.Missing) == 0
			_, v.Inspect = source.AsInspector(a)
		}
		v.Usage = s.channelUsage(ch.ID)
		views = append(views, v)
	}
	s.json(w, map[string]any{
		"channels": views,
		"adapters": s.adapterInfos(domain),
		"mode":     s.serverMode(),
	})
}

// listAdapters GET /api/source/adapters?domain=feed|live
func (s *Server) listAdapters(w http.ResponseWriter, r *http.Request) {
	domain := source.Domain(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain"))))
	s.json(w, s.adapterInfos(domain))
}

func (s *Server) adapterInfos(domain source.Domain) []source.Info {
	if s.Adapters == nil {
		return []source.Info{}
	}
	out := make([]source.Info, 0)
	for _, info := range s.Adapters.Infos() {
		if !info.SupportsMode(s.serverMode()) {
			continue
		}
		if domain != "" && !info.SupportsDomain(domain) {
			continue
		}
		out = append(out, info)
	}
	return out
}

func (s *Server) serverMode() source.Mode {
	if s.Mode == "" {
		return source.ModeWeb
	}
	return s.Mode
}

// upsertChannel POST /api/channels | PUT /api/channels/{id}
func (s *Server) upsertChannel(w http.ResponseWriter, r *http.Request) {
	if s.Channels == nil || s.Adapters == nil {
		s.err(w, http.StatusInternalServerError, errStr("渠道模型未初始化"))
		return
	}
	var ch source.Channel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	if id := r.PathValue("id"); id != "" {
		ch.ID = id
	}
	a, ok := s.Adapters.Get(ch.AdapterID)
	if !ok {
		s.err(w, http.StatusBadRequest, fmt.Errorf(s.tr(r, "err.unknown_adapter"), ch.AdapterID))
		return
	}
	if !a.Info().SupportsMode(s.serverMode()) {
		s.err(w, http.StatusBadRequest, fmt.Errorf(s.tr(r, "err.adapter_mode_unsupported"), s.serverMode(), ch.AdapterID))
		return
	}
	// domain 打底：显式值必须在适配器能力内；留空时优先沿用已有渠道的域，
	// 其次落到适配器声明的第一个可用域，避免写出「就绪但永远解析不到」的渠道。
	switch ch.Domain {
	case "":
		if cur, err := s.Channels.Get(ch.ID); err == nil && a.Info().SupportsDomain(cur.Domain) {
			ch.Domain = cur.Domain
		}
	case source.DomainFeed, source.DomainLive:
		if !a.Info().SupportsDomain(ch.Domain) {
			s.err(w, http.StatusBadRequest, fmt.Errorf(s.tr(r, "err.adapter_domain_unsupported"), ch.AdapterID, ch.Domain))
			return
		}
	default:
		s.err(w, http.StatusBadRequest, fmt.Errorf(s.tr(r, "err.unknown_domain"), ch.Domain))
		return
	}
	if ch.Domain == "" {
		domain, ok := source.PickDomain(a.Info(), "")
		if !ok {
			s.err(w, http.StatusBadRequest, fmt.Errorf(s.tr(r, "err.adapter_no_domain"), ch.AdapterID))
			return
		}
		ch.Domain = domain
	}
	saved, err := s.Channels.Upsert(ch)
	if err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.json(w, saved)
}

// disableChannel POST /api/channels/{id}/disable  body: {enabled:bool}
func (s *Server) disableChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	enabled := false
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if s.Channels == nil {
		s.err(w, http.StatusInternalServerError, errStr("渠道模型未初始化"))
		return
	}
	if err := s.Channels.SetEnabled(r.PathValue("id"), enabled); err != nil {
		s.err(w, http.StatusNotFound, err)
		return
	}
	s.json(w, map[string]bool{"ok": true, "enabled": enabled})
}

// deleteChannel DELETE /api/channels/{id}：被角色引用时 409。
func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if usage := s.channelUsage(id); len(usage) > 0 {
		w.WriteHeader(http.StatusConflict)
		s.json(w, map[string]any{"error": "渠道正被角色使用，先改掉角色绑定再删除", "usage": usage})
		return
	}
	if s.Channels == nil {
		s.err(w, http.StatusInternalServerError, errStr("渠道模型未初始化"))
		return
	}
	if err := s.Channels.Delete(id); err != nil {
		s.err(w, http.StatusNotFound, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// testChannel POST /api/channels/{id}/test  body: {domain?, target?, platform?}
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	if s.Channels == nil || s.Adapters == nil {
		s.err(w, http.StatusInternalServerError, errStr("渠道模型未初始化"))
		return
	}
	var in struct {
		Domain   source.Domain `json:"domain"`
		Target   string        `json:"target"`
		Platform string        `json:"platform"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ch, err := s.Channels.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, http.StatusNotFound, err)
		return
	}
	a, ok := s.Adapters.Get(ch.AdapterID)
	if !ok {
		s.err(w, http.StatusBadRequest, fmt.Errorf("未知适配器 %q", ch.AdapterID))
		return
	}
	domain := in.Domain
	if domain == "" {
		domain = ch.Domain
	}
	info := a.Info()
	if in.Target == "" && domain == source.DomainLive {
		s.err(w, http.StatusBadRequest, errStr("直播渠道测试需要填写一个直播 URL"))
		return
	}
	target, err := s.testTarget(domain, in.Target, in.Platform, info)
	if err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	switch domain {
	case source.DomainFeed:
		fa, ok := source.AsFeed(a)
		if !ok {
			s.err(w, http.StatusBadRequest, fmt.Errorf("适配器 %s 不支持 Feed", ch.AdapterID))
			return
		}
		probe, perr := fa.TestFeed(ctx, ch, target)
		if perr != nil {
			s.json(w, map[string]any{"ok": false, "error": perr.Error(), "target": target})
			return
		}
		s.json(w, map[string]any{"ok": probe.OK, "probe": probe, "target": target})
	case source.DomainLive:
		la, ok := source.AsLive(a)
		if !ok {
			s.err(w, http.StatusBadRequest, fmt.Errorf("适配器 %s 不支持 Live", ch.AdapterID))
			return
		}
		probe, perr := la.TestLive(ctx, ch, target)
		if perr != nil {
			s.json(w, map[string]any{"ok": false, "error": perr.Error(), "target": target})
			return
		}
		s.json(w, map[string]any{"ok": probe.OK, "probe": probe, "target": target})
	default:
		s.err(w, http.StatusBadRequest, errStr("domain 必须是 feed 或 live"))
	}
}

func (s *Server) testTarget(domain source.Domain, raw, platform string, info source.Info) (source.Target, error) {
	if strings.TrimSpace(raw) != "" {
		return source.Parse(domain, raw, source.Platform(platform))
	}
	p := source.NormalizePlatform(platform)
	if p == "" && len(info.Platforms) > 0 {
		p = info.Platforms[0]
	}
	sample := sampleTargetRaw(domain, p)
	if sample == "" {
		return source.Target{}, errStr("请填写测试目标")
	}
	return source.Parse(domain, sample, p)
}

func sampleTargetRaw(domain source.Domain, p source.Platform) string {
	if domain == source.DomainLive {
		return ""
	}
	switch p {
	case source.PlatformWeibo:
		return "人民日报"
	case source.PlatformXHS:
		return "butterzhu"
	case source.PlatformInstagram:
		return "instagram"
	case source.PlatformTruth:
		return "realDonaldTrump"
	case source.PlatformBluesky:
		return "bsky.app"
	case source.PlatformBilibili:
		return "2"
	case source.PlatformRSS:
		return "https://hnrss.org/frontpage"
	case source.PlatformYouTube:
		return "https://www.youtube.com/@YouTube"
	}
	return "@X"
}

// parseSource POST /api/source/parse  body: {domain, raw, platform?, channel_id?}
func (s *Server) parseSource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domain    source.Domain `json:"domain"`
		Raw       string        `json:"raw"`
		Platform  string        `json:"platform"`
		ChannelID string        `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	t, err := source.Parse(in.Domain, in.Raw, source.Platform(in.Platform))
	if err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.resolveAndRespond(w, in.Domain, t, in.ChannelID)
}

// resolveSource POST /api/source/resolve  body: {domain, target, channel_id?}
func (s *Server) resolveSource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domain    source.Domain `json:"domain"`
		Target    source.Target `json:"target"`
		ChannelID string        `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.resolveAndRespond(w, in.Domain, in.Target, in.ChannelID)
}

func (s *Server) resolveAndRespond(w http.ResponseWriter, domain source.Domain, t source.Target, channelID string) {
	resp := map[string]any{"target": t}
	if u, ok := t.ViewURL(); ok {
		resp["view_url"] = u
	}
	if s.Channels == nil || s.Adapters == nil {
		s.json(w, resp)
		return
	}
	channels := s.Channels.ForDomain(domain, s.Adapters)
	res, err := s.Adapters.Resolve(source.ResolveRequest{
		Domain: domain, Target: t, ChannelID: channelID, Mode: s.serverMode(),
	}, channels)
	resp["rejected"] = res.Rejected
	if err != nil {
		resp["warnings"] = []string{err.Error()}
	} else {
		resp["resolved"] = map[string]any{
			"channel_id": res.Channel.ID,
			"adapter_id": res.Channel.AdapterID,
			"score":      res.Score,
		}
	}
	s.json(w, resp)
}

type bindingView struct {
	RoleID    string   `json:"role_id"`
	RoleName  string   `json:"role_name"`
	Domain    string   `json:"domain"`
	Platform  string   `json:"platform,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Target    string   `json:"target,omitempty"`
	ChannelID string   `json:"channel_id,omitempty"`
	AdapterID string   `json:"adapter_id,omitempty"`
	Ready     bool     `json:"ready"`
	Warnings  []string `json:"warnings,omitempty"`
	Legacy    bool     `json:"legacy,omitempty"`
}

// listBindings GET /api/source/bindings：Upstreams 页的只读视图（Role × Target × Channel）。
func (s *Server) listBindings(w http.ResponseWriter, r *http.Request) {
	roles := s.Roles.List()
	out := make([]bindingView, 0, len(roles)*2)
	for _, role := range roles {
		if t, err := feedTargetForRole(role); err == nil {
			out = append(out, s.bindingFor(role, source.DomainFeed, t, role.FeedChannelID, role.UpstreamID, false))
		}
		if raw := strings.TrimSpace(role.LiveURL); raw != "" {
			if t, err := source.ParseLive(raw, ""); err == nil {
				out = append(out, s.bindingFor(role, source.DomainLive, t, role.LiveChannelID, "", false))
			}
		} else if id := strings.TrimSpace(role.LiveSourceID); id != "" {
			out = append(out, bindingView{
				RoleID: role.ID, RoleName: role.Name, Domain: string(source.DomainLive),
				Target: id, ChannelID: id, Legacy: true,
			})
		}
	}
	s.json(w, out)
}

func (s *Server) bindingFor(role *model.Role, domain source.Domain, t source.Target, channelID, legacyChannelID string, legacy bool) bindingView {
	b := bindingView{
		RoleID: role.ID, RoleName: role.Name, Domain: string(domain),
		Platform: string(t.Platform), Kind: string(t.Kind), Target: t.Display,
		Legacy: legacy,
	}
	if b.Target == "" {
		b.Target = t.Raw
	}
	if s.Channels == nil || s.Adapters == nil {
		return b
	}
	channels := s.Channels.ForDomain(domain, s.Adapters)
	res, err := s.Adapters.Resolve(source.ResolveRequest{
		Domain: domain, Target: t, ChannelID: firstNonEmptyStr(channelID, legacyChannelID), Mode: s.serverMode(),
	}, channels)
	if err != nil {
		b.Warnings = append(b.Warnings, err.Error())
		return b
	}
	b.ChannelID = res.Channel.ID
	b.AdapterID = res.Channel.AdapterID
	b.Ready = true
	return b
}

func (s *Server) channelUsage(channelID string) []channelUsage {
	if s.Roles == nil {
		return nil
	}
	var out []channelUsage
	for _, role := range s.Roles.List() {
		if role.FeedChannelID == channelID || role.UpstreamID == channelID {
			out = append(out, channelUsage{RoleID: role.ID, RoleName: role.Name, Domain: "feed", Target: role.FeedTarget})
			continue
		}
		if role.LiveChannelID == channelID {
			out = append(out, channelUsage{RoleID: role.ID, RoleName: role.Name, Domain: "live", Target: role.LiveURL})
		}
	}
	return out
}

func feedTargetForRole(role *model.Role) (source.Target, error) {
	raw := strings.TrimSpace(role.FeedTarget)
	if raw == "" {
		raw = strings.TrimSpace(role.Username)
	}
	if raw == "" {
		return source.Target{}, errStr("未配置 Feed 目标")
	}
	hint := source.Platform(role.Platform)
	if hint == "" && !source.LooksLikeURL(raw) {
		hint = source.PlatformX
	}
	return source.ParseFeed(raw, hint)
}
