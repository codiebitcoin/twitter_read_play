package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/watch"

	clt01 "workshop_inner_sdk"
)

// roleVRMUpload 上传 VRM 到角色的设备槽并选中。
func (s *Server) roleVRMUpload(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 220<<20) // VRM 几十 MB 是常态，上限与书上传对齐（M6）
	file, header, err := r.FormFile("file")
	if err != nil {
		s.err(w, 400, err)
		return
	}
	defer file.Close()
	clothID := 2
	if v := r.FormValue("cloth_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			clothID = n
		}
	}
	buf, err := io.ReadAll(file)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	// 本地留档：VRM 属于角色包的一部分，本地可存多个、选一激活（data/roles/{id}/），最多 5 个
	rel, err := s.Roles.SaveAsset(rl.ID, "model-"+strconv.FormatInt(time.Now().Unix(), 10), "vrm", buf)
	if err != nil {
		s.err(w, 500, err)
		return
	}
	// 展示名用上传时的原始文件名（去扩展名），落盘文件名仍是 model-{unix}.vrm 防冲突
	dispName := strings.TrimSuffix(filepath.Base(header.Filename), filepath.Ext(header.Filename))
	if err := s.Roles.AddVrmAsset(rl.ID, rel, clothID, dispName); err != nil {
		_ = os.Remove(s.Roles.AssetPath(rel)) // 没登记成功（如超限）不留孤儿文件
		s.err(w, 400, err)
		return
	}
	target := rl.FirstBinding()
	if v := r.FormValue("machine_id"); v != "" {
		slot := rl.FirstSlot()
		if sv := r.FormValue("slot"); sv != "" {
			if n, err := strconv.Atoi(sv); err == nil && n > 0 {
				slot = n
			}
		}
		target = &model.Binding{MachineID: v, Slot: slot}
	}
	if target == nil || target.MachineID == "" {
		s.json(w, map[string]any{"error": "NEED_BINDING", "code": "NEED_BINDING"})
		return
	}
	c, err := s.Machines.Client(target.MachineID)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	// 全局 job：同一时间只允许一个 VRM 上传（刷新页面/换角色页都看得到，防止重复上传）
	job := &vrmJob{
		RoleID: rl.ID, RoleName: rl.Name,
		MachineID: target.MachineID, Slot: target.Slot, ClothID: clothID,
		Filename: header.Filename, Phase: "processing",
	}
	if m, merr := s.Machines.Get(target.MachineID); merr == nil && m != nil {
		job.Machine = m.Name
	}
	if err := s.vrmJobStart(job); err != nil {
		s.err(w, 409, err)
		return
	}
	// 同一条操作登记进全局 MessageQueue：VRM 上传不再弹独立弹窗，进度在队列里跟进
	op := s.Watches.BeginOp("upload_vrm", rl, target.MachineID, target.Slot)
	opID := ""
	if op != nil {
		opID = op.ID
	}
	finishJob := func(errMsg, syncMsg string) {
		s.vrmJobFinish(errMsg)
		s.Watches.EndOp(opID, errMsg)
		if syncMsg != "" {
			s.Watches.SyncLogByID(rl.ID, target.MachineID, errMsg == "", syncMsg, "")
		}
	}
	// 轮询可达 2 分钟，nginx 60s 会 504 → 剩余处理（上传+轮询）放后台，立即响应。
	// 进度记在全局 job 上（GET /api/vrm/job），结果同时写同步日志面板。
	// Persona 页只上传到设备草稿（apply=false）：应用/换屏由 Status 页（VRM上传 / Check&fix）触发，
	// 那边会检测同名草稿直接应用、不再重传。
	go func() {
		s.vrmJobUpdate("uploading", fmt.Sprintf("cloth %d uploading (%d bytes)", clothID, len(buf)))
		err := watch.UploadVRMDataOpt(c, target.MachineID, target.Slot, clothID, header.Filename, buf, false, func(ok bool, detail, errMsg string) {
			if ok {
				log.Printf("[vrm] %s:%d %s", target.MachineID, target.Slot, detail)
				return
			}
			log.Printf("[vrm] %s:%d %s", target.MachineID, target.Slot, errMsg)
		})
		if err != nil {
			finishJob(err.Error(), err.Error())
			return
		}
		log.Printf("[vrm] %s:%d uploaded to drafts (cloth %d, not applied)", target.MachineID, target.Slot, clothID)
		finishJob("", fmt.Sprintf("VRM uploaded to drafts %s:%d (cloth %d, %s) — apply from Status page", target.MachineID, target.Slot, clothID, header.Filename))
	}()
	s.json(w, map[string]any{"ok": true, "async": true, "job_id": job.ID, "cloth_id": clothID, "machine_id": target.MachineID, "slot": target.Slot})
}

// roleIconUpload POST /api/roles/{id}/icon：上传角色头像（png/jpg/webp/gif，≤5MB）。
func (s *Server) roleIconUpload(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		s.err(w, 400, fmt.Errorf("missing file (max 5MB)"))
		return
	}
	defer file.Close()
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	switch ext {
	case "png", "jpg", "jpeg", "webp", "gif":
	default:
		s.err(w, 400, fmt.Errorf("unsupported icon type: %s (png/jpg/webp/gif)", ext))
		return
	}
	buf, err := io.ReadAll(file)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	if ext == "jpeg" {
		ext = "jpg"
	}
	rel, err := s.Roles.SaveAsset(rl.ID, "icon", ext, buf)
	if err != nil {
		s.err(w, 500, err)
		return
	}
	if err := s.Roles.SetIcon(rl.ID, rel); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "icon": rel})
}

// roleIconFile GET /api/roles/{id}/icon：下发角色头像（无头像 404，前端回退占位）。
func (s *Server) roleIconFile(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	if rl.Icon == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(s.Roles.AssetPath(rl.Icon))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch strings.ToLower(filepath.Ext(rl.Icon)) {
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".gif":
		w.Header().Set("Content-Type", "image/gif")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	default:
		w.Header().Set("Content-Type", "image/jpeg")
	}
	// 内容随上传变化：用 updated_at 做前端缓存破坏，HTTP 层禁缓存即可
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// roleVrmFile GET /api/roles/{id}/vrm/file：下发当前激活的本地 VRM（前端 WebGL 自动预览/缩略图用）。
func (s *Server) roleVrmFile(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	if rl.LocalVrm == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(s.Roles.AssetPath(rl.LocalVrm))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// VRM 较大（几十 MB）且切换激活模型后内容会变：禁缓存；缩略图由前端渲染后内存缓存
	w.Header().Set("Content-Type", "model/gltf-binary")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// roleAssetFile GET /api/roles/{id}/asset?file=…：下载角色的本地资源（VRM / 语音样本 mp3）。
// file 必须是该角色登记过的资源路径（local_vrm/local_vrms/local_voice/local_voices 之一），
// 其余一律 404（防路径穿越）；Content-Disposition: attachment 让浏览器直接下载
// 而不是内联播放 mp3 / 渲染 vrm。Persona 页资源列表的 ⬇ 下载按钮用。
func (s *Server) roleAssetFile(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	file := r.URL.Query().Get("file")
	allowed := map[string]bool{}
	if rl.LocalVrm != "" {
		allowed[rl.LocalVrm] = true
	}
	if rl.LocalVoice != "" {
		allowed[rl.LocalVoice] = true
	}
	for _, v := range rl.LocalVrms {
		allowed[v.File] = true
	}
	for _, v := range rl.LocalVoices {
		allowed[v.File] = true
	}
	if !allowed[file] {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(s.Roles.AssetPath(file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(file)
	ctype := "application/octet-stream"
	switch {
	case strings.HasSuffix(name, ".vrm"):
		ctype = "model/gltf-binary"
	case strings.HasSuffix(name, ".mp3"):
		ctype = "audio/mpeg"
	case strings.HasSuffix(name, ".wav"):
		ctype = "audio/wav"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// roleVrmDelete DELETE /api/roles/{id}/vrm：删除一个本地 VRM（body: {file}，须在资源列表中）。
func (s *Server) roleVrmDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.File == "" {
		s.err(w, 400, fmt.Errorf("missing file"))
		return
	}
	if err := s.Roles.RemoveVrmAsset(r.PathValue("id"), in.File); err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// roleVoiceDelete DELETE /api/roles/{id}/voice：删除一个本地语音样本（body: {file}，须在资源列表中）。
func (s *Server) roleVoiceDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.File == "" {
		s.err(w, 400, fmt.Errorf("missing file"))
		return
	}
	if err := s.Roles.RemoveVoiceAsset(r.PathValue("id"), in.File); err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// roleVrmRename POST /api/roles/{id}/vrm/rename：改一个本地 VRM 的展示名（body: {file, name}）。
func (s *Server) roleVrmRename(w http.ResponseWriter, r *http.Request) {
	var in struct {
		File string `json:"file"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.File == "" {
		s.err(w, 400, fmt.Errorf("missing file"))
		return
	}
	if err := s.Roles.RenameVrm(r.PathValue("id"), in.File, in.Name); err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// roleVrmSelect POST /api/roles/{id}/vrm/select：切换激活的本地 VRM。
func (s *Server) roleVrmSelect(w http.ResponseWriter, r *http.Request) {

	var in struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.File == "" {
		s.err(w, 400, fmt.Errorf("missing file"))
		return
	}
	if err := s.Roles.SelectVrm(r.PathValue("id"), in.File); err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// roleVoiceSelect POST /api/roles/{id}/voice/select：切换激活的语音样本（下一次 clone 用）。
func (s *Server) roleVoiceSelect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.File == "" {
		s.err(w, 400, fmt.Errorf("missing file"))
		return
	}
	if err := s.Roles.SelectVoice(r.PathValue("id"), in.File); err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// roleClone 上传音频触发 voice_clone，并绑定到角色。
func (s *Server) roleClone(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20) // 语音样本上限（M6：大上传撑爆内存）
	file, header, err := r.FormFile("file")
	if err != nil {
		s.err(w, 400, err)
		return
	}
	defer file.Close()
	// 分组/克隆槽默认交给 SDK 决定：自动选支持克隆的分组（minimax 被过滤）、自动挑空闲克隆槽。
	// 表单显式传 group_key / voice_id 时才按表单走（SDK 仍会拒绝 minimax 分组）。
	voiceID := r.FormValue("voice_id")
	groupKey := r.FormValue("group_key")
	buf, err := io.ReadAll(file)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	// 本地留档语音样本（可存多个，选一激活；data/roles/{id}/，最多 5 个）。
	// ext 走 filepath.Ext + 白名单：历史实现取文件名最后一个 "." 之后的整段，
	// multipart 文件名可带 "/"，能拼出目录穿越写入（H3）。
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	switch ext {
	case "":
		ext = "mp3" // 无扩展名沿用历史默认
	case "mp3", "wav", "m4a", "ogg", "flac":
	default:
		s.err(w, 400, fmt.Errorf("unsupported audio type: %s (mp3/wav/m4a/ogg/flac)", ext))
		return
	}
	rel, serr := s.Roles.SaveAsset(rl.ID, "voice-"+strconv.FormatInt(time.Now().Unix(), 10), ext, buf)
	if serr != nil {
		s.err(w, 500, serr)
		return
	}
	if err := s.Roles.AddVoiceAsset(rl.ID, rel); err != nil {
		_ = os.Remove(s.Roles.AssetPath(rel)) // 没登记成功（如超限）不留孤儿文件
		s.err(w, 400, err)
		return
	}
	_ = rel
	// upload_only=1：仅上传样本并激活（本地留档），不触发 clone。
	// 真正的 clone 在绑定面板按机器触发，或 sync 时自动进行（用激活样本）。
	if r.FormValue("upload_only") == "1" {
		if rel != "" {
			_ = s.Roles.SelectVoice(rl.ID, rel)
		}
		s.json(w, map[string]any{"ok": true, "upload_only": true, "file": rel})
		return
	}
	_ = rel
	target := rl.FirstBinding()
	if v := r.FormValue("machine_id"); v != "" {
		slot := rl.FirstSlot()
		if sv := r.FormValue("slot"); sv != "" {
			if n, err := strconv.Atoi(sv); err == nil && n > 0 {
				slot = n
			}
		}
		target = &model.Binding{MachineID: v, Slot: slot}
	}
	if target == nil || target.MachineID == "" {
		s.json(w, map[string]any{"error": "NEED_BINDING", "code": "NEED_BINDING"})
		return
	}
	c, err := s.Machines.Client(target.MachineID)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	// 同步克隆也纳入任务记录：同机互斥（409 CLONE_BUSY）+ 克隆期间前端「克隆中」展示
	task, terr := s.Watches.BeginCloneTask(rl, target.MachineID, target.Slot, "upload")
	if terr != nil {
		w.WriteHeader(http.StatusConflict)
		s.json(w, map[string]any{"error": terr.Error(), "code": "CLONE_BUSY"})
		return
	}
	// 克隆走 SDK 原子：解析分组（默认 qwen，minimax 被拒绝/过滤）+ 挑空闲克隆槽 + confirm 重试
	res, err := c.SetCloneVoice(r.Context(),
		&clt01.CallContext{MachineID: target.MachineID, Slot: target.Slot, CharacterName: rl.Name},
		buf, header.Filename,
		&clt01.CloneVoiceOptions{GroupKey: groupKey, VoiceID: voiceID, VoiceName: rl.Name + "-clone", Apply: true})
	if err != nil {
		s.Watches.FinishCloneTask(task.ID, "", "", err)
		s.err(w, 500, err)
		return
	}
	// voice 配置按 machine/slot 1:1 记录到目标绑定（每台机器不一样）
	_ = s.Roles.SetBindingVoice(rl.ID, target.MachineID, target.Slot, res.VoiceID, res.GroupKey)
	s.Watches.FinishCloneTask(task.ID, res.VoiceID, res.GroupKey, nil)
	s.json(w, map[string]any{"ok": true, "voice_id": res.VoiceID, "group_key": res.GroupKey, "machine_id": target.MachineID, "slot": target.Slot})
}
