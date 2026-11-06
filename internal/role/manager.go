// Package role 管理角色包（system prompt / voice_clone / voice / model 整体一个包）。
package role

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/assets"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

type Role = model.Role

// maxAssetsPerRole 每类本地资源（VRM / 语音样本）最多保留个数。
const maxAssetsPerRole = 5

type Manager struct {
	mu   sync.Mutex
	st   *store.Store
	list map[string]*Role
}

// NewManager 载入角色库。设计原则：**已有数据一概不动** —— 不删除、不改动任何角色
// （历史事故：曾经按「内置标记」自动清理角色，把用户真在用的角色连同绑定
// voice_id/group_key 与本地 VRM/语音一起删掉）。
// 只有空库（首次初始化）才插入一条普通角色，并把 go:embed 里的资源拷到数据目录。
// 角色没有任何特殊标记：插入之后它和用户自建角色完全一样。
func NewManager(st *store.Store) (*Manager, error) {
	m := &Manager{st: st, list: map[string]*Role{}}
	var list []*Role
	if err := st.Load("roles", &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		m.list[r.ID] = r
	}
	if len(m.list) == 0 {
		for _, p := range presets() {
			m.list[p.ID] = p
		}
		_ = m.save()
		m.seedRoleAssets()
		return m, nil
	}
	// 已有角色：只做派生字段补全（不删不改任何角色数据），并补写一次清掉 JSON 里遗留字段
	if m.normalize() {
		_ = m.save()
	}
	return m, nil
}

// seedRoleAssets 空库初始化专用：把 go:embed 里的 VRM / 语音样本拷到
// data/roles/{SeedRoleID}/，并在角色上登记路径。只在首次初始化调用，
// 不触碰任何已有角色的文件（绝不覆盖用户上传的资源）。
func (m *Manager) seedRoleAssets() {
	r, ok := m.list[assets.SeedRoleID]
	if !ok {
		return
	}
	dir := m.st.Dir + "/roles/" + r.ID
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[role] seed assets: mkdir %s: %v", dir, err)
		return
	}
	changed := false
	if err := os.WriteFile(dir+"/model.vrm", assets.SeedVRM, 0o644); err != nil {
		log.Printf("[role] seed vrm: %v", err)
	} else {
		r.LocalVrm = "roles/" + r.ID + "/model.vrm"
		r.LocalVrms = []model.VrmAsset{{File: r.LocalVrm, ClothID: 2, AddedAt: time.Now()}}
		changed = true
	}
	if err := os.WriteFile(dir+"/voice.mp3", assets.SeedVoice, 0o644); err != nil {
		log.Printf("[role] seed voice: %v", err)
	} else {
		r.LocalVoice = "roles/" + r.ID + "/voice.mp3"
		r.LocalVoices = []model.VoiceAsset{{File: r.LocalVoice, AddedAt: time.Now()}}
		changed = true
	}
	if changed {
		_ = m.save()
	}
	log.Printf("[role] 空库初始化：已插入初始角色 %q 并从 embed 拷贝资源到 %s", r.Name, dir)
}

// ValidRoleID 角色 ID 只允许字母/数字/下划线/连字符（服务端生成的 r-{UnixNano}、
// 种子角色 musk 都符合）。ID 会拼进素材落盘路径，含 / 或 .. 的 ID 可把文件写到
// data/ 之外（H3），入口处一律拒绝。
var roleIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func ValidRoleID(id string) bool { return roleIDRe.MatchString(id) }

// assetFileRe 素材文件名单段（SaveAsset 的 base+ext 产物）。
var assetFileRe = regexp.MustCompile(`^[\w.-]+$`)

// assetRelValid 素材相对路径必须形如 roles/{合法角色ID}/{单段文件名}，
// 拒绝 ..、绝对路径、多级子目录（H3 路径穿越：icon 等字段历史上直接取客户端 JSON）。
func assetRelValid(rel string) bool {
	if rel == "" || strings.Contains(rel, "\\") {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) != 3 || parts[0] != "roles" || !ValidRoleID(parts[1]) {
		return false
	}
	f := parts[2]
	return f != "." && f != ".." && assetFileRe.MatchString(f)
}

// AssetOwned rel 是否为该角色名下的合法素材路径（格式合法 + 归属角色匹配）。
// 保存角色时校验客户端回传的素材字段用：白名单来自服务端写入的值才有意义，
// 客户端手工指定的路径（如 ../app.db）在这里被拒。
func AssetOwned(roleID, rel string) bool {
	return assetRelValid(rel) && strings.SplitN(rel, "/", 3)[1] == roleID
}

// SaveAsset 角色资源（VRM / 语音样本）落盘到 data/roles/{id}/，返回相对路径。
func (m *Manager) SaveAsset(roleID, base, ext string, data []byte) (string, error) {
	if !ValidRoleID(roleID) {
		return "", fmt.Errorf("非法角色 ID %q", roleID)
	}
	if _, err := m.Get(roleID); err != nil {
		return "", err
	}
	dir := m.st.Dir + "/roles/" + roleID
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	rel := "roles/" + roleID + "/" + base + "." + ext
	if err := os.WriteFile(m.st.Dir+"/"+rel, data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

// AssetPath 相对路径 → 绝对路径（data/ 下）。解析后必须仍位于 data/roles/{角色ID}/ 内，
// 非法路径返回 ""（调用方 ReadFile/Remove 自然失败），不再裸拼接（H3）。
func (m *Manager) AssetPath(rel string) string {
	if !assetRelValid(rel) {
		return ""
	}
	return m.st.Dir + "/" + rel
}

// SetIcon 设置角色头像（icon，data/ 相对路径），传空清除。
func (m *Manager) SetIcon(roleID, rel string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	r.Icon = rel
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// AddVrmAsset 登记一个本地 VRM 并设为激活（最多 5 个）。name 为展示名（默认原始文件名，可改名）。
func (m *Manager) AddVrmAsset(roleID, rel string, clothID int, name string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	for _, v := range r.LocalVrms {
		if v.File == rel {
			return nil // 已登记
		}
	}
	if len(r.LocalVrms) >= maxAssetsPerRole {
		return fmt.Errorf("最多保留 %d 个 VRM，请先删除旧的", maxAssetsPerRole)
	}
	r.LocalVrms = append(r.LocalVrms, model.VrmAsset{File: rel, Name: name, ClothID: clothID, AddedAt: time.Now()})
	r.LocalVrm = rel
	r.ClothID = clothID
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// RenameVrm 改一个本地 VRM 的展示名（文件名不动，只改登记的 Name）。
func (m *Manager) RenameVrm(roleID, rel, name string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	found := false
	for i, v := range r.LocalVrms {
		if v.File == rel {
			r.LocalVrms[i].Name = name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("VRM %q 不在角色资源列表中", rel)
	}
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// SelectVrm 切换激活的本地 VRM（切 cloth 记录；设备侧由同步/播报时下发）。
func (m *Manager) SelectVrm(roleID, rel string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	found := false
	for _, v := range r.LocalVrms {
		if v.File == rel {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("VRM %q 不在角色资源列表中", rel)
	}
	r.LocalVrm = rel
	for _, v := range r.LocalVrms {
		if v.File == rel && v.ClothID > 0 {
			r.ClothID = v.ClothID
		}
	}
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// AddVoiceAsset 登记一个本地语音样本并设为激活（clone 用，最多 5 个）。
func (m *Manager) AddVoiceAsset(roleID, rel string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	for _, v := range r.LocalVoices {
		if v.File == rel {
			return nil
		}
	}
	if len(r.LocalVoices) >= maxAssetsPerRole {
		return fmt.Errorf("最多保留 %d 个语音样本，请先删除旧的", maxAssetsPerRole)
	}
	r.LocalVoices = append(r.LocalVoices, model.VoiceAsset{File: rel, AddedAt: time.Now()})
	r.LocalVoice = rel
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// SelectVoice 切换激活的语音样本（下一次 clone 使用）。
func (m *Manager) SelectVoice(roleID, rel string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	found := false
	for _, v := range r.LocalVoices {
		if v.File == rel {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("语音样本 %q 不在角色资源列表中", rel)
	}
	r.LocalVoice = rel
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// RemoveVrmAsset 删除一个本地 VRM（文件 + 登记）：若删的是激活项，自动切到剩余的第一个，没有则清空。
func (m *Manager) RemoveVrmAsset(roleID, rel string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	idx := -1
	for i, v := range r.LocalVrms {
		if v.File == rel {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("VRM %q 不在角色资源列表中", rel)
	}
	if err := os.Remove(m.AssetPath(rel)); err != nil && !os.IsNotExist(err) {
		return err
	}
	r.LocalVrms = append(r.LocalVrms[:idx], r.LocalVrms[idx+1:]...)
	if r.LocalVrm == rel {
		r.LocalVrm, r.ClothID = "", 0
		if len(r.LocalVrms) > 0 {
			r.LocalVrm = r.LocalVrms[0].File
			r.ClothID = r.LocalVrms[0].ClothID
		}
	}
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// RemoveVoiceAsset 删除一个本地语音样本（文件 + 登记）：若删的是激活项，自动切到剩余的第一个，没有则清空。
func (m *Manager) RemoveVoiceAsset(roleID, rel string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	idx := -1
	for i, v := range r.LocalVoices {
		if v.File == rel {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("语音样本 %q 不在角色资源列表中", rel)
	}
	if err := os.Remove(m.AssetPath(rel)); err != nil && !os.IsNotExist(err) {
		return err
	}
	r.LocalVoices = append(r.LocalVoices[:idx], r.LocalVoices[idx+1:]...)
	if r.LocalVoice == rel {
		r.LocalVoice = ""
		if len(r.LocalVoices) > 0 {
			r.LocalVoice = r.LocalVoices[0].File
		}
	}
	r.UpdatedAt = time.Now()
	return m.Upsert(r)
}

// BindingVoice 读取 machine/slot 绑定的 voice 配置（没有记录返回空）。
func (m *Manager) BindingVoice(roleID, machineID string, slot int) (voiceID, groupKey string) {
	r, err := m.Get(roleID)
	if err != nil {
		return "", ""
	}
	for i := range r.Bindings {
		if r.Bindings[i].MachineID == machineID && r.Bindings[i].Slot == slot {
			return r.Bindings[i].VoiceID, r.Bindings[i].GroupKey
		}
	}
	return "", ""
}

// SetBindingVoice 记录 machine/slot 级 voice 配置（1:1，每台机器不一样）：clone 成功或克隆槽变化时调用。
// voiceID/groupKey 传空表示作废该绑定的记录。
func (m *Manager) SetBindingVoice(roleID, machineID string, slot int, voiceID, groupKey string) error {
	r, err := m.Get(roleID)
	if err != nil {
		return err
	}
	found := false
	for i := range r.Bindings {
		b := &r.Bindings[i]
		if b.MachineID == machineID && b.Slot == slot {
			b.VoiceID = voiceID
			b.GroupKey = groupKey
			found = true
		}
	}
	if !found {
		return fmt.Errorf("binding %s/slot %d not found on role %s", machineID, slot, roleID)
	}
	return m.Upsert(r)
}

// normalize 轻量规范化：只补全派生字段，不删除、不改动任何角色的用户数据。
// （内置角色的自动清理逻辑已彻底移除：见 NewManager 注释。）
func (m *Manager) normalize() bool {
	changed := false
	for _, r := range m.list {
		// 预设规范化：Prompt / Model 至少一条且默认激活
		if len(r.Prompts) == 0 && r.Prompt != "" {
			r.Prompts = []model.PromptAsset{{Name: "default", Text: r.Prompt}}
			changed = true
		}
		if len(r.ModelProfiles) == 0 && r.ModelProfile != "" {
			r.ModelProfiles = []model.ModelAsset{{Name: "default", ProfileID: r.ModelProfile}}
			changed = true
		}
		// 内置种子角色:parody prompt 为空则补默认模板(只补一次,用户改过就不再动)
		if r.ID == assets.SeedRoleID && strings.TrimSpace(r.ParodyPrompt) == "" {
			r.ParodyPrompt = model.DefaultParodyPrompt
			changed = true
		}
		// Voice/VRM 来源模式：旧数据没有这个字段。已有本地/绑定音色或本地 VRM 的角色
		// 视为 custom（保持升级前的同步行为），其余为原 slot（默认）。
		if r.VoiceVrmMode != model.VoiceVrmModeSlot && r.VoiceVrmMode != model.VoiceVrmModeCustom {
			r.VoiceVrmMode = legacyVoiceVrmMode(r)
			changed = true
		}
		// 播报模板按通道拆分（speak_text_template_chat/tts）：旧数据只有 speak_template 一个
		// 字段两种模式共用。旧值迁入 chat 模板 —— 旧默认值本就是 chat 风格的朗读指令，且
		// 书朗读包装（bookWrap）也继续取 chat 模板；tts 留空 = 原文直读（与新默认 {{TWEET}}
		// 等效）。迁完清掉旧字段，之后的编辑/保存/播放都只走新字段。
		if strings.TrimSpace(r.SpeakTemplate) != "" && r.SpeakTemplateChat == "" && r.SpeakTemplateTTS == "" {
			r.SpeakTemplateChat = r.SpeakTemplate
			r.SpeakTemplate = ""
			changed = true
		}
	}
	return changed
}

// legacyVoiceVrmMode 旧数据（无 voice_vrm_mode）的推断：只要角色自己带 VRM/音色就是
// custom，否则沿用原 slot。新建角色不会走到这里（服务端显式写 slot）。
func legacyVoiceVrmMode(r *Role) string {
	if r.LocalVrm != "" || r.LocalVoice != "" || len(r.LocalVrms) > 0 || len(r.LocalVoices) > 0 {
		return model.VoiceVrmModeCustom
	}
	for _, b := range r.Bindings {
		if b.VoiceID != "" || b.GroupKey != "" {
			return model.VoiceVrmModeCustom
		}
	}
	return model.VoiceVrmModeSlot
}

// presets 空库初始化用的角色（Elon Musk）：VRM 与语音样本随包内置，
// 首次机器接入时自动推送到设备。就是一条普通角色，没有任何特殊标记。
func presets() []*Role {
	now := time.Now()
	return []*Role{
		{
			ID: assets.SeedRoleID, Name: "Elon Musk",
			Enabled:  true, // 内置角色默认启用（缺省 false 会让它一出生就是「停用」）
			Bindings: []model.Binding{},
			Prompt:   "You are Elon Musk. Read the given post aloud in your unmistakable voice: dry deadpan delivery, short punchy sentences, rockets/Mars/engineering tangents, dry jokes and the occasional self-deprecating aside. Start by saying you are reading your latest post, then read it, then add one short witty comment.",
			Platform: "twitter", Username: "elonmusk", Language: "en", UpdatedAt: now,
			AnimationIntensity: model.AnimIntensityHigh,   // 动作强度默认 STRONG（设备默认）
			ParodyPrompt:       model.DefaultParodyPrompt, // 内置角色预填默认生成 prompt
			VoiceVrmMode:       model.VoiceVrmModeCustom,  // 种子角色自带 VRM/语音样本
		},
	}
}

func (m *Manager) save() error {
	list := make([]*Role, 0, len(m.list))
	for _, v := range m.list {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return m.st.Save("roles", list)
}

func (m *Manager) List() []*Role {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Role, 0, len(m.list))
	for _, v := range m.list {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get 读取角色快照（拷贝）。
// 注意：返回的是副本 —— 调用方读到的是「那一刻」的值，改它不会影响角色库（要改请用 Upsert）。
// 历史上这里直接返回内部指针：GET Promise.all 并发读时，拿到的是**正在被别处改写**的同一对象，
// 表现为「明明刚保存的字段，下一次保存又变回去了」。
func (m *Manager) Get(id string) (*Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.list[id]
	if !ok {
		return nil, fmt.Errorf("role %q 不存在", id)
	}
	return copyRole(r), nil
}

// copyRole 角色深拷贝（切片字段各复制一份；Bindings 里的指针元素按值复制）。
func copyRole(r *Role) *Role {
	c := *r
	c.Bindings = append([]model.Binding(nil), r.Bindings...)
	c.LocalVrms = append([]model.VrmAsset(nil), r.LocalVrms...)
	c.LocalVoices = append([]model.VoiceAsset(nil), r.LocalVoices...)
	c.Prompts = append([]model.PromptAsset(nil), r.Prompts...)
	c.ModelProfiles = append([]model.ModelAsset(nil), r.ModelProfiles...)
	return &c
}

func (m *Manager) Upsert(r *Role) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.ID == "" {
		r.ID = fmt.Sprintf("r-%d", time.Now().UnixNano())
	}
	if old, ok := m.list[r.ID]; ok {
		// 同理保护本地资源指针（旧快照可能没有这些字段）
		if r.LocalVrm == "" && old.LocalVrm != "" {
			r.LocalVrm = old.LocalVrm
		}
		if r.LocalVoice == "" && old.LocalVoice != "" {
			r.LocalVoice = old.LocalVoice
		}
		if len(r.LocalVrms) == 0 && len(old.LocalVrms) > 0 {
			r.LocalVrms = old.LocalVrms
		}
		if len(r.LocalVoices) == 0 && len(old.LocalVoices) > 0 {
			r.LocalVoices = old.LocalVoices
		}
		// 旧前端 PUT 的快照可能没有这个字段：不要用空值把已有模式冲掉。
		if r.VoiceVrmMode == "" && old.VoiceVrmMode != "" {
			r.VoiceVrmMode = old.VoiceVrmMode
		}
	}
	if r.VoiceVrmMode == "" {
		r.VoiceVrmMode = legacyVoiceVrmMode(r)
	}
	if r.VoiceVrmMode != model.VoiceVrmModeCustom {
		r.VoiceVrmMode = model.VoiceVrmModeSlot
	}
	// 软一致性：无效绑定（无机器/槽位）直接丢弃，不做外键约束
	if len(r.Bindings) > 0 {
		out := r.Bindings[:0]
		for _, b := range r.Bindings {
			if b.MachineID != "" && b.Slot > 0 {
				out = append(out, b)
			}
		}
		r.Bindings = out
	}
	// 硬一致性：同一台机器的同一槽位只能绑一个角色（否则互相覆盖 character/VRM/voice）
	for _, b := range r.Bindings {
		for id, other := range m.list {
			if id == r.ID {
				continue
			}
			for _, ob := range other.Bindings {
				if ob.MachineID == b.MachineID && ob.Slot == b.Slot {
					name := other.Name
					if name == "" {
						name = id
					}
					return fmt.Errorf("slot conflict: machine %s slot %d is already bound to role %q — one slot holds one role; pick another slot", b.MachineID, b.Slot, name)
				}
			}
		}
	}
	r.UpdatedAt = time.Now()
	m.list[r.ID] = r
	return m.save()
}

// RemoveMachineBindings 软一致性清理：机器删除后，摘除所有角色上指向它的绑定。
// 返回发生变更的角色数。
func (m *Manager) RemoveMachineBindings(machineID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := 0
	for _, r := range m.list {
		n := len(r.Bindings)
		out := r.Bindings[:0]
		for _, b := range r.Bindings {
			if b.MachineID != machineID {
				out = append(out, b)
			}
		}
		r.Bindings = out
		if len(r.Bindings) != n {
			r.UpdatedAt = time.Now()
			changed++
		}
	}
	if changed > 0 {
		_ = m.save()
	}
	return changed
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.list[id]; !ok {
		return fmt.Errorf("role %q 不存在", id)
	}
	delete(m.list, id)
	return m.save()
}
