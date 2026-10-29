package api

import (
	"fmt"
	"net/http"
	"time"
)

// 全局 VRM 上传 job（单例：同一时间只允许一个 VRM 上传在跑，避免重复上传/互相覆盖）。
// job 状态在服务端内存里 —— 前端刷新页面后轮询 GET /api/vrm/job 仍能看到进度并继续弹窗。
type vrmJob struct {
	ID        string     `json:"id"`
	RoleID    string     `json:"role_id"`
	RoleName  string     `json:"role_name"`
	MachineID string     `json:"machine_id"`
	Machine   string     `json:"machine_name"`
	Slot      int        `json:"slot"`
	ClothID   int        `json:"cloth_id"`
	Filename  string     `json:"filename"`
	Phase     string     `json:"phase"` // processing → selecting → done / failed
	Detail    string     `json:"detail,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Finished  *time.Time `json:"finished_at,omitempty"`
}

func (s *Server) vrmJobStart(j *vrmJob) error {
	s.vrmMu.Lock()
	defer s.vrmMu.Unlock()
	if s.vrmCur != nil && s.vrmCur.Finished == nil {
		return fmt.Errorf("another VRM upload is already running (%s)", s.vrmCur.Filename)
	}
	now := time.Now()
	j.ID = fmt.Sprintf("vrm-%d", now.UnixMilli())
	j.StartedAt, j.UpdatedAt = now, now
	s.vrmCur = j
	return nil
}

func (s *Server) vrmJobUpdate(phase, detail string) {
	s.vrmMu.Lock()
	defer s.vrmMu.Unlock()
	if s.vrmCur == nil {
		return
	}
	if phase != "" {
		s.vrmCur.Phase = phase
	}
	if detail != "" {
		s.vrmCur.Detail = detail
	}
	s.vrmCur.UpdatedAt = time.Now()
}

func (s *Server) vrmJobFinish(errMsg string) {
	s.vrmMu.Lock()
	defer s.vrmMu.Unlock()
	if s.vrmCur == nil {
		return
	}
	now := time.Now()
	s.vrmCur.Finished = &now
	s.vrmCur.UpdatedAt = now
	if errMsg != "" {
		s.vrmCur.Phase, s.vrmCur.Error = "failed", errMsg
	} else {
		s.vrmCur.Phase = "done"
	}
}

// GET /api/vrm/job：当前/最近一次全局 VRM 上传 job（前端全局弹窗轮询用）。
func (s *Server) vrmJobGet(w http.ResponseWriter, r *http.Request) {
	s.vrmMu.Lock()
	defer s.vrmMu.Unlock()
	if s.vrmCur == nil {
		s.json(w, map[string]any{"job": nil})
		return
	}
	s.json(w, map[string]any{"job": s.vrmCur})
}
