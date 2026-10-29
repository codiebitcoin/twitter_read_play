package clt01

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// 本文件实现固件版本模型：版本号用字符串（"1.5.3"、"1.6.0"，可带 v 前缀），
// 大小比较用数值化的字符串比较（CompareVersions）—— 未来 v1.5.4 / v1.6.x 等新版本
// 无需改任何枚举，按能力阈值（VerChunkedUpload）分发。
//
// 本 SDK 只支持 1.5.3 及以上固件：gzip 压缩分块上传、outfit/apply 应用通道、
// uploaded-models 权威落盘校验。更早的 1.5.2 / legacy 通道已移除。
//
// 设备不暴露版本号，版本按只读能力探测（drafts 端点）确定。

// FirmwareVersion 固件版本号（字符串形式，如 "1.5.3"；比较用 AtLeast/CompareVersions）。
type FirmwareVersion string

// 能力阈值版本（ver.AtLeast(threshold) 即具备该能力层）。
const (
	VerChunkedUpload FirmwareVersion = "1.5.3" // 压缩分块通道（≥1.5.3）
	VerV153          FirmwareVersion = "1.5.3" // ≥ 1.5.3
)

// CompareVersions 数值化字符串版本比较：逐段按整数比较，缺省段按 0，
// 因此 "1.5.10" > "1.5.9"、"1.6" > "1.5.9"、"v1.5.3" == "1.5.3"。
// 返回 -1/0/1。无法解析为数字的段按字符串比较兜底。
func CompareVersions(a, b FirmwareVersion) int {
	as, bs := splitVersion(a), splitVersion(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		if i >= len(as) {
			if zeroRest(bs[i:]) {
				return 0
			}
			return -1
		}
		if i >= len(bs) {
			if zeroRest(as[i:]) {
				return 0
			}
			return 1
		}
		an, aok := parseNum(as[i])
		bn, bok := parseNum(bs[i])
		switch {
		case aok && bok:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aok != bok: // 一方能解析另一方不能：数字段 < 非数字段（如 3 < 4-rc 的 "4-rc"）
			if aok {
				return 1
			}
			return -1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return 0
}

// zeroRest 剩余段是否全为 0（"1.5" vs "1.5.0.0" 视为相等）。
func zeroRest(segs []string) bool {
	for _, s := range segs {
		if n, ok := parseNum(s); !ok || n != 0 {
			return false
		}
	}
	return true
}

// splitVersion 归一化版本串：去空白与 v/V 前缀，去 "-" 预发布后缀，按 "." 切分。
func splitVersion(v FirmwareVersion) []string {
	s := strings.TrimSpace(string(v))
	s = strings.TrimLeft(s, "vV")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	return strings.Split(s, ".")
}

func parseNum(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil
}

// AtLeast 版本比较：v >= o（数值化字符串比较，支持任意未来版本号）。
func (v FirmwareVersion) AtLeast(o FirmwareVersion) bool { return CompareVersions(v, o) >= 0 }

// String 版本串原文。
func (v FirmwareVersion) String() string { return string(v) }

// VersionProfile 固件能力/版本探测结果。Version 是已知/占位版本号，
// 后续新固件（1.5.4、1.6.x）自动落入对应能力层，无需改 SDK。
type VersionProfile struct {
	Version           FirmwareVersion
	HasDrafts         bool // GET /cloth/drafts 可用（当前固件未注册该端点，恒 false）
	HasUploadedModels bool // GET /cloth/uploaded-models 可用（≥1.5.0）
	ChunkedProbed     bool // chunk 端点已实测（true 时 Version 结论含分块通道证据）
}

// firmwareState 缓存的探测结果。挂在指针上，Client 值拷贝（Bootstrap/Ping 内部
// 的 c2 := *c）仍共享同一份缓存，且不引入需要锁拷贝检查的 mutex 字段。
type firmwareState struct {
	mu      sync.Mutex
	profile *VersionProfile
}

func (c *Client) fwState() *firmwareState {
	if c.fw == nil { // 仅零值 Client 直构时出现；New() 已初始化
		c.fw = &firmwareState{}
	}
	return c.fw
}

// Firmware 返回固件版本档案（首次调用探测并缓存，之后复用）。
// 探测失败（非 404 的网络/鉴权错误）时返回当时已知的部分档案与错误。
func (c *Client) Firmware() (*VersionProfile, error) {
	st := c.fwState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.profile != nil {
		return st.profile, nil
	}
	p := &VersionProfile{}
	var firstErr error
	// drafts 探测：路由不存在（旧固件 404）= 无该能力；路由存在但缺参数（400）
	// 或带参成功 = 有该能力（1.5.3+ 时代新增端点）。带参重试一次以确认。
	if _, err := c.GetRaw("/v1/api/data/user/cloth/drafts"); err == nil {
		p.HasDrafts = true
	} else if !isNotFound(err) {
		if ae, ok := err.(*APIError); ok && ae.StatusCode == http.StatusBadRequest {
			if _, err2 := c.GetRaw("/v1/api/data/user/cloth/drafts?slot_number=1"); err2 == nil || !isNotFound(err2) {
				p.HasDrafts = true
			}
		} else {
			firstErr = err
		}
	}
	if _, err := c.GetRaw("/v1/api/data/user/cloth/uploaded-models"); err == nil {
		p.HasUploadedModels = true
	} else if !isNotFound(err) && firstErr == nil {
		firstErr = err
	}
	// 只支持 1.5.3+：drafts 端点是 1.5.3 时代新增，有即 ≥1.5.3；
	// 没有也按 1.5.3 处理（本 SDK 不再提供更低固件的通道）。
	p.Version = VerV153
	p.ChunkedProbed = true
	st.profile = p
	return p, firstErr
}

// ForceFirmware 强制指定固件版本（联调/测试用，如 "v1.6.0"），跳过探测且不再被试探改写。
func (c *Client) ForceFirmware(v FirmwareVersion) {
	st := c.fwState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.profile = &VersionProfile{Version: v, ChunkedProbed: v.AtLeast(VerChunkedUpload)}
}

// fwUpdate 在缓存存在时原地改写（分块试探升格/确认），未探测过则忽略。
func (c *Client) fwUpdate(fn func(*VersionProfile)) {
	st := c.fwState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.profile != nil {
		fn(st.profile)
	}
}

// promoteChunked 分块上传整链路成功 → 标记分块通道已实测。
func (c *Client) promoteChunked() {
	c.fwUpdate(func(p *VersionProfile) {
		p.Version = VerChunkedUpload
		p.ChunkedProbed = true
	})
}

// isNotFound 判断是否为设备端接口不存在（404）。
func isNotFound(err error) bool {
	var apiErr *APIError
	return errAs(err, &apiErr) && apiErr.StatusCode == 404
}

func errAs(err error, target **APIError) bool {
	for err != nil {
		if ae, ok := err.(*APIError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
