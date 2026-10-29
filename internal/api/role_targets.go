package api

import (
	"strings"

	"twitter_read_play/internal/livechat"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/source"
)

// parseRoleTargets 解析 Role 的 feed_target / live_url，校验平台一致性并做选源预检。
// 返回的 validation 只用于本次响应，不落库。
//
// 规则：
//   - Feed 目标格式不支持 → 硬错误（400）；
//   - Live 目标格式不支持 → 硬错误（400）；
//   - 格式支持但没有可用渠道 → 保存成功 + warning（用户可能先去配渠道）。
func (s *Server) parseRoleTargets(role *model.Role) (*model.RoleValidation, error) {
	role.Validation = nil

	// 旧前端只传 platform + username：合成 feed_target，让后续统一走新解析。
	if strings.TrimSpace(role.FeedTarget) == "" && strings.TrimSpace(role.Username) != "" {
		role.FeedTarget = strings.TrimSpace(role.Username)
	}

	validation := &model.RoleValidation{}

	if raw := strings.TrimSpace(role.FeedTarget); raw != "" {
		hint := source.Platform(role.Platform)
		if hint == "" && !source.LooksLikeURL(raw) {
			hint = source.PlatformX
		}
		t, err := source.ParseFeed(raw, hint)
		if err != nil {
			return validation, err
		}
		if err := source.ValidatePlatform(role.Platform, t); err != nil {
			return validation, err
		}
		role.Platform = string(t.Platform)
		role.FeedKind = string(t.Kind)
		role.FeedTargetID = t.ID
		role.Username = t.ID

		rv := &model.RoleTargetValidation{
			Platform: string(t.Platform), Kind: string(t.Kind), TargetID: t.ID,
		}
		if s.Channels != nil && s.Adapters != nil {
			channels := s.Channels.ForDomain(source.DomainFeed, s.Adapters)
			res, rerr := s.Adapters.Resolve(source.ResolveRequest{
				Domain:    source.DomainFeed,
				Target:    t,
				ChannelID: firstNonEmptyStr(role.FeedChannelID, role.UpstreamID),
				Mode:      s.serverMode(),
			}, channels)
			if rerr != nil {
				rv.Warnings = append(rv.Warnings, rerr.Error())
			} else {
				rv.ChannelID = res.Channel.ID
				rv.AdapterID = res.Channel.AdapterID
			}
		}
		validation.Feed = rv
	}

	if raw := strings.TrimSpace(role.LiveURL); raw != "" {
		t, err := source.ParseLive(raw, "")
		if err != nil {
			return validation, err
		}
		role.LivePlatform = string(t.Platform)
		role.LiveKind = string(t.Kind)
		role.LiveTargetID = t.ID

		rv := &model.RoleTargetValidation{
			Platform: string(t.Platform), Kind: string(t.Kind), TargetID: t.ID,
		}
		if s.Channels != nil && s.Adapters != nil {
			channels := s.Channels.ForDomain(source.DomainLive, s.Adapters)
			res, rerr := s.Adapters.Resolve(source.ResolveRequest{
				Domain:    source.DomainLive,
				Target:    t,
				ChannelID: role.LiveChannelID,
				Mode:      s.serverMode(),
			}, channels)
			if rerr != nil {
				rv.Warnings = append(rv.Warnings, rerr.Error())
			} else {
				rv.ChannelID = res.Channel.ID
				rv.AdapterID = res.Channel.AdapterID
			}
		}
		validation.Live = rv
	}

	if validation.Feed == nil && validation.Live == nil {
		return nil, nil
	}
	return validation, nil
}

// syncRoleLiveSource 把 Role 的 live_url 桥接成 livechat KindSource 配置，
// 让现有 scanLive 运行时无需改动即可消费弹幕。
func (s *Server) syncRoleLiveSource(role *model.Role, v *model.RoleTargetValidation) {
	if s.Live == nil {
		return
	}
	if strings.TrimSpace(role.LiveURL) == "" {
		if strings.HasPrefix(role.LiveSourceID, "role-live-") {
			s.Live.DeleteConfig(role.LiveSourceID)
			role.LiveSourceID = ""
			_ = s.Roles.Upsert(role)
		}
		return
	}
	if v == nil || v.ChannelID == "" {
		return
	}
	cfgID := "role-live-" + role.ID
	_, err := s.Live.SaveConfig(livechat.SourceConfig{
		ID: cfgID, Title: role.Name + " · live", Kind: livechat.KindSource,
		Channel: strings.TrimSpace(role.LiveURL), Enabled: role.LiveEnabled,
		RoleID: role.ID, ChannelID: v.ChannelID,
		TargetPlatform: v.Platform, TargetKind: v.Kind, TargetID: v.TargetID,
	})
	if err != nil {
		return
	}
	if role.LiveSourceID != cfgID {
		role.LiveSourceID = cfgID
		_ = s.Roles.Upsert(role)
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
