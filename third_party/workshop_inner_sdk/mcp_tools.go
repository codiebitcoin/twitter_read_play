package clt01

// mcp_tools.go — MCP 内置工具集：把 SDK 的配对 / 角色配置 / 播报 / 通用请求
// 封装成 MCP tools，供模型（Cursor / Claude 等）直接调用。
//
// 设备地址解析规则：工具参数里的 addr > MCPServer.DefaultAddr（启动参数或
// CLT01_ADDR 环境变量）。签名调用需要已配对凭据（ClientFromStore，
// 默认 ~/.clt01sdk/credentials.json）。

import (
	"context"
	"fmt"
	"time"
)

// registerTools 注册全部内置 MCP 工具。
func (s *MCPServer) registerTools() {
	// ---- 连接 / 配对 ----
	s.add(MCPTool{
		Name:        "clt01_ping",
		Description: "检查 CLT01 设备是否可达（免鉴权 ping）。返回设备可达性与基本引导信息。",
		InputSchema: mcpSchema("检查设备连通性", strProp("addr", "设备地址 host:port（缺省用启动时配置的默认地址）")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := New(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			if err := c.Ping(); err != nil {
				return nil, fmt.Errorf("设备不可达: %w", err)
			}
			boot, err := c.Bootstrap()
			if err != nil {
				return map[string]any{"ok": true, "addr": c.BaseURL}, nil
			}
			return map[string]any{"ok": true, "addr": c.BaseURL, "bootstrap": boot}, nil
		},
	})

	s.add(MCPTool{
		Name:        "clt01_pair_start",
		Description: "请求 CLT01 设备弹出 6 位配对码窗口（5 分钟内有效）。用户在设备屏幕上读出配对码后，用 clt01_pair_confirm 提交。",
		InputSchema: mcpSchema("发起配对", strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := New(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			challengeID, expiresAtMs, err := c.RequestPairing()
			if err != nil {
				return nil, fmt.Errorf("请求配对失败（设备需已登录）: %w", err)
			}
			return map[string]any{
				"challenge_id":  challengeID,
				"expires_at_ms": expiresAtMs,
				"hint":          "请让用户读出设备屏幕上的 6 位配对码，然后调用 clt01_pair_confirm",
			}, nil
		},
	})

	s.add(MCPTool{
		Name:        "clt01_pair_confirm",
		Description: "用设备屏幕上的 6 位配对码完成配对并保存凭据到本地（~/.clt01sdk/credentials.json）。scope=session 为 12 小时短期凭据，permanent 为长期凭据（默认）。",
		InputSchema: mcpSchema("确认配对",
			strProp("code", "设备屏幕上的 6 位配对码（必填）"),
			strProp("challenge_id", "clt01_pair_start 返回的 challenge_id（必填）"),
			strProp("scope", "session | permanent（默认 permanent）"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			code := argStr(args, "code")
			challenge := argStr(args, "challenge_id")
			if code == "" || challenge == "" {
				return nil, fmt.Errorf("需要 code（6 位配对码）与 challenge_id 两个参数")
			}
			addr := s.addrOf(args)
			c, err := New(addr)
			if err != nil {
				return nil, err
			}
			scope := argStr(args, "scope")
			creds, err := c.ConfirmPairing(challenge, code, scope)
			if err != nil {
				return nil, fmt.Errorf("配对确认失败（配对码错误或已过期）: %w", err)
			}
			store, err := NewStore()
			if err != nil {
				return nil, err
			}
			if err := store.Save(creds); err != nil {
				return nil, err
			}
			return map[string]any{
				"ok":          true,
				"device_id":   creds.DeviceID,
				"user_id":     creds.UserID,
				"auth_scope":  creds.AuthScope,
				"device_addr": creds.DeviceAddr,
				"hint":        "凭据已保存，后续签名接口（system_info / character_get 等）可直接使用",
			}, nil
		},
	})

	s.add(MCPTool{
		Name:        "clt01_pair_cancel",
		Description: "取消进行中的配对挑战（关闭设备上的配对码窗口）。",
		InputSchema: mcpSchema("取消配对",
			strProp("challenge_id", "clt01_pair_start 返回的 challenge_id"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := New(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			if err := c.CancelPairing(argStr(args, "challenge_id")); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(MCPTool{
		Name:        "clt01_pair_status",
		Description: "查看本地凭据库中该设备的配对状态（密钥脱敏，不回传 secret）。",
		InputSchema: mcpSchema("查询配对状态", strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			addr := s.addrOf(args)
			c, err := New(addr)
			if err != nil {
				return nil, err
			}
			store, err := NewStore()
			if err != nil {
				return nil, err
			}
			creds, err := store.Load(c.BaseURL)
			if err != nil {
				return nil, err
			}
			if creds == nil {
				return map[string]any{"paired": false, "device_addr": c.BaseURL,
					"hint": "未配对：先调用 clt01_pair_start → 用户读码 → clt01_pair_confirm"}, nil
			}
			return map[string]any{
				"paired":      true,
				"device_id":   creds.DeviceID,
				"user_id":     creds.UserID,
				"auth_scope":  creds.AuthScope,
				"device_addr": creds.DeviceAddr,
				"expired":     creds.SessionExpired(),
			}, nil
		},
	})

	// ---- 设备 / 角色 ----
	s.add(MCPTool{
		Name:        "clt01_system_info",
		Description: "读取设备系统信息（登录用户、时区、能力列表等，需已配对）。",
		InputSchema: mcpSchema("读取系统信息", strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			info, err := c.SystemInfo()
			if err != nil {
				return nil, err
			}
			return info, nil
		},
	})

	s.add(MCPTool{
		Name:        "clt01_character_get",
		Description: "读取指定角色槽（1-5）的完整配置：名字、人设 prompt、音色、音量、当前模型等。",
		InputSchema: mcpSchema("读取角色配置",
			intProp("slot", "角色槽 1-5（默认 1）"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			return c.GetCharacterCtx(ctx, argIntOr(args, "slot", 1))
		},
	})

	s.add(MCPTool{
		Name:        "clt01_character_set",
		Description: "设置角色槽配置（原子：与设备现值一致则跳过）。非零字段合并下发：name / description（人设）/ prompt（system prompt）/ voice_id / group_key / volume / sex / animation_intensity。",
		InputSchema: mcpSchema("设置角色配置",
			intProp("slot", "角色槽 1-5（默认 1）"),
			strProp("name", "角色名"),
			strProp("description", "角色描述"),
			strProp("prompt", "System prompt"),
			strProp("voice_id", "音色 ID（用 clt01_voice_groups 查询可用值）"),
			strProp("group_key", "音色分组 key"),
			intProp("volume", "TTS 音量（>0 才生效）"),
			boolProp("force", "与现值一致时也强制下发（默认 false）"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			cc := &CallContext{Slot: argIntOr(args, "slot", 1)}
			want := &CharacterSettings{
				Name:            argStr(args, "name"),
				Description:     argStr(args, "description"),
				CharacterPrompt: argStr(args, "prompt"),
				VoiceID:         argStr(args, "voice_id"),
				GroupKey:        argStr(args, "group_key"),
				TTSVolume:       argInt(args, "volume"),
			}
			changed, err := c.SetInfo(ctx, cc, want, argBool(args, "force"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"changed": changed,
				"hint": changedText(changed, "角色配置未变化，设备未被扰动")}, nil
		},
	})

	s.add(MCPTool{
		Name:        "clt01_character_list",
		Description: "列出设备全部角色槽（原始 JSON）。",
		InputSchema: mcpSchema("列出角色", strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			return c.CharacterList(ctx)
		},
	})

	// ---- 音色 / 播报 ----
	s.add(MCPTool{
		Name:        "clt01_voice_groups",
		Description: "查询设备全部音色分组及各组 voice_id 列表（clt01_character_set / clt01_play_chat 的 voice_id 取值来源）。",
		InputSchema: mcpSchema("查询音色分组", strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			return c.VoiceGroup(ctx)
		},
	})

	s.add(MCPTool{
		Name:        "clt01_play_chat",
		Description: "让设备播报一段文本（≥1.5.3 走 chat/turns 管线返回 chat_id；<1.5.3 走 tts 通道需 voice_id）。返回 channel 供 clt01_play_state 查询播放状态。",
		InputSchema: mcpSchema("播报文本",
			strProp("text", "要播报的文本（必填）"),
			intProp("slot", "角色槽 1-5（默认 1）"),
			strProp("voice_id", "tts 通道（旧固件）必需的音色 ID"),
			strProp("group_key", "tts 通道音色分组"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			text := argStr(args, "text")
			if text == "" {
				return nil, fmt.Errorf("需要 text 参数")
			}
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			cc := &CallContext{Slot: argIntOr(args, "slot", 1)}
			res, err := c.PlayChat(ctx, cc, text, &PlayChatOptions{
				VoiceID:  argStr(args, "voice_id"),
				GroupKey: argStr(args, "group_key"),
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"channel": res.Channel, "chat_id": res.ChatID,
				"hint": "可用 clt01_play_state（channel=" + res.Channel + "）查询是否播完"}, nil
		},
	})

	s.add(MCPTool{
		Name:        "clt01_play_state",
		Description: "查询设备播放状态：channel=chat-turns（≥1.5.3 会话管线）或 tts（旧固件）。不传 channel 自动按设备固件层预判。",
		InputSchema: mcpSchema("查询播放状态",
			strProp("channel", "chat-turns | tts（可空=自动预判）"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			channel := argStr(args, "channel")
			if channel == "" {
				channel = c.PredictPlayChatChannel()
			}
			return c.StateForChannel(ctx, channel)
		},
	})

	s.add(MCPTool{
		Name:        "clt01_play_cancel",
		Description: "取消设备正在播报的会话（≥1.5.3 chat/turns 通道，用 clt01_play_chat 返回的 chat_id）。",
		InputSchema: mcpSchema("取消播报",
			strProp("chat_id", "clt01_play_chat 返回的 chat_id"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			return c.CancelChatTurn(ctx, argStr(args, "chat_id"))
		},
	})

	s.add(MCPTool{
		Name:        "clt01_tts_say",
		Description: "直接 TTS 合成播报（不经角色 LLM 管线，按槽位音色朗读，返回 utterance_id 可查状态）。",
		InputSchema: mcpSchema("TTS 直读",
			strProp("text", "要朗读的文本（必填）"),
			intProp("slot", "角色槽 1-5（默认 1，决定使用哪个角色的音色）"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			text := argStr(args, "text")
			if text == "" {
				return nil, fmt.Errorf("需要 text 参数")
			}
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			return c.TTSUtteranceCtx(ctx, argIntOr(args, "slot", 1), text, "")
		},
	})

	// ---- 通用逃生口 ----
	s.add(MCPTool{
		Name:        "clt01_raw_request",
		Description: "通用签名请求（逃生口）：对设备任意 LOCAL API 端点发起已签名请求并返回原始 JSON。用于工具集未覆盖的接口；接口清单见设备端 LOCAL_API_REFERENCE.md。",
		InputSchema: mcpSchema("原始请求",
			strProp("method", "HTTP 方法 GET/POST/PUT/DELETE（默认 GET）"),
			strProp("path", "API 路径，如 /v1/api/data/user/ability/group-keys（必填）"),
			strProp("body", "请求体 JSON 字符串（可空）"),
			strProp("addr", "设备地址 host:port")),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			path := argStr(args, "path")
			if path == "" {
				return nil, fmt.Errorf("需要 path 参数")
			}
			c, err := ClientFromStore(s.addrOf(args))
			if err != nil {
				return nil, err
			}
			method := argStr(args, "method")
			if method == "" {
				method = "GET"
			}
			body := []byte(argStr(args, "body"))
			mode := PayloadModeEmpty
			if len(body) > 0 {
				mode = PayloadModeJSON
			}
			ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			return c.DoCtx(ctx2, method, path, body, mode)
		},
	})
}

// ---- 参数与 schema 辅助 ----

// addrOf 工具参数里的 addr 优先，否则用服务端默认地址。
func (s *MCPServer) addrOf(args map[string]any) string {
	if a := argStr(args, "addr"); a != "" {
		return a
	}
	return s.DefaultAddr
}

func changedText(changed bool, unchangedMsg string) string {
	if changed {
		return "已下发设备"
	}
	return unchangedMsg
}

func mcpSchema(desc string, props ...map[string]any) map[string]any {
	merged := map[string]any{}
	for _, m := range props {
		for k, v := range m {
			merged[k] = v
		}
	}
	return map[string]any{
		"type":        "object",
		"description": desc,
		"properties":  merged,
	}
}

// strProp/intProp/boolProp 生成单个属性定义（返回 键名→定义 的小 map 供 mcpSchema 合并）。
func strProp(name, desc string) map[string]any {
	return map[string]any{name: map[string]any{"type": "string", "description": desc}}
}

func intProp(name, desc string) map[string]any {
	return map[string]any{name: map[string]any{"type": "integer", "description": desc}}
}

func boolProp(name, desc string) map[string]any {
	return map[string]any{name: map[string]any{"type": "boolean", "description": desc}}
}

func argStr(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return trimSpace(v)
}

func argInt(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case string:
		n := 0
		_, _ = fmt.Sscanf(trimSpace(v), "%d", &n)
		return n
	}
	return 0
}

func argIntOr(args map[string]any, key string, def int) int {
	if n := argInt(args, key); n != 0 {
		return n
	}
	return def
}

func argBool(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}