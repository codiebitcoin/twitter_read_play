package clt01

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// 配对范围
const (
	AuthScopeSession   = "session"   // 短期凭据（12 小时，设备主密钥轮换即失效）
	AuthScopePermanent = "permanent" // 长期凭据（直到密钥轮换）
)

// Credentials 是配对成功后下发的凭据。
type Credentials struct {
	AuthScope        string `json:"auth_scope"`
	LocalSecret      string `json:"local_secret"`
	LocalSessionID   string `json:"local_session_id"`
	SessionExpiresMs int64  `json:"local_session_expires_at_ms"`
	ChallengeID      string `json:"challenge_id"`
	BridgeID         string `json:"bridge_id"`
	ServiceSecret    string `json:"service_secret"`
	HasService       bool   `json:"has_service"`
	DeviceID         string `json:"device_id"`
	Name             string `json:"name"`
	Version          string `json:"version"`

	DeviceAddr string `json:"device_addr"` // SDK 补充：设备地址
	UserID     string `json:"user_id"`     // SDK 补充：设备登录用户
	PairedAt   int64  `json:"paired_at"`   // SDK 补充：配对时间
}

// SessionExpired 报告 session 凭据是否已过期。
func (cr *Credentials) SessionExpired() bool {
	return cr.AuthScope == AuthScopeSession && cr.SessionExpiresMs > 0 && time.Now().UnixMilli() >= cr.SessionExpiresMs
}

// CodeProvider 由宿主实现：设备弹出配对窗口后，向用户收集 6 位配对码。
// 典型实现：终端 stdin、GUI 弹窗、轮询输入框等。
type CodeProvider interface {
	// WaitForCode 返回用户输入的配对码；ctx 取消时返回错误以中止配对。
	WaitForCode(ctx context.Context, challengeID string) (string, error)
}

// CodeFunc 是 CodeProvider 的函数适配器。
type CodeFunc func(ctx context.Context, challengeID string) (string, error)

func (f CodeFunc) WaitForCode(ctx context.Context, challengeID string) (string, error) {
	return f(ctx, challengeID)
}

// pairRequestResponse / pairConfirmResponse 对应设备端 JSON。
type pairRequestResponse struct {
	ChallengeID string `json:"challenge_id"`
	ExpiresAtMs int64  `json:"expires_at_ms"`
}

// RequestPairing 第一步：请求设备弹出配对码窗口。
// 返回 challenge_id（5 分钟内有效）。免签名，但要求设备已登录。
func (c *Client) RequestPairing() (challengeID string, expiresAtMs int64, err error) {
	c2 := *c
	c2.Secret = ""
	data, err := (&c2).doUnsigned(context.Background(), "POST", "/v1/api/auth/pair/request", nil, PayloadModeEmpty)
	if err != nil {
		return "", 0, err
	}
	var r pairRequestResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return "", 0, err
	}
	return r.ChallengeID, r.ExpiresAtMs, nil
}

// CancelPairing 幂等取消配对挑战（关闭设备上的配对窗口）。
func (c *Client) CancelPairing(challengeID string) error {
	c2 := *c
	c2.Secret = ""
	body, _ := json.Marshal(map[string]string{"challenge_id": challengeID})
	data, err := (&c2).doUnsigned(context.Background(), "POST", "/v1/api/auth/pair/cancel", body, PayloadModeJSON)
	if err != nil {
		return err
	}
	var r struct {
		Transitioned bool `json:"transitioned"`
	}
	_ = json.Unmarshal(data, &r)
	return nil
}

// ConfirmPairing 第二步：用配对码换取凭据。
// scope 传 "" 时默认 permanent。
func (c *Client) ConfirmPairing(challengeID, pairingCode, scope string) (*Credentials, error) {
	if scope == "" {
		scope = AuthScopePermanent
	}
	c2 := *c
	c2.Secret = ""
	body, _ := json.Marshal(map[string]string{
		"pairing_code": pairingCode,
		"challenge_id": challengeID,
		"auth_scope":   scope,
	})
	data, err := (&c2).doUnsigned(context.Background(), "POST", "/v1/api/auth/pair/confirm", body, PayloadModeJSON)
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	creds.AuthScope = scope
	creds.DeviceAddr = c.BaseURL
	if creds.LocalSecret == "" {
		return nil, fmt.Errorf("clt01: 配对响应缺少 local_secret")
	}
	return &creds, nil
}

// Pair 执行完整配对流程（后台自动串联）：
//
//  1. ping / bootstrap 校验设备可达且已登录
//  2. 请求配对（设备弹出 6 位配对码窗口）
//  3. 通过 codeProvider 向用户收集配对码
//  4. confirm 换取凭据
//
// scope 为 "" 时默认 permanent。成功返回凭据（含 device_id / user_id），
// 宿主可用 store.Save 持久化，或直接 Apply 到 Client。
func (c *Client) Pair(ctx context.Context, codeProvider CodeProvider, scope string) (*Credentials, error) {
	if codeProvider == nil {
		return nil, fmt.Errorf("clt01: codeProvider 不能为空")
	}

	// 1. 可达性 + 登录校验
	if err := c.Ping(); err != nil {
		return nil, fmt.Errorf("clt01: 设备不可达: %w", err)
	}
	boot, err := c.Bootstrap()
	if err != nil {
		return nil, fmt.Errorf("clt01: 获取引导信息失败: %w", err)
	}
	if !boot.DeviceLoggedIn || boot.UserID == "" {
		return nil, fmt.Errorf("clt01: 设备未登录，请先在设备上完成账号登录")
	}

	// 2. 请求配对窗口
	challengeID, _, err := c.RequestPairing()
	if err != nil {
		return nil, fmt.Errorf("clt01: 请求配对失败: %w", err)
	}

	// 用户取消/超时时，顺手关闭设备配对窗口
	defer func() {
		if ctx.Err() != nil {
			_ = c.CancelPairing(challengeID)
		}
	}()

	// 3. 收集配对码
	code, err := codeProvider.WaitForCode(ctx, challengeID)
	if err != nil {
		return nil, fmt.Errorf("clt01: 获取配对码失败: %w", err)
	}

	// 4. confirm
	creds, err := c.ConfirmPairing(challengeID, code, scope)
	if err != nil {
		return nil, err
	}
	creds.UserID = boot.UserID
	creds.PairedAt = time.Now().Unix()
	c.Apply(creds)
	return creds, nil
}

// Apply 把凭据应用到客户端，之后即可调用签名接口。
func (c *Client) Apply(creds *Credentials) {
	c.Secret = creds.LocalSecret
	c.SessionID = creds.LocalSessionID
	c.UserID = creds.UserID
}
