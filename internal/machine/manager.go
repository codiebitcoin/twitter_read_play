// Package machine 管理机器（一台设备 = 一份授权与授权配置）与配对流程。
package machine

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"

	clt01 "workshop_inner_sdk"
)

// DefaultPort 设备端口默认值：地址只填 IP/host 时自动补全。
const DefaultPort = "8081"

// NormalizeAddr 规范化设备地址：纯 host 自动补 :8081，已带端口原样保留。
func NormalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimSuffix(addr, "/")
	if addr == "" {
		return addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil && host != "" && port != "" {
		return addr // 已带端口
	}
	return addr + ":" + DefaultPort
}

type pendingPair struct {
	challengeID string
	startedAt   time.Time
}

type Manager struct {
	mu      sync.Mutex
	st      *store.Store
	list    map[string]*model.Machine
	pending map[string]*pendingPair
}

func NewManager(st *store.Store) (*Manager, error) {
	m := &Manager{st: st, list: map[string]*model.Machine{}, pending: map[string]*pendingPair{}}
	var list []*model.Machine
	if err := st.Load("machines", &list); err != nil {
		return nil, err
	}
	for _, it := range list {
		// 存量数据兜底：无端口的地址补默认端口
		if n := NormalizeAddr(it.Addr); n != it.Addr {
			it.Addr = n
		}
		// 清理历史数据：无 user_id 的凭据必然 LOCAL_USER_MISMATCH，直接丢弃
		if it.Creds != nil && it.Creds.UserID == "" {
			it.Creds = nil
			it.EnabledRoles = nil
		}
		m.list[it.ID] = it
	}
	if err := m.save(); err != nil {
		return nil, err
	}
	return m, nil
}

// DropEnabledRoles 从各机器的启用角色白名单里摘掉这些角色 ID。
// 角色被删除后白名单里只剩一个不存在的 id，那台机器会判定「本角色未启用」而静默
// 停止播报；全摘空则回到「未配置 = 全部启用」的默认语义。
func (m *Manager) DropEnabledRoles(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, mc := range m.list {
		if len(mc.EnabledRoles) == 0 {
			continue
		}
		out := make([]string, 0, len(mc.EnabledRoles))
		for _, id := range mc.EnabledRoles {
			if drop[id] {
				changed = true
				continue
			}
			out = append(out, id)
		}
		mc.EnabledRoles = out
	}
	if !changed {
		return nil
	}
	return m.save()
}

func (m *Manager) save() error {
	list := make([]*model.Machine, 0, len(m.list))
	for _, v := range m.list {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	return m.st.Save("machines", list)
}

func (m *Manager) List() []*model.Machine {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Machine, 0, len(m.list))
	for _, v := range m.list {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (m *Manager) Get(id string) (*model.Machine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mc, ok := m.list[id]
	if !ok {
		return nil, fmt.Errorf("machine %q 不存在", id)
	}
	return mc, nil
}

// Add 添加机器（尚未配对）。地址只填 IP/host 时端口默认 8081。
func (m *Manager) Add(name, addr, authScope string) (*model.Machine, error) {
	addr = NormalizeAddr(addr)
	if addr == "" {
		return nil, fmt.Errorf("地址为空")
	}
	if authScope == "" {
		authScope = clt01.AuthScopePermanent
	}
	mc := &model.Machine{
		ID:        fmt.Sprintf("m-%d", time.Now().UnixNano()),
		Name:      name,
		Addr:      addr,
		AuthScope: authScope,
		CreatedAt: time.Now(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list[mc.ID] = mc
	return mc, m.save()
}

// Update 保存机器修改。
func (m *Manager) Update(mc *model.Machine) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.list[mc.ID]; !ok {
		return fmt.Errorf("machine %q 不存在", mc.ID)
	}
	m.list[mc.ID] = mc
	return m.save()
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.list, id)
	delete(m.pending, id)
	return m.save()
}

// Import 导入已有凭据（绕过配对流程）。
func (m *Manager) StartPair(id string) (string, error) {
	mc, err := m.Get(id)
	if err != nil {
		return "", err
	}
	c, err := clt01.New(mc.Addr)
	if err != nil {
		return "", err
	}
	challengeID, _, err := c.RequestPairing()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.pending[id] = &pendingPair{challengeID: challengeID, startedAt: time.Now()}
	m.mu.Unlock()
	return challengeID, nil
}

// CancelPair 取消配对。
func (m *Manager) CancelPair(id string) error {
	m.mu.Lock()
	p := m.pending[id]
	delete(m.pending, id)
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	mc, _ := m.Get(id)
	if mc == nil {
		return nil
	}
	c, err := clt01.New(mc.Addr)
	if err != nil {
		return err
	}
	return c.CancelPairing(p.challengeID)
}

// ConfirmPair 用 6 位配对码完成配对并保存凭据。
func (m *Manager) ConfirmPair(id, code string) (*model.Machine, error) {
	m.mu.Lock()
	p := m.pending[id]
	m.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("machine %q 没有进行中的配对，请先触发配对", id)
	}
	mc, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	c, err := clt01.New(mc.Addr)
	if err != nil {
		return nil, err
	}
	creds, err := c.ConfirmPairing(p.challengeID, code, mc.AuthScope)
	if err != nil {
		return nil, err
	}
	// ConfirmPairing 响应不含 user_id：bootstrap 免签接口补齐。
	// 缺 user_id 的凭据会被启动清理逻辑删掉（教训：配对成功→重启→未配对）。
	if creds.UserID == "" {
		boot, berr := c.Bootstrap()
		if berr != nil || boot.UserID == "" {
			return nil, fmt.Errorf("配对成功但获取 user_id 失败（设备未登录？）: %v", berr)
		}
		creds.UserID = boot.UserID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, id)
	mc.Creds = &model.Creds{
		AuthScope:      creds.AuthScope,
		LocalSecret:    creds.LocalSecret,
		SessionID:      creds.LocalSessionID,
		SessionExpires: creds.SessionExpiresMs,
		DeviceID:       creds.DeviceID,
		UserID:         creds.UserID,
		DeviceAddr:     mc.Addr,
		PairedAt:       time.Now().Unix(),
	}
	return mc, m.save()
}

// ImportCreds 直接导入已有凭据（跳过配对流程，例如从本地 store 迁移）。
// userID 从设备免签名 bootstrap 接口自动获取。
func (m *Manager) ImportCreds(id string, scope, secret, sessionID string) (*model.Machine, error) {
	if secret == "" {
		return nil, fmt.Errorf("local_secret 必填")
	}
	mc, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	c, err := clt01.New(mc.Addr)
	if err != nil {
		return nil, err
	}
	boot, err := c.Bootstrap()
	if err != nil {
		return nil, fmt.Errorf("无法连接设备获取 user_id: %w", err)
	}
	if scope == "" {
		scope = mc.AuthScope
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mc.Creds = &model.Creds{
		AuthScope:   scope,
		LocalSecret: secret,
		SessionID:   sessionID,
		DeviceID:    "",
		UserID:      boot.UserID,
		DeviceAddr:  mc.Addr,
		PairedAt:    time.Now().Unix(),
	}
	return mc, m.save()
}

// NoteError 在设备调用失败后检查是否为凭据失效类错误，是则把凭据标记为已过期。
// 返回 true 表示判定为凭据失效（LOCAL_SESSION_EXPIRED / LOCAL_SECRET_MISMATCH）。
func (m *Manager) NoteError(id string, err error) bool {
	if err == nil {
		return false
	}
	code := ""
	if ae, ok := err.(*clt01.APIError); ok {
		code = ae.Code
	} else if s := err.Error(); strings.Contains(s, "LOCAL_SESSION_EXPIRED") {
		code = "LOCAL_SESSION_EXPIRED"
	} else if strings.Contains(s, "LOCAL_SECRET_MISMATCH") {
		code = "LOCAL_SECRET_MISMATCH"
	} else if strings.Contains(s, "LOCAL_USER_MISMATCH") {
		code = "LOCAL_USER_MISMATCH"
	}
	if code != "LOCAL_SESSION_EXPIRED" && code != "LOCAL_SECRET_MISMATCH" && code != "LOCAL_USER_MISMATCH" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if mc, ok := m.list[id]; ok && mc.Creds != nil {
		mc.Creds.SessionExpires = time.Now().UnixMilli() - 1000 // 置为已过期
		_ = m.save()
	}
	return true
}

// Client 构建已签名的设备客户端。
func (m *Manager) Client(id string) (*clt01.Client, error) {
	mc, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if mc.Creds == nil || mc.Creds.LocalSecret == "" {
		return nil, fmt.Errorf("machine %q 未配对", id)
	}
	c, err := clt01.New(mc.Addr)
	if err != nil {
		return nil, err
	}
	c.Apply(&clt01.Credentials{
		AuthScope:        mc.Creds.AuthScope,
		LocalSecret:      mc.Creds.LocalSecret,
		LocalSessionID:   mc.Creds.SessionID,
		SessionExpiresMs: mc.Creds.SessionExpires,
		DeviceID:         mc.Creds.DeviceID,
		UserID:           mc.Creds.UserID,
		DeviceAddr:       mc.Addr,
	})
	// 已探测过的固件版本直接注入 SDK（跳过重复探测/试探，跨重启保持分层结论）
	if mc.Firmware != "" {
		c.ForceFirmware(clt01.FirmwareVersion(mc.Firmware))
	}
	return c, nil
}

// fwProbeInterval 版本探测的有效期：过期或未探测过时在 Status 成功后自动重探。
const fwProbeInterval = 24 * time.Hour

// probeClient 与 Client 相同，但**不注入已记录的固件版本** —— 专供版本探测用。
// 否则 Firmware() 看到被 ForceFirmware 注入的旧记录就直接返回，探测永远"确认"旧值，
// 设备刷机降级（1.5.3→1.5.2）后永远修不回来。
func (m *Manager) probeClient(id string) (*clt01.Client, error) {
	mc, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if mc.Creds == nil || mc.Creds.LocalSecret == "" {
		return nil, fmt.Errorf("machine %q 未配对", id)
	}
	c, err := clt01.New(mc.Addr)
	if err != nil {
		return nil, err
	}
	c.Apply(&clt01.Credentials{
		AuthScope:        mc.Creds.AuthScope,
		LocalSecret:      mc.Creds.LocalSecret,
		LocalSessionID:   mc.Creds.SessionID,
		SessionExpiresMs: mc.Creds.SessionExpires,
		DeviceID:         mc.Creds.DeviceID,
		UserID:           mc.Creds.UserID,
		DeviceAddr:       mc.Addr,
	})
	return c, nil
}

// ProbeFirmware 主动探测机器固件版本并持久化（添加/配对/重连/定期刷新）。
func (m *Manager) ProbeFirmware(id string) (string, error) {
	c, err := m.probeClient(id)
	if err != nil {
		return "", err
	}
	fw, err := c.Firmware()
	if err != nil && fw == nil {
		return "", err
	}
	version := ""
	if fw != nil {
		version = fw.Version.String()
	}
	m.mu.Lock()
	if mc, ok := m.list[id]; ok {
		mc.Firmware = version
		mc.FwProbedAt = time.Now().UnixMilli()
		_ = m.save()
	}
	m.mu.Unlock()
	return version, err
}

// fwRefreshIfNeeded 连接正常但版本未探测/已过期时主动刷新（尽力而为，不报错）。
func (m *Manager) fwRefreshIfNeeded(id string) {
	mc, err := m.Get(id)
	if err != nil {
		return
	}
	fresh := mc.FwProbedAt > 0 && time.Now().UnixMilli()-mc.FwProbedAt < fwProbeInterval.Milliseconds()
	if mc.Firmware != "" && fresh {
		return
	}
	go func() {
		_, _ = m.ProbeFirmware(id)
	}()
}

// Status 探测机器连接状态（ping + 系统信息）。
func (m *Manager) Status(id string) (map[string]any, error) {
	c, err := m.Client(id)
	if err != nil {
		return map[string]any{"connected": false, "error": err.Error()}, nil
	}
	if err := c.Ping(); err != nil {
		return map[string]any{"connected": false, "error": err.Error()}, nil
	}
	info, err := c.SystemInfo()
	if err != nil {
		authFail := m.NoteError(id, err)
		return map[string]any{"connected": !authFail, "error": err.Error(), "expired": authFail}, nil
	}
	mc, _ := m.Get(id)
	expired := mc != nil && mc.Creds != nil &&
		mc.Creds.AuthScope == clt01.AuthScopeSession && mc.Creds.SessionExpires > 0 &&
		time.Now().UnixMilli() >= mc.Creds.SessionExpires
	// 连接正常但版本未探测/已过期 → 后台主动刷新并持久化
	m.fwRefreshIfNeeded(id)
	out := map[string]any{
		"connected": !expired,
		"username":  info.Username,
		"expired":   expired,
	}
	if mc != nil {
		out["firmware"] = mc.Firmware
	}
	return out, nil
}
