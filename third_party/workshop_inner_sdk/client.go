// Package clt01 提供 CLT01 嵌入式设备（codie-embed-backend）局域网访问 SDK。
//
// 功能：
//   - 局域网 API 签名调用（HMAC-SHA256，算法与设备端 LocalAuthGuard 一致）
//   - 完整配对流程封装（请求配对码 → 用户输入 6 位码 → 换取凭据）
//   - 凭据本地持久化与复用
//   - Web 控制台（/config/pair 等）反向代理，供宿主程序内嵌授权页面
package clt01

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client 是单台设备的局域网客户端。
type Client struct {
	BaseURL string // 如 http://192.168.1.10:8081（不带尾斜杠）
	HTTP    *http.Client

	// 凭据（配对后填入；permanent 模式只用 Secret，session 模式另带 SessionID）
	Secret    string
	SessionID string
	UserID    string

	httpTimeout time.Duration // 0 = 默认

	// 固件版本档案缓存（指针：Client 值拷贝共享同一份，且避免 mutex 进入拷贝）
	fw *firmwareState
}

// SetHTTPTimeout 调整该客户端的请求超时（音色克隆等长耗时操作用）。
func (c *Client) SetHTTPTimeout(d time.Duration) {
	c.httpTimeout = d
	if c.HTTP != nil {
		c.HTTP.Timeout = d
	}
}

// New 创建客户端。addr 可以是 "192.168.1.10:8081" 或 "http://192.168.1.10:8081"。
func New(addr string) (*Client, error) {
	u := addr
	if u == "" {
		return nil, fmt.Errorf("clt01: 地址为空")
	}
	if u[0] != 'h' { // 简单判断：无 scheme 则补 http
		u = "http://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("clt01: 地址无效 %q: %w", addr, err)
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	return &Client{
		BaseURL: parsed.String(),
		fw:      &firmwareState{},
		// 默认 2 分钟：cloth/VRM 上传约 15MB，局域网 WiFi 下 15s 会超时；
		// 短请求不受影响（超时只是上限）。特殊场景仍可用 SetHTTPTimeout 覆盖。
		//
		// Transport 必须直连（Proxy: nil）：设备都在局域网，而宿主进程常配 HTTP_PROXY
		// 用于外网抓取（socialdata 等），默认 Transport 会把内网设备请求也送进代理，
		// 代理返回 502 → 配对/状态/上传全部失败（实测：设置页配了 http_proxy 后
		// pair/start 报 "clt01:  (HTTP 502)"）。
		HTTP: &http.Client{Timeout: 2 * time.Minute, Transport: directTransport()},
	}, nil
}

// directTransport 返回一份绕过代理的 Transport（保留默认 TLS/连接池参数）。
func directTransport() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{Proxy: nil}
	}
	clone := t.Clone()
	clone.Proxy = nil
	return clone
}

// ---- 签名 ----

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func randomNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// canonical 组规范化字符串：METHOD|path|timestamp|nonce|payloadMode|bodyHash|userID
func canonical(method, path, ts, nonce, payloadMode, bodyHash, userID string) string {
	if method == "" {
		method = http.MethodGet
	}
	return strings.ToUpper(method) + "|" + path + "|" + ts + "|" + nonce + "|" + payloadMode + "|" + bodyHash + "|" + userID
}

// Sign 按设备端算法计算 X-Local-Sig。
func Sign(secret, method, path, ts, nonce, payloadMode, bodyHash, userID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical(method, path, ts, nonce, payloadMode, bodyHash, userID)))
	return hex.EncodeToString(mac.Sum(nil))
}

// ---- 请求 ----

// APIError 是设备返回的非 2xx 响应。
type APIError struct {
	StatusCode int
	Code       string // 设备错误码，如 LOCAL_SESSION_EXPIRED
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("clt01: %s: %s (HTTP %d)", e.Code, e.Message, e.StatusCode)
	}
	return fmt.Sprintf("clt01: %s (HTTP %d)", e.Message, e.StatusCode)
}

// PayloadMode 覆盖默认的 payloadMode 推断（empty/json/multipart/zstd-stream/gzip-stream）。
type PayloadMode string

const (
	PayloadModeEmpty     PayloadMode = "empty"
	PayloadModeJSON      PayloadMode = "json"
	PayloadModeMultipart PayloadMode = "multipart"
)

// Do 发送一次签名请求并解析统一 JSON 信封（{"success":...,"data":...}）。
// path 必须以 / 开头；body 为 nil 或空表示无请求体。
// 返回 data 字段的原始 JSON；信封 success=false 或 HTTP 非 2xx 时返回 *APIError。
func (c *Client) Do(method, path string, body []byte, mode PayloadMode) (json.RawMessage, error) {
	return c.DoCtx(context.Background(), method, path, body, mode)
}

// DoCtx 同 Do，带 context（取消/超时传播到 HTTP 层）。
func (c *Client) DoCtx(ctx context.Context, method, path string, body []byte, mode PayloadMode) (json.RawMessage, error) {
	if c.Secret == "" {
		return nil, fmt.Errorf("clt01: 尚未配对，无签名密钥（请先调用 Pair 或 LoadCredentials）")
	}
	return c.doUnsigned(ctx, method, path, body, mode)
}

// doUnsigned 是核心请求通道：不检查凭据（免鉴权接口与配对流程使用）。
func (c *Client) doUnsigned(ctx context.Context, method, path string, body []byte, mode PayloadMode) (json.RawMessage, error) {
	return c.doUnsignedCT(ctx, method, path, body, mode, "")
}

// DoRawBytes 同 Do 的签名请求，但不做 JSON 信封解析，返回原始响应体（二进制下载用）。
func (c *Client) DoRawBytes(method, path string, body []byte, mode PayloadMode) ([]byte, error) {
	return c.DoRawBytesCtx(context.Background(), method, path, body, mode)
}

// DoRawBytesCtx 同 DoRawBytes，带 context。
func (c *Client) DoRawBytesCtx(ctx context.Context, method, path string, body []byte, mode PayloadMode) ([]byte, error) {
	if c.Secret == "" {
		return nil, fmt.Errorf("clt01: 尚未配对，无签名密钥（请先调用 Pair 或 LoadCredentials）")
	}
	return c.DoRawBytesCT(ctx, method, path, body, mode, "")
}

// DoRawBytesCT 同 DoRawBytesCtx，但允许覆盖 Content-Type（multipart 上传用；
// 传 "" 时按 body 非空回落 application/json）。
func (c *Client) DoRawBytesCT(ctx context.Context, method, path string, body []byte, mode PayloadMode, contentType string) ([]byte, error) {
	if path == "" || path[0] != '/' {
		return nil, fmt.Errorf("clt01: path 必须以 / 开头: %q", path)
	}

	payloadMode := string(mode)
	if payloadMode == "" {
		if len(body) == 0 {
			payloadMode = string(PayloadModeEmpty)
		} else {
			payloadMode = string(PayloadModeJSON)
		}
	}
	bodyHash := sha256Hex(body)
	if mode == PayloadModeMultipart {
		bodyHash = "UNSIGNED-PAYLOAD"
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	signPath := path
	if i := strings.IndexByte(signPath, '?'); i >= 0 {
		signPath = signPath[:i]
	}
	if c.UserID == "" && c.Secret != "" {
		if info, err := c.Bootstrap(); err == nil && info.UserID != "" {
			c.UserID = info.UserID
		}
	}
	sig := Sign(c.Secret, method, signPath, ts, nonce, payloadMode, bodyHash, c.UserID)

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Local-Sig", sig)
	req.Header.Set("X-Local-Nonce", nonce)
	req.Header.Set("X-Local-Timestamp", ts)
	req.Header.Set("X-Local-Payload-Mode", payloadMode)
	req.Header.Set("X-Local-Body-Hash", bodyHash)
	if c.UserID != "" {
		req.Header.Set("X-Local-User-Id", c.UserID)
	}
	if c.SessionID != "" {
		req.Header.Set("X-Local-Session-Id", c.SessionID)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clt01: 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return nil, &APIError{StatusCode: resp.StatusCode, Code: envelope.Code, Message: envelope.Message}
	}
	return raw, nil
}

func (c *Client) doUnsignedCT(ctx context.Context, method, path string, body []byte, mode PayloadMode, contentType string) (json.RawMessage, error) {
	if path == "" || path[0] != '/' {
		return nil, fmt.Errorf("clt01: path 必须以 / 开头: %q", path)
	}

	payloadMode := string(mode)
	if payloadMode == "" {
		if len(body) == 0 {
			payloadMode = string(PayloadModeEmpty)
		} else {
			payloadMode = string(PayloadModeJSON)
		}
	}
	bodyHash := sha256Hex(body)
	if mode == PayloadModeMultipart {
		bodyHash = "UNSIGNED-PAYLOAD"
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	// 设备端签名只覆盖 URL.Path（不含 query），query 仅参与实际请求。
	signPath := path
	if i := strings.IndexByte(signPath, '?'); i >= 0 {
		signPath = signPath[:i]
	}
	// user_id 缺失（旧版本导入的凭据等）时自动从设备 bootstrap 补齐并缓存，
	// 否则设备端 verifyLocalUserID 会报 LOCAL_USER_MISMATCH。
	if c.UserID == "" && c.Secret != "" {
		if info, err := c.Bootstrap(); err == nil && info.UserID != "" {
			c.UserID = info.UserID
		}
	}
	sig := Sign(c.Secret, method, signPath, ts, nonce, payloadMode, bodyHash, c.UserID)

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Local-Sig", sig)
	req.Header.Set("X-Local-Nonce", nonce)
	req.Header.Set("X-Local-Timestamp", ts)
	req.Header.Set("X-Local-Payload-Mode", payloadMode)
	req.Header.Set("X-Local-Body-Hash", bodyHash)
	if c.UserID != "" {
		req.Header.Set("X-Local-User-Id", c.UserID)
	}
	if c.SessionID != "" {
		req.Header.Set("X-Local-Session-Id", c.SessionID)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clt01: 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Success bool            `json:"success"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (raw != nil && len(raw) > 0 && envelope.Message != "" && !envelope.Success && envelope.Data == nil) {
		return nil, &APIError{StatusCode: resp.StatusCode, Code: envelope.Code, Message: envelope.Message}
	}
	if !envelope.Success && envelope.Data == nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Code: envelope.Code, Message: envelope.Message}
	}
	return envelope.Data, nil
}

// GetJSON / PostJSON 是 Do 的便捷封装。
func (c *Client) GetJSON(path string, out any) error {
	data, err := c.Do(http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) PostJSON(path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	data, err := c.Do(http.MethodPost, path, body, PayloadModeJSON)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---- 便捷业务方法 ----

// Ping 免鉴权存活探测。
func (c *Client) Ping() error {
	c2 := *c
	c2.Secret = "" // ping 免鉴权
	data, err := (&c2).doUnsigned(context.Background(), http.MethodGet, "/v1/api/auth/ping", nil, "")
	if err != nil {
		return err
	}
	var s string
	_ = json.Unmarshal(data, &s)
	return nil
}

// BootstrapInfo 免鉴权引导信息。
type BootstrapInfo struct {
	Language       string `json:"language"`
	AILanguage     string `json:"ai_language"`
	DeviceLoggedIn bool   `json:"device_logged_in"`
	UserID         string `json:"user_id"`
}

// Bootstrap 获取设备引导信息（免鉴权）。
func (c *Client) Bootstrap() (*BootstrapInfo, error) {
	c2 := *c
	c2.Secret = ""
	data, err := (&c2).doUnsigned(context.Background(), http.MethodGet, "/v1/api/auth/bootstrap", nil, "")
	if err != nil {
		return nil, err
	}
	var info BootstrapInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// SystemInfo 需要签名。
type SystemInfo struct {
	LocalIP      string          `json:"local_ip"`
	Language     string          `json:"language"`
	Username     string          `json:"username"`
	UserID       string          `json:"user_id"`
	Timezone     string          `json:"timezone"`
	Capabilities map[string]bool `json:"capabilities"`
	Extra        map[string]any  `json:"-"`
}

// SystemInfo 获取系统信息（已配对）。
func (c *Client) SystemInfo() (*SystemInfo, error) {
	data, err := c.GetRaw("/v1/api/data/user/system")
	if err != nil {
		return nil, err
	}
	info := &SystemInfo{}
	if err := json.Unmarshal(data, info); err != nil {
		return nil, err
	}
	return info, nil
}

// GetRaw 发送签名 GET，返回 data 原始 JSON。
func (c *Client) GetRaw(path string) (json.RawMessage, error) {
	return c.GetRawCtx(context.Background(), path)
}

// GetRawCtx 同 GetRaw，带 context。
func (c *Client) GetRawCtx(ctx context.Context, path string) (json.RawMessage, error) {
	return c.DoCtx(ctx, http.MethodGet, path, nil, "")
}

// verify 恒定时间比较（保留给内部扩展用，避免误引入 strings.EqualFold 之类非常量比较）。
func verify(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
