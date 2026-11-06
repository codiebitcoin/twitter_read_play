package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	clt01 "workshop_inner_sdk"

	"twitter_read_play/internal/assets"
	"twitter_read_play/internal/machine"
)

// discoverMachines GET /api/machines/discover —— 扫描本机所在网段里监听设备端口的地址，
// 供「添加设备」表单的「扫描局域网」按钮使用，用户不必手工去路由器上找 IP。
//
// 有意加了截止时间：空 /24 也能在几秒内返回，不会把表单卡住。
func (s *Server) discoverMachines(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	port := strings.TrimSpace(r.URL.Query().Get("port"))
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		port = machine.DefaultPort
	}
	res := machine.Discover(ctx, port, 300*time.Millisecond)
	s.json(w, res)
}

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	list := s.Machines.List()
	out := make([]map[string]any, 0, len(list))
	for _, mc := range list {
		// M5：设备 local_secret / local_session_id 不回传（配对凭据可全权控制设备）
		out = append(out, toRedactedMap(mc, func(m map[string]any) {
			if creds, ok := m["creds"].(map[string]any); ok {
				redactKeys(creds, "local_secret", "local_session_id")
			}
		}))
	}
	s.json(w, out)
}

func (s *Server) addMachine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		Addr      string `json:"addr"`
		AuthScope string `json:"auth_scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, 400, err)
		return
	}
	if in.Addr == "" {
		s.err(w, 400, errStr(s.tr(r, "err.addr_required")))
		return
	}
	mc, err := s.Machines.Add(in.Name, in.Addr, in.AuthScope)
	if err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, mc)
}

func (s *Server) delMachine(w http.ResponseWriter, r *http.Request) {
	if err := s.Machines.Delete(r.PathValue("id")); err != nil {
		s.err(w, 500, err)
		return
	}
	// 软一致性：摘除所有角色上指向该机器的绑定（无外键，删除时顺手清理）
	if n := s.Roles.RemoveMachineBindings(r.PathValue("id")); n > 0 {
		log.Printf("[machine] 已从 %d 个角色摘除机器 %s 的绑定", n, r.PathValue("id"))
	}
	s.json(w, map[string]bool{"ok": true})
}

func (s *Server) pairStart(w http.ResponseWriter, r *http.Request) {
	challenge, err := s.Machines.StartPair(r.PathValue("id"))
	if err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]string{"challenge_id": challenge})
}

func (s *Server) pairConfirm(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, 400, err)
		return
	}
	mc, err := s.Machines.ConfirmPair(r.PathValue("id"), in.Code)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	// 配对成功立即探测固件版本并持久化（异步，不阻塞配对返回）
	go func() { _, _ = s.Machines.ProbeFirmware(mc.ID) }()
	s.json(w, mc)
}

func (s *Server) pairCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Machines.CancelPair(r.PathValue("id")); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

func (s *Server) machineStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, err := s.Machines.Status(id)
	if err != nil {
		s.err(w, 500, err)
		return
	}
	if connected, _ := st["connected"].(bool); connected {
		go s.provisionSeedRole(id)
	}
	// 初始角色资源自动下发的最后一次失败原因（成功后清空），供界面展示。
	if v, _ := s.Store.GetSetting("provision_error_" + id); v != "" {
		st["provision_error"] = v
	}
	s.json(w, st)
}

// provisionSeedRole 初始角色资源自动下发：每台机器只做一次（内置 VRM + 语音样本）。
// voice_clone（内置语音样本 → 槽 21）+ VRM（内置模型 → cloth 2 并选中）。
// 失败时记录尝试次数（上限 3 次），下次机器在线时重试。
func (s *Server) provisionSeedRole(machineID string) {
	if machineID == "" {
		return
	}
	key := "provisioned_" + machineID
	if v, _ := s.Store.GetSetting(key); v == "1" {
		return
	}
	attKey := key + "_attempts"
	n := 0
	if v, _ := s.Store.GetSetting(attKey); v != "" {
		n, _ = strconv.Atoi(v)
	}
	if n >= 3 {
		log.Printf("[provision] %s: 已失败 %d 次，跳过自动下发（可清除设置 provisioned_%s_attempts 重置）", machineID, n, machineID)
		return
	}
	if !s.provisionMu.TryLock() {
		return
	}
	defer s.provisionMu.Unlock()
	_ = s.Store.SetSetting(attKey, strconv.Itoa(n+1))
	log.Printf("[provision] %s: 开始第 %d/3 次 资源下发", machineID, n+1)

	errKey := "provision_error_" + machineID
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		log.Printf("[provision] %s: %s", machineID, msg)
		_ = s.Store.SetSetting(errKey, fmt.Sprintf("第%d/3次(%s): %s", n+1, time.Now().Format("01-02 15:04"), msg))
	}

	rl, err := s.Roles.Get(assets.SeedRoleID)
	if err != nil {
		fail("初始角色不存在: %v", err)
		return
	}
	c, err := s.Machines.Client(machineID)
	if err != nil {
		fail("获取设备客户端失败: %v", err)
		return
	}
	// VRM 上传超时由 SDK 默认 2 分钟覆盖，无需在此单独放宽。

	// 1) voice_clone：内置样本 → 默认分组（SDK 自动选支持克隆的分组，minimax 被过滤）
	//    各机器各自 clone 并把**实际使用的** voice/group 记录到自己的绑定
	seedSlot := rl.FirstSlot()
	if seedSlot <= 0 {
		seedSlot = 1
	}
	if seedVoiceID, _ := s.Roles.BindingVoice(rl.ID, machineID, seedSlot); seedVoiceID == "" {
		res, err := c.SetCloneVoice(context.Background(),
			&clt01.CallContext{MachineID: machineID, Slot: seedSlot, CharacterName: rl.Name},
			assets.SeedVoice, "seed_voice.mp3",
			&clt01.CloneVoiceOptions{VoiceName: rl.Name + "-seed", Apply: false})
		if err != nil {
			fail("音色克隆失败: %v", err)
			return
		}
		_ = s.Roles.SetBindingVoice(rl.ID, machineID, seedSlot, res.VoiceID, res.GroupKey)
	}

	// 2) VRM：内置模型 → cloth 2 并选中（原子 SetVrm force：上传+应用+校验一步完成）
	clothID := 2
	c.SetHTTPTimeout(15 * time.Minute)
	if _, err := c.SetVrm(context.Background(), &clt01.CallContext{MachineID: machineID, Slot: seedSlot, Cloth: clothID, CharacterName: rl.Name}, "model.vrm", assets.SeedVRM, true, nil); err != nil {
		fail("VRM 下发失败: %v", err)
		return
	}
	_ = s.Store.SetSetting(key, "1")
	_ = s.Store.SetSetting(errKey, "")
	log.Printf("[provision] %s: 资源下发完成", machineID)
}

func (s *Server) machineVoices(w http.ResponseWriter, r *http.Request) {
	c, err := s.Machines.Client(r.PathValue("id"))
	if err != nil {
		s.err(w, 400, err)
		return
	}
	raw, err := c.VoiceGroup(r.Context())
	if err != nil {
		s.err(w, 500, err)
		return
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	s.json(w, out)
}

// machineDeviceProbe 设备诊断：system、槽位 character、已上传模型（只读）。
func (s *Server) machineDeviceProbe(w http.ResponseWriter, r *http.Request) {
	c, err := s.Machines.Client(r.PathValue("id"))
	if err != nil {
		s.err(w, 400, err)
		return
	}
	out := map[string]any{}
	if sys, err := c.SystemInfo(); err == nil {
		out["system"] = sys
	} else {
		out["system_error"] = err.Error()
	}
	chars := map[string]any{}
	for slot := 1; slot <= 5; slot++ {
		if ch, err := c.GetCharacter(slot); err == nil {
			chars[strconv.Itoa(slot)] = ch
		} else {
			chars[strconv.Itoa(slot)] = map[string]string{"error": err.Error()}
		}
	}
	out["characters"] = chars
	if models, err := c.UploadedClothModels(); err == nil {
		var v any
		_ = json.Unmarshal(models, &v)
		out["uploaded_models"] = v
	} else {
		out["uploaded_models_error"] = err.Error()
	}
	s.json(w, out)
}

// ---- 角色 ----

// machineToggleRoles 机器侧启用/停用角色。
func (s *Server) machineToggleRoles(w http.ResponseWriter, r *http.Request) {
	mc, err := s.Machines.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.machine_not_found")))
		return
	}
	var in struct {
		RoleID string `json:"role_id"`
		On     bool   `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, 400, err)
		return
	}
	set := map[string]bool{}
	for _, id := range mc.EnabledRoles {
		set[id] = true
	}
	set[in.RoleID] = in.On
	mc.EnabledRoles = mc.EnabledRoles[:0]
	for id := range set {
		if set[id] {
			mc.EnabledRoles = append(mc.EnabledRoles, id)
		}
	}
	if err := s.Machines.Update(mc); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, mc)
}
