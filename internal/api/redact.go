// Package api 出站脱敏（M5）：密钥 / cookie / session 一律不回传前端，
// 换成 <field>_set 布尔标志（参照 livechat OAuth 的 Public() 视图做法）。
// 任一 XSS / 重绑定 / 会话泄露不再能直接读走全部凭证。
package api

import (
	"encoding/json"
	"strings"
)

// toRedactedMap 深拷贝为 map（经 JSON 往返，保持对外字段形状），再交给 fn 处理。
func toRedactedMap(v any, fn func(m map[string]any)) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]any{}
	}
	fn(m)
	return m
}

// redactKeys 把 m 里的密钥字段替换为 <k>_set 布尔：非空 → 删原值 + true；
// 空（或字段不存在，omitempty）→ false。嵌套对象用子 map 递归调用。
func redactKeys(m map[string]any, keys ...string) {
	if m == nil {
		return
	}
	for _, k := range keys {
		set := false
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			set = true
			delete(m, k)
		}
		m[k+"_set"] = set
	}
}
