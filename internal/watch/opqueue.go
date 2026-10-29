// opqueue.go 设备操作消息队列（非「播报」类的操作任务）。
//
// PATCH设置 / Check&fix / VRM上传 / voice_clone / 轮询 这类操作，
// 由 API 触发后异步执行；这里逐条登记（running → done/failed），
// 前端 MessageQueue 面板与播报队列统一展示。约定：
//   - 登记必须同步发生在触发请求返回之前（beginOps 在 goroutine 体内先跑），
//     保证前端 notify 后立刻轮询就能看到；
//   - 内存态，重启即清空；已结束条目保留 opKeepDone 供查看；
//   - voice_clone 不在此单独记账：克隆任务（CloneTask，持久化 + 机器级互斥，
//     见 clonetask.go）本身就是权威记录，OpQueue 快照时把最近的克隆任务映射进来即可。
package watch

import (
	"strconv"
	"time"

	"twitter_read_play/internal/model"
)

const (
	opMaxKeep  = 100              // 最多保留条数（超长丢最旧的已结束条目）
	opKeepDone = 10 * time.Minute // 已结束条目保留时长
	opErrMax   = 400              // 错误文本截断长度（check 的 issue 串可能很长）
)

// OpItem 消息队列里的一条操作任务。
type OpItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // patch_config | check_fix | upload_vrm | voice_clone | poll
	Detail    string `json:"detail,omitempty"` // poll 成功时：none | filtered | queued
	Count     int    `json:"count,omitempty"`  // poll queued 的入队条数
	RoleID    string `json:"role_id"`
	RoleName  string `json:"role_name,omitempty"`
	MachineID string `json:"machine_id"`
	Machine   string `json:"machine_name,omitempty"`
	Slot      int    `json:"slot"`
	Status    string `json:"status"` // running | done | failed
	Error     string `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// BeginOp 登记一条进行中的操作（同步调用，返回 nil 表示角色为空不登记）。
// poll 允许没有绑定机器：队列里归到「未绑定机器」。
func (m *Manager) BeginOp(kind string, r *model.Role, machineID string, slot int) *OpItem {
	if r == nil || (machineID == "" && kind != "poll") {
		return nil
	}
	it := &OpItem{
		ID: "op-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Kind: kind, RoleID: r.ID, RoleName: r.Name,
		MachineID: machineID, Slot: slot,
		Status: "running", StartedAt: time.Now(),
	}
	if machineID != "" && m.machines != nil {
		if mc, err := m.machines.Get(machineID); err == nil && mc != nil {
			it.Machine = mc.Name
		}
	}
	m.opsMu.Lock()
	defer m.opsMu.Unlock()
	if kind == "poll" {
		m.dropFinishedOpsLocked(r.ID, "poll")
	}
	m.ops = append(m.ops, it)
	m.pruneOpsLocked()
	return it
}

// EndOp 收尾一条操作（errMsg 为空 = 成功；幂等：只收尾 running）。
func (m *Manager) EndOp(id, errMsg string) {
	m.FinishOp(id, errMsg, "", 0)
}

// FinishOp 收尾一条操作。detail/count 只在成功时有意义（轮询结果说明）。
func (m *Manager) FinishOp(id, errMsg, detail string, count int) {
	if id == "" {
		return
	}
	now := time.Now()
	m.opsMu.Lock()
	defer m.opsMu.Unlock()
	for _, it := range m.ops {
		if it.ID != id || it.Status != "running" {
			continue
		}
		it.FinishedAt = &now
		if errMsg != "" {
			it.Status, it.Error = "failed", truncateOpErr(errMsg)
		} else {
			it.Status = "done"
			it.Detail, it.Count = detail, count
		}
	}
}

// dropFinishedOpsLocked 同一角色同一类操作只留进行中的（调用方持 opsMu）。
func (m *Manager) dropFinishedOpsLocked(roleID, kind string) {
	kept := make([]*OpItem, 0, len(m.ops))
	for _, it := range m.ops {
		if it.RoleID == roleID && it.Kind == kind && it.Status != "running" {
			continue
		}
		kept = append(kept, it)
	}
	m.ops = kept
}

// beginOps 按绑定逐条登记 op（同步执行；绑定由 filterBindings 解析，
// onlyMachineID/onlySlot 与 ApplyConfig/PushVRM/CheckRole 的语义一致）。
func (m *Manager) beginOps(kind string, r *model.Role, onlyMachineID string, onlySlot int) []*OpItem {
	if r == nil {
		return nil
	}
	var out []*OpItem
	for _, b := range m.filterBindings(r, onlyMachineID, onlySlot) {
		if it := m.BeginOp(kind, r, b.MachineID, b.Slot); it != nil {
			out = append(out, it)
		}
	}
	return out
}

// endBindingOp 收尾 beginOps 登记的、machine/slot 对应的那条（errMsg 空=成功）。
func endBindingOp(m *Manager, ops []*OpItem, machineID string, slot int, errMsg string) {
	for _, it := range ops {
		if it != nil && it.MachineID == machineID && it.Slot == slot {
			m.EndOp(it.ID, errMsg)
			return
		}
	}
}

// OpQueue 操作队列快照（登记顺序），并映射最近的 voice_clone 克隆任务。
func (m *Manager) OpQueue() []OpItem {
	m.opsMu.Lock()
	m.pruneOpsLocked()
	out := make([]OpItem, 0, len(m.ops))
	for _, it := range m.ops {
		out = append(out, *it)
	}
	m.opsMu.Unlock()
	// 克隆任务只取运行中的 + 最近结束的，避免历史任务刷屏（全部历史走 /api/clone/tasks）
	for _, t := range m.CloneTasks() {
		if t.Status != "running" && (t.FinishedAt.IsZero() || time.Since(t.FinishedAt) > opKeepDone) {
			continue
		}
		it := OpItem{
			ID: t.ID, Kind: "voice_clone",
			RoleID: t.RoleID, RoleName: t.RoleName,
			MachineID: t.MachineID, Slot: t.Slot,
			Status: t.Status, Error: t.Error,
			StartedAt: t.StartedAt,
		}
		if mc, err := m.machines.Get(t.MachineID); err == nil && mc != nil {
			it.Machine = mc.Name
		}
		if !t.FinishedAt.IsZero() {
			ft := t.FinishedAt
			it.FinishedAt = &ft
		}
		out = append(out, it)
	}
	return out
}

// pruneOpsLocked 清掉过期的已结束条目并限制总长度（调用方持锁）。
// running 永不丢弃 —— 丢一条 running 等于凭空吞掉用户的操作进度。
func (m *Manager) pruneOpsLocked() {
	cut := time.Now().Add(-opKeepDone)
	kept := make([]*OpItem, 0, len(m.ops))
	for _, it := range m.ops {
		if it.Status != "running" && it.FinishedAt != nil && it.FinishedAt.Before(cut) {
			continue
		}
		kept = append(kept, it)
	}
	if drop := len(kept) - opMaxKeep; drop > 0 {
		trimmed := make([]*OpItem, 0, len(kept))
		for _, it := range kept {
			if drop > 0 && it.Status != "running" {
				drop--
				continue
			}
			trimmed = append(trimmed, it)
		}
		kept = trimmed
	}
	m.ops = kept
}

// truncateOpErr 错误文本过长时截断（防止 check 的一长串 issue 撑爆队列条目）。
func truncateOpErr(s string) string {
	r := []rune(s)
	if len(r) <= opErrMax {
		return s
	}
	return string(r[:opErrMax]) + "…"
}