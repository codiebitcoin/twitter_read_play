package source

import (
	"errors"
	"fmt"
	"strings"
)

// UnsupportedTargetError 目标格式不支持（Role 保存时应返回 400）。
type UnsupportedTargetError struct {
	Domain    Domain
	Raw       string
	Supported []string
	Examples  []string
}

func (e *UnsupportedTargetError) Error() string {
	msg := fmt.Sprintf("不支持的%s地址 %q", domainLabel(e.Domain), strings.TrimSpace(e.Raw))
	if len(e.Supported) > 0 {
		msg += "（支持：" + strings.Join(e.Supported, "；") + "）"
	}
	return msg
}

// MissingCredentialError 渠道缺少凭据。
type MissingCredentialError struct {
	ChannelID string
	AdapterID string
	Target    Target
	Missing   []CredentialKey
}

func (e *MissingCredentialError) Error() string {
	return fmt.Sprintf("渠道 %s（%s）缺少凭据：%s", e.ChannelID, e.AdapterID, joinCreds(e.Missing))
}

// NoMatchError 没有任何渠道能服务该目标。
type NoMatchError struct {
	Target   Target
	Mode     Mode
	Rejected []Rejection
}

func (e *NoMatchError) Error() string {
	msg := fmt.Sprintf("没有可用的上游渠道能处理 %s", targetLabel(e.Target))
	if len(e.Rejected) > 0 {
		parts := make([]string, 0, len(e.Rejected))
		for _, r := range e.Rejected {
			p := r.ChannelID
			if r.AdapterID != "" {
				p += "(" + r.AdapterID + ")"
			}
			parts = append(parts, p+": "+r.Reason)
		}
		msg += "；候选：" + strings.Join(parts, "；")
	}
	return msg
}

// PlatformConflictError Role 平台与目标平台冲突。
type PlatformConflictError struct {
	Role   Platform
	Target Platform
}

func (e *PlatformConflictError) Error() string {
	return fmt.Sprintf("角色平台 %q 与目标平台 %q 不一致", e.Role, e.Target)
}

// ModeMismatchError 适配器不支持当前运行模式。
type ModeMismatchError struct {
	AdapterID string
	Mode      Mode
}

func (e *ModeMismatchError) Error() string {
	return fmt.Sprintf("适配器 %s 不支持当前模式 %s", e.AdapterID, e.Mode)
}

// ChannelNotFoundError 显式指定的渠道不存在。
type ChannelNotFoundError struct{ ID string }

func (e *ChannelNotFoundError) Error() string { return "渠道不存在: " + e.ID }

// DuplicateAdapterError 重复注册适配器。
type DuplicateAdapterError struct{ ID string }

func (e *DuplicateAdapterError) Error() string { return "适配器重复注册: " + e.ID }

// InspectErrorKind 详情识别失败类型（前端据此分流文案/动作）。
type InspectErrorKind string

const (
	InspectUnsupported InspectErrorKind = "inspect_unsupported"
	InspectNeedLogin   InspectErrorKind = "need_login"
	InspectNotFound    InspectErrorKind = "not_found"
	InspectRateLimited InspectErrorKind = "rate_limited"
	InspectTimeout     InspectErrorKind = "timeout"
	InspectUpstream    InspectErrorKind = "upstream"
)

// InspectError 详情识别失败。
type InspectError struct {
	Kind      InspectErrorKind
	ChannelID string
	AdapterID string
	Message   string
	Err       error
}

func (e *InspectError) Error() string {
	msg := e.Message
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if msg == "" {
		msg = "账号详情识别失败"
	}
	return msg
}

func (e *InspectError) Unwrap() error { return e.Err }

// InspectKindOf 取错误的识别类型；非 InspectError 返回 InspectUpstream。
func InspectKindOf(err error) InspectErrorKind {
	var ie *InspectError
	if errors.As(err, &ie) && ie.Kind != "" {
		return ie.Kind
	}
	return InspectUpstream
}

func joinCreds(keys []CredentialKey) string {
	if len(keys) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, ", ")
}

func domainLabel(d Domain) string {
	if d == DomainLive {
		return "直播"
	}
	return "Feed"
}

func targetLabel(t Target) string {
	if t.Display != "" {
		return t.Display
	}
	if t.Raw != "" {
		return string(t.Platform) + "/" + string(t.Kind) + " " + t.Raw
	}
	return string(t.Platform) + "/" + string(t.Kind)
}
