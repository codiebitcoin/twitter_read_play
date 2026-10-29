// clonetask.go voice_clone 后台任务记录。
// 一次克隆分钟级且占用设备克隆槽（21-25，设备级资源）：
//   - 同一台机器同时只允许一个克隆（beginCloneTask 互斥，ErrCloneBusy）；
//   - 任务持久化（store blobs），前端轮询 GET /api/clone/tasks 在克隆期间灰掉
//     re-clone 按钮并显示「克隆中」，完成后带 voice_id 结果；
//   - 服务重启会把悬挂的 running 标记为 failed，避免按钮永久灰掉。
package watch

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"twitter_read_play/internal/model"
)

// ErrCloneBusy 同机已有克隆进行中（调用方据此返回 409，前端灰按钮）。
var ErrCloneBusy = errors.New("voice clone busy on this device")

const maxCloneTasks = 100

// CloneTask 一次 voice_clone 后台任务。
type CloneTask struct {
	ID         string    `json:"id"`
	RoleID     string    `json:"role_id"`
	RoleName   string    `json:"role_name,omitempty"`
	MachineID  string    `json:"machine_id"`
	Slot       int       `json:"slot"`    // 角色绑定槽位（machine/slot 1:1）
	Trigger    string    `json:"trigger"` // reclone / sync / check / upload
	Status     string    `json:"status"`  // running / done / failed
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	VoiceID    string    `json:"voice_id,omitempty"`
	GroupKey   string    `json:"group_key,omitempty"`
	Error      string    `json:"error,omitempty"`
	// 重克隆前的记录（清空前快照）：克隆失败/被重启打断时据此恢复，
	// 避免一次失败的重克隆把可用音色弄丢。
	PrevVoiceID  string `json:"prev_voice_id,omitempty"`
	PrevGroupKey string `json:"prev_group_key,omitempty"`
}

// beginCloneTask 登记一个克隆任务；同机已有 running 任务时返回 ErrCloneBusy（严格互斥，绝不复用）。
func (m *Manager) beginCloneTask(r *model.Role, machineID string, slot int, trigger string) (*CloneTask, error) {
	return m.beginCloneTaskOpt(r, machineID, slot, trigger, false)
}

// beginCloneTaskOpt allowReuse=true 时，同角色/同绑定/同触发的 running reclone 任务可直接续用 ——
// 仅供 ensureVoiceT 克隆段取回 StartReclone 的预登记（避免自己被自己挡住）。
// StartReclone 本身必须走严格互斥（allowReuse=false），否则同绑定重复点击会
// 复用任务记录却又起第二个克隆 goroutine，设备上并发克隆互踩。
func (m *Manager) beginCloneTaskOpt(r *model.Role, machineID string, slot int, trigger string, allowReuse bool) (*CloneTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.cloneTasks {
		if t.Status != "running" || t.MachineID != machineID {
			continue
		}
		if allowReuse && trigger == "reclone" && t.Trigger == "reclone" && t.RoleID == r.ID && t.Slot == slot {
			return t, nil // 自己预登记的任务，续用
		}
		who := t.RoleName
		if who == "" {
			who = t.RoleID
		}
		return nil, fmt.Errorf("%w: %s triggered by %s (slot %d) started %s ago",
			ErrCloneBusy, t.Trigger, who, t.Slot, time.Since(t.StartedAt).Round(time.Second))
	}
	t := &CloneTask{
		ID:        "clone_" + strconv.FormatInt(time.Now().UnixNano(), 10),
		RoleID:    r.ID,
		RoleName:  r.Name,
		MachineID: machineID,
		Slot:      slot,
		Trigger:   trigger,
		Status:    "running",
		StartedAt: time.Now(),
	}
	m.cloneTasks = append(m.cloneTasks, t)
	m.trimCloneTasksLocked()
	m.saveCloneTasksLocked()
	return t, nil
}

// setCloneTaskPrev 记录任务开始前的 voice 记录（清空前快照，供失败/重启恢复）。
func (m *Manager) setCloneTaskPrev(id, voiceID, groupKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.cloneTasks {
		if t.ID == id {
			t.PrevVoiceID, t.PrevGroupKey = voiceID, groupKey
		}
	}
	m.saveCloneTasksLocked()
}

// finishCloneTask 收尾任务（幂等：只收尾 running，重复调用 no-op）。
func (m *Manager) finishCloneTask(id, voiceID, groupKey string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.cloneTasks {
		if t.ID != id || t.Status != "running" {
			continue
		}
		t.FinishedAt = time.Now()
		t.VoiceID, t.GroupKey = voiceID, groupKey
		if err != nil {
			t.Status = "failed"
			t.Error = err.Error()
		} else {
			t.Status = "done"
		}
	}
	m.saveCloneTasksLocked()
}

// trimCloneTasksLocked 超上限时优先淘汰最旧的已结束记录（running 永不淘汰）。
func (m *Manager) trimCloneTasksLocked() {
	for len(m.cloneTasks) > maxCloneTasks {
		cut := -1
		for i, t := range m.cloneTasks {
			if t.Status != "running" {
				cut = i
				break
			}
		}
		if cut < 0 {
			return
		}
		m.cloneTasks = append(m.cloneTasks[:cut], m.cloneTasks[cut+1:]...)
	}
}

func (m *Manager) saveCloneTasksLocked() { _ = m.st.Save("clone_tasks", m.cloneTasks) }

// CloneTasks 全部任务记录（最新在前），供 GET /api/clone/tasks。
func (m *Manager) CloneTasks() []*CloneTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*CloneTask, 0, len(m.cloneTasks))
	for i := len(m.cloneTasks) - 1; i >= 0; i-- {
		out = append(out, m.cloneTasks[i])
	}
	return out
}

// loadCloneTasks 启动时加载持久化任务；上一次进程残留的 running 标记为失败
// （goroutine 已随进程消失，不收尾会让前端按钮永久灰掉）。
func (m *Manager) loadCloneTasks() {
	var ct []*CloneTask
	if err := m.st.Load("clone_tasks", &ct); err != nil {
		return
	}
	now := time.Now()
	for _, t := range ct {
		if t.Status == "running" {
			t.Status = "failed"
			t.Error = "interrupted by service restart"
			t.FinishedAt = now
			// 重启打断的重克隆：清空动作已发生但 goroutine 没来得及恢复 →
			// 按快照把原记录补回去，别让一次失败的重克隆永久丢掉可用音色。
			if t.Trigger == "reclone" && t.PrevVoiceID != "" && m.roles != nil {
				if err := m.roles.SetBindingVoice(t.RoleID, t.MachineID, t.Slot, t.PrevVoiceID, t.PrevGroupKey); err == nil {
					t.Error = "interrupted by service restart (kept previous voice " + t.PrevVoiceID + ")"
					t.VoiceID, t.GroupKey = t.PrevVoiceID, t.PrevGroupKey
				}
			}
		}
	}
	m.cloneTasks = ct
	m.saveCloneTasksLocked()
}

// BeginCloneTask / FinishCloneTask 供 API 层把同步克隆路径（roleClone 上传并克隆）
// 也纳入任务记录：同机互斥 + 前端「克隆中」展示。
func (m *Manager) BeginCloneTask(r *model.Role, machineID string, slot int, trigger string) (*CloneTask, error) {
	return m.beginCloneTask(r, machineID, slot, trigger)
}

func (m *Manager) FinishCloneTask(id, voiceID, groupKey string, err error) {
	m.finishCloneTask(id, voiceID, groupKey, err)
}
