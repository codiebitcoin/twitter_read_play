package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
	"twitter_read_play/internal/watch"
)

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) { s.json(w, s.Roles.List()) }

func (s *Server) getRole(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	s.json(w, rl)
}

// DefaultSpeakTemplate 新建角色的默认 chat 播报模板（{{TWEET}} 会被替换为推文正文）。
// tts 直读模式的默认是 model.DefaultTTSSpeakTemplate（{{TWEET}}，原文直读）。
func DefaultSpeakTemplate() string {
	return "Rewrite the tweet into natural, direct, spoken-language text suitable for speaking or TTS, " +
		"without changing the original meaning, adding facts, or omitting key information. " +
		"Output only the rewritten spoken text; no explanations, titles, Markdown, quotes, or prefixes/suffixes. " +
		"Preserve the original person, emotion, stance, and intensity. " +
		"Keep sentences short, with natural pauses, like a real person speaking. " +
		"And do not say \"I will read the post\".\n\nTweet:\n{{TWEET}}"
}

func (s *Server) upsertRole(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	var role model.Role
	if err := json.Unmarshal(body, &role); err != nil {
		s.err(w, 400, err)
		return
	}
	// platform 白名单校验：曾出事故——前端页面用旧的 bundle 打开时，平台下拉回落到第一项
	// （twitter），保存后微博角色被静默改成 twitter。这里只接受已知平台，未知值直接 400。
	switch strings.ToLower(strings.TrimSpace(role.Platform)) {
	case "", "twitter", "x", "weibo", "xiaohongshu", "instagram", "truth", "bluesky", "bilibili", "rss":
		role.Platform = strings.ToLower(strings.TrimSpace(role.Platform))
	default:
		s.err(w, 400, fmt.Errorf("未知平台 %q（可用：twitter / weibo / xiaohongshu / instagram / truth / bluesky / bilibili / rss）", role.Platform))
		return
	}
	id := r.PathValue("id")
	if id != "" {
		role.ID = id
	}
	// 默认不允许「同一台机器的同一个 slot」被多个角色占用：设备同一时刻只有一个激活槽位
	// + 一套角色/音色，两个角色抢同一槽位会互相覆盖（实测：dev slot1 同时绑 Trump/Paimon，
	// 播报时出现「切了角色没切声音」）。真要共用同一槽位时显式传 allow_slot_conflict=true。
	{
		var opt struct {
			Allow bool `json:"allow_slot_conflict"`
		}
		_ = json.Unmarshal(body, &opt)
		if !opt.Allow {
			if conflicts := s.slotBindingConflicts(id, role.Bindings); len(conflicts) > 0 {
				s.err(w, 400, errStr("槽位冲突："+strings.Join(conflicts, "；")+
					"。一台设备同一时刻只能激活一个槽位，两个角色绑同一槽位会互相覆盖角色/声音。"+
					"请换一台机器或换一个 slot（或显式传 allow_slot_conflict=true 强制保存）"))
				return
			}
		}
	}
	// Voice / VRM 来源模式：新建角色默认「原 slot」（不上传 VRM、不克隆声音）。
	// 更新时留空表示沿用旧值（由 role.Manager.Upsert 保护），不接受未知取值。
	switch role.VoiceVrmMode {
	case "":
		if id == "" {
			role.VoiceVrmMode = model.VoiceVrmModeSlot
		}
	case model.VoiceVrmModeSlot, model.VoiceVrmModeCustom:
	default:
		s.err(w, 400, fmt.Errorf("未知 voice_vrm_mode %q（可用：slot / custom）", role.VoiceVrmMode))
		return
	}
	// 新建角色：提供默认 Character Prompt 与 SpeakText Template（前端都可再改）
	if id == "" {
		if strings.TrimSpace(role.Prompt) == "" {
			role.Prompt = fmt.Sprintf("You are %s. Stay in character at all times: keep your signature tone, emotion, stance and intensity. "+
				"When a tweet is given to you, present it naturally as your own post — do not announce that you are reading a post, "+
				"and never say \"I will read the post\". Never mention being an AI.", role.Name)
		}
		if strings.TrimSpace(role.SpeakTemplateChat) == "" {
			role.SpeakTemplateChat = DefaultSpeakTemplate()
		}
		if strings.TrimSpace(role.SpeakTemplateTTS) == "" {
			role.SpeakTemplateTTS = model.DefaultTTSSpeakTemplate
		}
		// 动作强度默认 STRONG（3）：与设备默认一致，新角色一出生就是明确的强动作，
		// 不用先「沿用」再靠设备兜底。
		if role.AnimationIntensity == 0 {
			role.AnimationIntensity = model.AnimIntensityHigh
		}
	}
	// enabled 缺省语义（曾出事故：新建角色/部分字段 PUT 没带 enabled → bool 零值 false，
	// 角色被静默停用，RolesPage 上出现莫名其妙的「停用」标签且不再自动播报）：
	//   新建（无 id）→ 默认启用；更新 → 保留原值，绝不由缺省值把角色关掉。
	var probe map[string]json.RawMessage
	_ = json.Unmarshal(body, &probe)
	if _, ok := probe["enabled"]; !ok {
		if id == "" {
			role.Enabled = true
		} else if old, gerr := s.Roles.Get(id); gerr == nil {
			role.Enabled = old.Enabled
		}
	}
	// 运行态字段由服务端维护：前端 PUT 的是「打开页面那一刻」的快照，直接回写旧值会导致
	// 立刻重复轮询（上游计费）、重复播报同一批新推文、自动随机播报提前触发。
	if id != "" {
		if old, gerr := s.Roles.Get(id); gerr == nil {
			role.LastPollAt = old.LastPollAt
			role.LastTweetID = old.LastTweetID
			role.LastRandomAt = old.LastRandomAt
			// Stream 游标是运行态：前端 PUT 的是打开页面时的快照，回写会把已消费 seq 打回去、重复播。
			// 换源或刚打开开关时清掉游标，下一轮从当前最新弹幕接着听（丢掉积压）。
			if role.LiveSourceID != old.LiveSourceID || (role.LiveEnabled && !old.LiveEnabled) {
				role.LiveLastSeq = 0
				role.LiveLastAt = time.Time{}
			} else {
				role.LiveLastSeq = old.LiveLastSeq
				role.LiveLastAt = old.LiveLastAt
			}
			// 平台以「数据源」为准：页面是旧快照时会把平台写回默认 twitter（实测发生过，
			// 微博角色被写坏 → 去 twitter 拉中文账号）。这里用角色绑定的上游平台纠正回来；
			// 真要换平台，前端换平台时会把不匹配的 upstream_id 清掉（那时不会再纠正）。
			role.Platform = correctPlatformByUpstream(role.Platform, role.UpstreamID, old, s)
		}
	}
	// 解析 Feed/Live 目标并做选源预检。格式不支持 → 400；缺渠道 → warning（仍保存）。
	validation, verr := s.parseRoleTargets(&role)
	if verr != nil {
		s.err(w, http.StatusBadRequest, verr)
		return
	}
	role.Validation = nil // 不落库
	if err := s.Roles.Upsert(&role); err != nil {
		s.err(w, 500, err)
		return
	}
	var liveValidation *model.RoleTargetValidation
	if validation != nil {
		liveValidation = validation.Live
	}
	s.syncRoleLiveSource(&role, liveValidation)
	role.Validation = validation
	s.json(w, &role)
}

func (s *Server) delRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if rl, err := s.Roles.Get(id); err == nil && s.Live != nil && strings.HasPrefix(rl.LiveSourceID, "role-live-") {
		s.Live.DeleteConfig(rl.LiveSourceID)
	}
	if err := s.Roles.Delete(id); err != nil {
		s.err(w, 400, err)
		return
	}
	s.Watches.ForgetBookProgress(id)
	// 书归属角色：删角色时级联删掉它名下的书（索引 + 文件 + 进度）。
	if lib := s.Watches.BookLibrary(); lib != nil {
		lib.DeleteByRole(id)
	}
	s.json(w, map[string]bool{"ok": true})
}

func (s *Server) roleTweets(w http.ResponseWriter, r *http.Request) {
	// force=1 强制拉新（「刷新推文」按钮）；默认 SQLite 缓存秒开 + 后台刷新/翻译
	// 游标分页：before=上一页最后一条的 tid，limit 每页条数（默认 50，上限 200）
	q := r.URL.Query()
	limit := 0
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = n
	}
	// more=1：缓存翻到底后向上游翻历史页（计费调用，前端「加载更多」触发）
	// checked=1：只返回某池勾选中的推文（随机池/parody 背景池），支持 before+limit 游标分页；
	//            不传 limit 时取全部（Feed 顶部池区一次展示全量，兼容旧行为）
	// count=1：只回总数 {"total":N}（池/搜索结果分页显示「已加载 x / 共 N 条」，不拉行数据）
	// kw：关键字过滤（正文/编辑文本/引用/转推文本），与 before 游标分页兼容
	if q.Get("count") == "1" {
		if q.Get("checked") == "1" {
			s.json(w, map[string]int{"total": s.Watches.CheckedTweetCount(r.PathValue("id"), q.Get("pool"))})
		} else {
			s.json(w, map[string]int{"total": s.Watches.TweetCount(r.PathValue("id"), q.Get("kw"))})
		}
		return
	}
	var rows []store.TweetRow
	if q.Get("checked") == "1" {
		if limit <= 0 || limit > 200 {
			limit = 0 // 未指定/越界：取全部（池量级有限，前端首屏只按页拉时始终带 limit）
		}
		rows = s.Watches.RoleCheckedTweets(r.PathValue("id"), q.Get("pool"), q.Get("before"), limit)
	} else {
		rows = s.Watches.RoleTweetRows(r.PathValue("id"), q.Get("force") == "1", q.Get("before"), q.Get("kw"), limit, q.Get("more") == "1")
	}
	if rows == nil {
		rows = []store.TweetRow{}
	}
	// 接口一定要带 platform：老缓存行（列刚加）没值时用角色的平台补齐
	if rl, err := s.Roles.Get(r.PathValue("id")); err == nil && rl != nil {
		fallback := strings.ToLower(strings.TrimSpace(rl.Platform))
		if fallback == "" || fallback == "x" {
			fallback = "twitter"
		}
		for i := range rows {
			if rows[i].Platform == "" {
				rows[i].Platform = fallback
			}
		}
	}
	s.json(w, rows)
}

// roleVoiceReclone POST /api/roles/{id}/voice/reclone {machine_id, slot}：
// 异步发起重新克隆（分钟级）：预登记任务并立即返回，结果由前端轮询 /api/clone/tasks 展示
// （克隆期间 re-clone 按钮灰掉、显示「克隆中」，完成后任务带 voice_id）。
// 同机已有克隆进行中 → 409 CLONE_BUSY。
func (s *Server) roleVoiceReclone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MachineID string `json:"machine_id"`
		Slot      int    `json:"slot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.MachineID == "" || in.Slot <= 0 {
		s.err(w, 400, fmt.Errorf("machine_id and slot required"))
		return
	}
	task, err := s.Watches.StartReclone(r.PathValue("id"), in.MachineID, in.Slot)
	if err != nil {
		if errors.Is(err, watch.ErrCloneBusy) {
			w.WriteHeader(http.StatusConflict)
			s.json(w, map[string]any{"error": err.Error(), "code": "CLONE_BUSY"})
			return
		}
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "async": true, "task": task})
}

// roleApplyConfig POST /api/roles/{id}/apply {machine_id?, slot?}:
// 仅 PATCH 基础角色配置(prompt/name/desc/sex/intensity + 已记录 voice)到设备,不等 VRM/克隆。
// 异步执行,结果进同步日志("config patch applied (slot N)")。
func (s *Server) roleApplyConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MachineID string `json:"machine_id"`
		Slot      int    `json:"slot"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	go s.Watches.ApplyConfig(r.PathValue("id"), in.MachineID, in.Slot)
	s.json(w, map[string]any{"ok": true, "async": true})
}

// roleVRMPush POST /api/roles/{id}/vrm/push {machine_id?, slot?}:
// 仅上传激活的本地 VRM(上传完成自动 apply:SelectCloth/三件套)。异步,结果进同步日志。
func (s *Server) roleVRMPush(w http.ResponseWriter, r *http.Request) {
	rl, err := s.Roles.Get(r.PathValue("id"))
	if err != nil {
		s.err(w, 404, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	if rl.LocalVrm == "" {
		s.json(w, map[string]any{"error": "no local VRM on this role", "code": "NO_VRM"})
		return
	}
	var in struct {
		MachineID string `json:"machine_id"`
		Slot      int    `json:"slot"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	go s.Watches.PushVRM(rl.ID, in.MachineID, in.Slot)
	s.json(w, map[string]any{"ok": true, "async": true})
}

// cloneTasks GET /api/clone/tasks：voice_clone 后台任务记录（最新在前）。
// 前端轮询：running 时灰掉该机器的 re-clone/克隆按钮并显示「克隆中」。
func (s *Server) cloneTasks(w http.ResponseWriter, r *http.Request) {
	s.json(w, s.Watches.CloneTasks())
}

// roleCheck POST /api/roles/{id}/check：核对设备 voice/model/prompt 与角色配置，不一致自动修复。
func (s *Server) roleCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MachineID string `json:"machine_id"`
		Slot      int    `json:"slot"`
		// probe_voice：是否先试听验证已记录的克隆能播（默认 true，会外放一小段）。
		// 传 false 可静默校验（不试听），代价是设备没登记克隆槽时会走重克隆。
		ProbeVoice *bool `json:"probe_voice"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	probe := true
	if in.ProbeVoice != nil {
		probe = *in.ProbeVoice
	}
	// 异步执行：check 可能触发 voice_clone（分钟级），同步等待会被 nginx 60s 超时 504。
	// 结果写入系统日志（kind=sync），Status 面板轮询展示。
	go s.Watches.CheckRole(r.PathValue("id"), in.MachineID, in.Slot, watch.CheckOpts{ProbeVoice: probe})
	s.json(w, map[string]any{"ok": true, "async": true, "probe_voice": probe})
}

// roleSync POST /api/roles/{id}/sync：一键同步角色包到 Codie。
// body 可选 {machine_id, slot}：角色未绑定时由前端弹槽位设置后带上，绑定会被记住（存角色）。
func (s *Server) roleSync(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MachineID string `json:"machine_id"`
		Slot      int    `json:"slot"`
		// probe_voice：同步前是否试听验证已记录的克隆能播（默认 true，会外放一小段）
		ProbeVoice *bool `json:"probe_voice"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	probe := true
	if in.ProbeVoice != nil {
		probe = *in.ProbeVoice
	}
	if in.MachineID != "" && in.Slot > 0 {
		// 绑定不存在时登记（记住 machine/slot ↔ role 绑定），已存在则不重复
		rl, err := s.Roles.Get(r.PathValue("id"))
		if err != nil {
			s.err(w, 404, err)
			return
		}
		found := false
		for _, b := range rl.Bindings {
			if b.MachineID == in.MachineID && b.Slot == in.Slot {
				found = true
				break
			}
		}
		if !found {
			rl.Bindings = append(rl.Bindings, model.Binding{MachineID: in.MachineID, Slot: in.Slot, Enabled: true})
			rl.UpdatedAt = time.Now()
			if err := s.Roles.Upsert(rl); err != nil {
				s.err(w, 500, err)
				return
			}
		}
	}
	if err := s.Watches.SyncRole(r.PathValue("id"), in.MachineID, in.Slot, probe); err != nil {
		if errors.Is(err, watch.ErrNeedBinding) {
			s.json(w, map[string]any{"error": err.Error(), "code": "NEED_BINDING"})
			return
		}
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// roleVoiceCheck GET /api/roles/{id}/voice?machine_id=&slot=：
// 检查该绑定的 voice_id 是否在设备上仍可用（machine/slot 1:1）。
func (s *Server) roleVoiceCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	machineID := q.Get("machine_id")
	slot, _ := strconv.Atoi(q.Get("slot"))
	if machineID == "" || slot <= 0 {
		s.err(w, 400, fmt.Errorf("machine_id and slot required"))
		return
	}
	voiceID, groupKey := s.Roles.BindingVoice(r.PathValue("id"), machineID, slot)
	if voiceID == "" {
		s.json(w, map[string]any{"exists": false, "voice_id": "", "group_key": groupKey, "recorded": false})
		return
	}
	exists, err := s.Watches.VoiceExists(machineID, voiceID)
	if err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]any{"exists": exists, "voice_id": voiceID, "group_key": groupKey, "recorded": true})
}

// roleVoiceAudition POST /api/roles/{id}/voice/audition {machine_id, slot, text?}：
// 用该绑定的 voice 在对应机器上试听一段文本（tts_audition，设备直接外放）。
func (s *Server) roleVoiceAudition(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MachineID string `json:"machine_id"`
		Slot      int    `json:"slot"`
		Text      string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.MachineID == "" || in.Slot <= 0 {
		s.err(w, 400, fmt.Errorf("machine_id and slot required"))
		return
	}
	voiceID, groupKey := s.Roles.BindingVoice(r.PathValue("id"), in.MachineID, in.Slot)
	if voiceID == "" {
		s.json(w, map[string]any{"error": "no voice recorded on this binding — sync or re-clone first", "code": "NO_VOICE"})
		return
	}
	c, err := s.Machines.Client(in.MachineID)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	text := in.Text
	if text == "" {
		text = "This is a voice audition test."
	}
	if err := c.TTSAudition(voiceID, text, groupKey); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "voice_id": voiceID, "group_key": groupKey})
}

func (s *Server) roleBroadcast(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TweetID   string `json:"tweet_id"`
		MachineID string `json:"machine_id"` // 本次播报目标机器（空=角色绑定）
		Slot      int    `json:"slot"`       // 本次播报目标槽位（0=角色绑定）
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	// 播报统一入队（按机器串行消费）：解析错误当场返回，设备执行结果记在队列条目上。
	source := "manual"
	if in.TweetID == "" {
		source = "random"
	}
	items, err := s.Watches.EnqueueBroadcast(r.PathValue("id"), in.TweetID, watch.BroadcastOpts{MachineID: in.MachineID, Slot: in.Slot}, source)
	if err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "queued": len(items), "items": items})
}

// roleRandomMode POST /api/roles/{id}/random：random 模式「立即随机播」
// —— 按规则池（关键字/排除/发布日期区间/点赞/勾选）挑一条入队。
func (s *Server) roleRandomMode(w http.ResponseWriter, r *http.Request) {
	items, err := s.Watches.EnqueueRandomMode(r.PathValue("id"))
	if err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "queued": len(items), "items": items})
}

func (s *Server) rolePoll(w http.ResponseWriter, r *http.Request) {
	// 立刻登记 MessageQueue 并返回；拉取在后台跑，状态从 running 收到 done/failed。
	if err := s.Watches.StartPoll(r.PathValue("id")); err != nil {
		code := 500
		if errors.Is(err, watch.ErrPollBusy) {
			code = http.StatusConflict
		}
		s.err(w, code, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// tweetSelect POST /api/roles/{id}/tweets/select：勾选/取消勾选推文。
// pool=random（默认,随机播报池）| parody（仿写背景推文池）——两池数据隔离。
func (s *Server) tweetSelect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string `json:"id"`
		Selected bool   `json:"selected"`
		Pool     string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ID == "" {
		s.err(w, 400, fmt.Errorf("missing id"))
		return
	}
	if err := s.Store.SetTweetSelected(r.PathValue("id"), in.ID, in.Pool, in.Selected); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// tweetEdit POST /api/roles/{id}/tweets/edit：编辑推文文本（原文保留；text 空串=还原原文）。
func (s *Server) tweetEdit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ID == "" {
		s.err(w, 400, fmt.Errorf("missing id"))
		return
	}
	if err := s.Store.SaveTweetEdit(r.PathValue("id"), in.ID, strings.TrimSpace(in.Text)); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	// 过滤：?role_id=&kind=sync|check|broadcast
	roleID := r.URL.Query().Get("role_id")
	kind := r.URL.Query().Get("kind")
	all := s.Watches.Logs(500)
	out := make([]*model.BroadcastLog, 0, len(all))
	for _, l := range all {
		if roleID != "" && l.RoleID != roleID {
			continue
		}
		if kind != "" && l.Kind != kind && !(kind == "broadcast" && l.Kind == "") {
			continue // broadcast 的 Kind 为空串
		}
		out = append(out, l)
	}
	s.json(w, out)
}

// ---- 上游数据源 ----

// correctPlatformByUpstream 用上游声明的平台纠正角色平台。
// 只有当进来的平台是默认值（空/twitter/x）而上游明确声明了别的平台时才纠正：
// 这样「旧页面保存」不会把微博/小红书角色写回 twitter（实测发生过），
// 而人工换平台（前端换平台时会清掉不匹配的 upstream_id）仍然有效。
func correctPlatformByUpstream(incoming, upstreamID string, old *model.Role, s *Server) string {
	in := strings.ToLower(strings.TrimSpace(incoming))
	if in != "" && in != "twitter" && in != "x" {
		return in // 明确指定了微博/小红书，尊重它
	}
	uid := strings.TrimSpace(upstreamID)
	if uid == "" && old != nil {
		uid = strings.TrimSpace(old.UpstreamID)
	}
	if uid != "" {
		if u, err := s.Upstreams.Get(uid); err == nil && u != nil {
			if p := strings.ToLower(strings.TrimSpace(u.Platform)); p != "" && p != "twitter" && p != "x" {
				log.Printf("[role] platform 由默认值 %q 纠正为上游 %s 的平台 %q", incoming, u.Name, p)
				return p
			}
		}
	}
	if in == "" {
		return "twitter"
	}
	return in
}

// slotBindingConflicts 检查这些绑定是否与「别的启用角色」占用同一 machine+slot。
// 返回人类可读的冲突描述（空 = 无冲突）。
func (s *Server) slotBindingConflicts(selfID string, bindings []model.Binding) []string {
	var out []string
	seen := map[string]bool{} // 同一请求内部也去重
	for _, b := range bindings {
		if b.MachineID == "" || b.Slot <= 0 {
			continue
		}
		key := b.MachineID + "#" + strconv.Itoa(b.Slot)
		if seen[key] {
			out = append(out, fmt.Sprintf("本次提交里 %s slot %d 重复绑定", b.MachineID, b.Slot))
			continue
		}
		seen[key] = true
		for _, other := range s.Roles.List() {
			if other.ID == selfID || !other.Enabled {
				continue
			}
			for _, ob := range other.Bindings {
				if !ob.Enabled || ob.MachineID != b.MachineID || ob.Slot != b.Slot {
					continue
				}
				name := b.MachineID
				if m, err := s.Machines.Get(b.MachineID); err == nil && m != nil {
					name = m.Name
				}
				out = append(out, fmt.Sprintf("%s slot %d 已被角色「%s」占用", name, b.Slot, other.Name))
			}
		}
	}
	return out
}
