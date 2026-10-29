// Package i18n 多语言错误与提示（en / ja / zh，兜底 en）。
package i18n

import (
	"strings"
	"sync"
)

// Lang 规范化语言：en / ja / zh。
func Lang(lang string) string {
	l := strings.ToLower(lang)
	switch {
	case strings.HasPrefix(l, "zh"):
		return "zh"
	case strings.HasPrefix(l, "ja"):
		return "ja"
	default:
		return "en"
	}
}

// FromAcceptLanguage 从 Accept-Language 头解析。
func FromAcceptLanguage(h string) string {
	if h == "" {
		return "en"
	}
	// 简单取权重最高项
	best, bestQ := "en", -1.0
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag := part
		q := 1.0
		if i := strings.Index(part, ";q="); i >= 0 {
			tag = part[:i]
			fmt := strings.TrimSpace(part[i+3:])
			var f float64
			for _, c := range fmt {
				if c >= '0' && c <= '9' {
					f = f*10 + float64(c-'0')
				}
			}
			q = f / 10
		}
		if q > bestQ {
			normalized := Lang(tag)
			if normalized != "" {
				best, bestQ = normalized, q
			}
		}
	}
	return best
}

type catalog map[string]map[string]string

var (
	mu       sync.RWMutex
	msgs     = catalog{}
	fallback = "en"
)

// Add 注册词条（任一语言缺失时回落 en）。
func Add(key string, translations map[string]string) {
	mu.Lock()
	defer mu.Unlock()
	msgs[key] = translations
}

// T 翻译。
func T(lang, key string) string {
	lang = Lang(lang)
	mu.RLock()
	defer mu.RUnlock()
	m, ok := msgs[key]
	if !ok {
		return key
	}
	if v, ok := m[lang]; ok && v != "" {
		return v
	}
	return m[fallback]
}

func init() {
	// API 错误词条
	Add("err.unauthorized", map[string]string{
		"en": "Not logged in", "ja": "ログインしていません", "zh": "未登录",
	})
	Add("err.bad_credentials", map[string]string{
		"en": "Invalid username or password", "ja": "ユーザー名またはパスワードが正しくありません", "zh": "用户名或密码错误",
	})
	Add("err.addr_required", map[string]string{
		"en": "addr is required", "ja": "addr は必須です", "zh": "addr 必填",
	})
	Add("err.username_required", map[string]string{
		"en": "username is not configured on this role", "ja": "このロールには username が未設定です", "zh": "角色未配置 username",
	})
	Add("err.no_upstream", map[string]string{
		"en": "No enabled upstream for this platform. Add one in Settings", "ja": "このプラットフォームに有効なアップストリームがありません。設定で追加してください", "zh": "该平台没有启用的上游数据源，请在设置中添加",
	})
	Add("err.no_tweets", map[string]string{
		"en": "No tweets available", "ja": "利用可能なツイートがありません", "zh": "没有可用推文",
	})
	Add("err.tweet_not_found", map[string]string{
		"en": "Tweet not in latest list", "ja": "ツイートが最新リストにありません", "zh": "推文不在最新列表中",
	})
	Add("err.machine_not_found", map[string]string{
		"en": "Machine not found", "ja": "マシンが見つかりません", "zh": "机器不存在",
	})
	Add("err.machine_not_paired", map[string]string{
		"en": "Machine is not paired", "ja": "マシンが未ペアリングです", "zh": "机器未配对",
	})
	Add("err.role_not_found", map[string]string{
		"en": "Role not found", "ja": "ロールが見つかりません", "zh": "角色不存在",
	})
	Add("err.upstream_not_found", map[string]string{
		"en": "Upstream not found", "ja": "アップストリームが見つかりません", "zh": "上游不存在",
	})
	Add("err.upstream_unknown_api", map[string]string{
		"en": "Unknown API type", "ja": "不明な API タイプです", "zh": "未知 API 类型",
	})
	Add("err.unknown_adapter", map[string]string{
		"en": "Unknown adapter %q", "ja": "不明なアダプター %q", "zh": "未知适配器 %q",
	})
	Add("err.adapter_mode_unsupported", map[string]string{
		"en": "Adapter %s is not supported in the current mode (%s)", "ja": "現在のモード（%s）ではアダプター %s を使用できません", "zh": "当前模式（%s）不支持适配器 %s",
	})
	Add("err.adapter_domain_unsupported", map[string]string{
		"en": "Adapter %s does not support the %s domain", "ja": "アダプター %s は %s ドメインに対応していません", "zh": "适配器 %s 不支持 %s 域",
	})
	Add("err.unknown_domain", map[string]string{
		"en": "Unknown domain %q", "ja": "不明なドメイン %q", "zh": "未知域 %q",
	})
	Add("err.adapter_no_domain", map[string]string{
		"en": "Adapter %s declares no usable feed/live domain", "ja": "アダプター %s に利用可能な feed/live ドメインがありません", "zh": "适配器 %s 没有可用的 feed/live 域",
	})
}
