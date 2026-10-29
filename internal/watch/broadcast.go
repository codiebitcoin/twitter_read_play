package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/twitter"

	clt01 "workshop_inner_sdk"
)

// PollResult 一次轮询的结果，供 MessageQueue 展示（不代表播报成败）。
type PollResult struct {
	Code  string // none | filtered | queued
	Count int    // queued 时入队条数
}

// PollOnce 拉取并播报（随机选新推文，其余作为上下文）。
func (m *Manager) PollOnce(roleID string) (PollResult, error) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return PollResult{}, err
	}
	provider, up, err := m.providerForRole(r)
	if err != nil {
		return PollResult{}, err
	}
	_ = up
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// 缓存键与 feed.fetchKey 同口径：必须带平台，否则 twitter/weibo 同名账号会互相命中
	cacheKey := fmt.Sprintf("%s|%s|%s|%s", provider.Name(), upIDorEmpty(up), normalizePlatform(r.Platform), strings.ToLower(r.Username))
	tweets, err := m.sharedFetch(ctx, provider, cacheKey, time.Duration(r.Interval)*time.Second, r.Username, fetchCount(r))
	if err != nil {
		if twitter.IsQuotaError(err) {
			// 欠费/Key 无效：标记 dead 停止轮询（每次扫描都会重试，只会烧日志），等用户修复
			m.markDead(r.ID, err.Error())
			err = fmt.Errorf("%w (role marked dead; top up / fix key then Poll Now to recover)", err)
		}
		m.appendLog(&model.BroadcastLog{Time: time.Now(), Platform: normalizePlatform(r.Platform), Twitter: r.Username, RoleID: r.ID, Error: "fetch failed: " + err.Error()})
		if isNeedLoginError(err) {
			// 需要人工先去操作页登录（微博深翻页 / 小红书）：按 interval 退避，别每 30s 起一次浏览器
			m.saveRolePollState(roleID, r.LastTweetID)
		}
		return PollResult{}, err
	}
	// 拉取成功：自动解除 dead（上游已恢复）
	m.clearDead(roleID)
	if len(tweets) == 0 {
		// 上游没有返回推文：也刷新轮询时间，否则界面「最后轮询」停滞、每次都像没拉过
		m.saveRolePollState(roleID, r.LastTweetID)
		return PollResult{Code: "none"}, nil
	}

	fresh := pickNewer(tweets, r.LastTweetID)
	if len(fresh) == 0 {
		m.saveRolePollState(roleID, r.LastTweetID)
		return PollResult{Code: "none"}, nil
	}
	// 新推文先过「可播报」闸门：排除词、必须包含词、无正文开关（一条都不合格就不播，
	// 但仍推进 last_tweet_id，否则每轮都会把同一批重新评估一遍）。
	var candidates []model.Tweet
	for _, t := range fresh {
		if m.broadcastable(r, t) {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		m.saveRolePollState(roleID, newestTweetID(tweets))
		return PollResult{Code: "filtered"}, nil
	}
	pick := candidates
	if len(pick) > 1 {
		// 自动播报同样尊重勾选：候选里挑勾选的；无交集才用全部候选
		if sel := m.st.SelectedTweetIDs(roleID); len(sel) > 0 {
			var picked []model.Tweet
			for _, t := range pick {
				if sel[t.ID] {
					picked = append(picked, t)
				}
			}
			if len(picked) > 0 {
				pick = picked
			}
		}
		pick = []model.Tweet{pick[rand.Intn(len(pick))]}
	}
	bindings := m.enabledBindings(r)
	n := 0
	for i := len(pick) - 1; i >= 0; i-- {
		n += len(m.enqueueItems(r, pick[i], bindings, "poll")) // 入队，按机器串行播；失败只记队列/日志
	}
	// 保存本轮见到的最新推文 ID：上游（socialdata）会把置顶推文排在列表第一位，
	// 直接取 tweets[0] 会导致 last_tweet_id 卡在置顶旧推文上，每次轮询都重复播同一条。
	m.saveRolePollState(roleID, newestTweetID(tweets))
	if n == 0 {
		return PollResult{Code: "none"}, nil
	}
	return PollResult{Code: "queued", Count: n}, nil
}

// newestTweetID 列表里最大的 tid（雪花 id 字符串可比较；上游会把置顶旧推文排第一）。
// isNeedLoginError 判断抓取失败是否属于「需要人工登录」：这类错误重试再快也没用，
// 每次都得起一次真实浏览器（十几秒到一分钟），所以按角色的 interval 退避。
func isNeedLoginError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, k := range []string{"need_login", "无登录态", "需要先在操作页登录", "需要登录"} {
		if strings.Contains(msg, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func newestTweetID(tweets []model.Tweet) string {
	if len(tweets) == 0 {
		return ""
	}
	newest := tweets[0].ID
	for _, t := range tweets[1:] {
		if gtID(t.ID, newest) {
			newest = t.ID
		}
	}
	return newest
}

// BroadcastOpts 手动播报的覆盖项：MachineID/Slot 非空时本次播报到指定机器/槽位（不改角色绑定）。
type BroadcastOpts struct {
	MachineID string
	Slot      int
}

// EnqueueBroadcast 解析要播的内容与目标机器，按 (machine, slot) 入队后立即返回。
// 解析类错误（没勾选、推文找不到、没绑定）当场返回，用户能立刻看到；
// 真正的设备播报由队列按机器串行消费，成败记在队列条目上（前端队列弹窗可见）。
// source: manual(点「播报这条」) | random(点「随机播报」) | auto(自动随机) | poll(自动新推文)
func (m *Manager) EnqueueBroadcast(roleID, tweetID string, opts BroadcastOpts, source string) ([]QueueItem, error) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return nil, err
	}
	target := m.enabledBindings(r)
	if opts.MachineID != "" || opts.Slot > 0 {
		b := model.Binding{MachineID: r.FirstMachineID(), Slot: r.FirstSlot(), Enabled: true}
		if opts.MachineID != "" {
			b.MachineID = opts.MachineID
		}
		if opts.Slot > 0 {
			b.Slot = opts.Slot
		}
		target = []model.Binding{b}
	}
	if len(target) == 0 {
		return nil, fmt.Errorf("role has no enabled machine/slot binding")
	}
	// 目标必须是有效绑定：空机器/槽位的条目在队列里永远排不出去（没有对应 worker）
	for _, b := range target {
		if b.MachineID == "" || b.Slot <= 0 {
			return nil, fmt.Errorf("target machine/slot not resolved (machine_id=%q slot=%d)", b.MachineID, b.Slot)
		}
	}
	// 候选池一律来自本地缓存（不产生上游计费）：
	//  - 随机播报：缓存里「已勾选」且通过过滤的推文，勾选的旧推文同样有机会被播；
	//  - 指定推文：按 id 查缓存，上游故障/欠费时缓存里已有的也能照常播。
	// 只有「指定了 id 但缓存里确实没有」才拉一次上游兜底。
	var tw model.Tweet
	if tweetID == "" {
		pool := m.broadcastPool(r)
		if len(pool) == 0 {
			return nil, fmt.Errorf("no broadcastable selected tweets (check some tweets in the Feed tab, or relax filter/require words)")
		}
		tw = pool[rand.Intn(len(pool))]
	} else if row, ok := m.st.TweetByID(roleID, tweetID); ok {
		tw = rowToTweet(row)
	} else {
		tweets, err := m.RoleTweets(roleID, true) // 缓存未命中：拉上游最新一页
		if err != nil {
			return nil, err
		}
		found := false
		for _, t := range tweets {
			if t.ID == tweetID {
				tw, found = t, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("tweet %s not found (not in local cache nor latest tweets)", tweetID)
		}
	}
	return m.enqueueItems(r, tw, target, source), nil
}

// enqueueItems 把一条推文按目标绑定逐个入队（一条 (machine,slot) 一个队列条目）。
// PollOnce 直接用它：那里的推文来自上游即时拉取，未必已经落缓存。
func (m *Manager) enqueueItems(r *model.Role, tw model.Tweet, bindings []model.Binding, source string) []QueueItem {
	items := make([]QueueItem, 0, len(bindings))
	preview := queuePreview(tw)
	for _, b := range bindings {
		name := ""
		if mc, err := m.machines.Get(b.MachineID); err == nil && mc != nil {
			name = mc.Name
		}
		items = append(items, m.queue().enqueue(&QueueItem{
			RoleID: r.ID, RoleName: r.Name, Platform: normalizePlatform(r.Platform), TweetID: tw.ID, Preview: preview,
			MachineID: b.MachineID, Machine: name, Slot: b.Slot,
			Source: source, tw: tw, binding: b,
		}))
	}
	return items
}

// EnqueueRandomMode random 模式：按规则池挑一条入队（前端 random tab 的「立即随机播」）。
// 规则见 randomModePool；池子为空当场返回错误，用户能立刻看到。
func (m *Manager) EnqueueRandomMode(roleID string) ([]QueueItem, error) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return nil, err
	}
	bindings := m.enabledBindings(r)
	if len(bindings) == 0 {
		return nil, fmt.Errorf("role has no enabled machine/slot binding")
	}
	pool := m.randomModePool(r)
	if len(pool) == 0 {
		return nil, fmt.Errorf("no cached tweet matches the random rules (check keywords / date range / likes / checked)")
	}
	return m.enqueueItems(r, pool[rand.Intn(len(pool))], bindings, "random"), nil
}

// maybeRandomMode random 模式自动播：开关打开且到间隔时，从规则池随机挑一条入队。
// 池子为空（规则太严/还没缓存推文）静默跳过，不刷日志。
func (m *Manager) maybeRandomMode(roleID string) {
	r, err := m.roles.Get(roleID)
	if err != nil || !r.RandomEnabled || r.RandomInterval <= 0 {
		return
	}
	if !r.LastRandomAt.IsZero() && time.Since(r.LastRandomAt) < time.Duration(r.RandomInterval)*time.Second {
		return
	}
	pool := m.randomModePool(r)
	if len(pool) == 0 {
		return
	}
	// 先落时间戳再入队：避免池子长期为空时每轮扫描都重试
	m.markRandomAt(roleID)
	m.enqueueItems(r, pool[rand.Intn(len(pool))], m.enabledBindings(r), "random")
}

// markRandomAt 记录 random 模式上次播报时间（持久化在角色上，重启不会立刻重放一次）。
func (m *Manager) markRandomAt(roleID string) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return
	}
	r.LastRandomAt = time.Now()
	_ = m.roles.Upsert(r)
}

// pickNewer 返回 id 大于 last 的推文（雪花 id 可按字符串比较）。
func pickNewer(tweets []model.Tweet, last string) []model.Tweet {
	if len(tweets) == 0 {
		return nil
	}
	if last == "" {
		return tweets[:1]
	}
	out := make([]model.Tweet, 0, len(tweets))
	for _, t := range tweets {
		if t.ID != "" && gtID(t.ID, last) {
			out = append(out, t)
		}
	}
	return out
}

func gtID(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// speakableText 粗口语化：去链接、@x -> at x、去空白。
func speakableText(s string) string {
	linkRe := regexp.MustCompile(`https?://\S+`)
	atRe := regexp.MustCompile(`@(\w+)`)
	s = linkRe.ReplaceAllString(s, "")
	s = atRe.ReplaceAllString(s, "at $1")
	return strings.Join(strings.Fields(s), " ")
}

// playbackText 播报用的完整文本：本推文 + 引用/转发的目标内容一起播。
// RT 的 full_text 常被截断成 "RT @x: …"，正文以 retweeted_status 为准。
func playbackText(tw model.Tweet) string {
	base := strings.TrimSpace(tw.EffectiveText())
	// 纯转发：本条文本就是 "RT @x: …" 截断壳，直接用转发目标正文替换
	if tw.RetweetText != "" {
		rt := strings.TrimSpace(tw.RetweetText)
		trimmed := strings.TrimPrefix(base, "RT @"+tw.RetweetUser+":")
		trimmed = strings.TrimSpace(strings.Trim(trimmed, "…"))
		if trimmed == "" || len([]rune(trimmed)) < len([]rune(rt))/2 {
			base = rt
		} else {
			base = trimmed
		}
	}
	var parts []string
	if base != "" {
		parts = append(parts, base)
	}
	// 纯媒体推文（只有 t.co 占位链接、清洗后无正文）：给一句自然兜底，
	// 否则 TTS 读空串设备报 400（realDonaldTrump 大量视频推文就是这种情况）。
	if base == "" && tw.RetweetText == "" && tw.QuotedText == "" {
		if fb := mediaFallbackText(tw.Media); fb != "" {
			parts = append(parts, fb)
		}
	}
	if tw.RetweetText != "" && strings.TrimSpace(base) != strings.TrimSpace(tw.RetweetText) {
		parts = append(parts, fmt.Sprintf("It also retweeted @%s: %s", tw.RetweetUser, tw.RetweetText))
	}
	if tw.QuotedText != "" {
		parts = append(parts, fmt.Sprintf("Quoting @%s: %s", tw.QuotedUser, tw.QuotedText))
	}
	return strings.Join(parts, " — ")
}

// mediaFallbackText 纯媒体推文的播报兜底文本（无可读正文时用）。
func mediaFallbackText(media string) string {
	switch media {
	case "video", "animated_gif":
		return "Just posted a new video. Check it out."
	case "photo", "image":
		return "Just posted a new photo. Check it out."
	}
	return ""
}

// broadcast 应用角色到设备并播报推文（context 作为人设上下文）。
// 失败原因如实返回（同时记日志），供手动播报 API 反馈给前端。
func (m *Manager) broadcast(ctx context.Context, r *model.Role, b model.Binding, tw model.Tweet, source string) error {
	entry := &model.BroadcastLog{
		Time: time.Now(), Platform: normalizePlatform(r.Platform), Twitter: r.Username, TweetID: tw.ID, Text: tw.EffectiveText(),
		RoleID: r.ID, MachineID: b.MachineID, Source: source,
	}
	defer func() { m.appendLog(entry) }()

	// 播报前**必须**确认设备上的「激活槽位 + 角色/声音」就是本角色。
	// 一台设备同一时刻只有一个激活槽位：别的角色 sync/check 过就会把它抢走，此时直接下发会
	//   ① 报 SLOT_MISMATCH(409)，或 ② 用设备上当前那个角色的声音把本角色的内容播出去
	//      —— 用户实测到「trump 的推文、别人的声音」。
	// 旧实现按「机器空闲 > 3 分钟」跳过这步比对（省 2 次设备 HTTP），恰好是最容易漂移的场景。
	// applyRolePackage(skipIfMatch=true) 本身是原子 SetInfo（检查不匹配才 PUT）+ 必要时 SwitchSlot，
	// 一致时只多 1~2 次轻量 HTTP，远小于一次错误播报的代价。
	step := time.Now()
	var tRolePkg, tRewrite, tBaseline, tSubmit time.Duration
	if err := m.applyRolePackage(r, b, true); err != nil {
		entry.Error = err.Error()
		return errors.New(err.Error())
	}
	tRolePkg = time.Since(step)
	c, err := m.machines.Client(b.MachineID)
	if err != nil {
		entry.Error = err.Error()
		return err
	}
	voiceID, groupKey := m.voiceIDFor(r, b)

	// 播报：统一走原子 PlayChat，不再区分 tts/chat 模式 —— 通道由 SDK 按固件版本决定
	//（≥1.5.3 → chat/turns 走设备 LLM+TTS 管线；1.5.2 → tts_audition 直读合成，
	// 见 docs/chat_qwen_audio_bug.md：设备 chat 入口流式 TTS 对 qwen_audio 无声的问题
	// 在 1.5.2 层由 tts_audition 通道天然规避）。可开 SpeakRewrite：tweet → LLM 口语化。
	text := speakableText(playbackText(tw))
	if strings.TrimSpace(text) == "" {
		// 纯媒体推文（无文字）：读空串设备报 400，给用户明确报错
		entry.Error = "tweet has no readable text (media only), nothing to speak"
		return errors.New(entry.Error)
	}
	if r.SpeakRewrite {
		// tweet → LLM(SpeakPrompt) 口语化；失败回退原文，保证能播出来
		rwStart := time.Now()
		if rewritten, err := m.rewriteForSpeech(r, playbackText(tw)); err != nil {
			log.Printf("[watch] %s speak rewrite failed, reading raw text: %v", r.Username, err)
		} else if s := speakableText(rewritten); s != "" {
			text = s
		}
		tRewrite = time.Since(rwStart)
	}
	// 播报通道：默认按固件版本自动分发（≥1.5.3 → chat/turns，<1.5.3 → tts_audition）；
	// 角色可用 speak_mode 显式指定：tts=TTS 直读（≥1.5.3 走独立文本播报 tts/utterances，
	// <1.5.3 回退 tts_audition 试听通道），chat=LLM+TTS 管线。
	// 通道要先于文本转换算好：播报模板按通道取字段（speak_text_template_chat/tts）。
	override := strings.ToLower(strings.TrimSpace(r.SpeakMode))
	mode := playbackModeOf(c.PredictPlayChatChannel())
	if override == "tts" {
		mode = "tts"
	}
	// <1.5.3 固件不支持 chat/turns：选了 chat 也只能回退 tts（前端会禁用该选项，这里是兜底）
	if override == "chat" && mode == "tts" {
		log.Printf("[playback] machine=%s slot=%d speak_mode=chat but firmware <1.5.3 — falling back to tts",
			b.MachineID, b.Slot)
	}
	// 角色级文本转换（仅 rewrite=template 档：未开 LLM 口语化且配了模板/正则）：
	// 先正则替换，再按模板包占位符（{{TWEET}}/{{NAME}}/{{SEX}}/{{TIME}}/{{LIKES}}/{{USERNAME}}）；
	// 模板按播报通道取 chat/tts 各自字段；LLM 口语化（SpeakRewrite）走自己的 prompt，不套模板
	if !r.SpeakRewrite {
		text = m.applySpeakTransform(r, tw, text, mode)
		if strings.TrimSpace(text) == "" {
			entry.Error = "speak transform produced empty text"
			return errors.New(entry.Error)
		}
	}
	// 基线在真正下发朗读前取（LLM 改写可能耗时几秒，基线要贴着播放动作）；
	// 通道按 SDK 预判，实际通道不同时再补取
	cc := &clt01.CallContext{MachineID: b.MachineID, Slot: b.Slot, CharacterName: r.Name}
	// 绑定上记的 minimax 分组不能用于播报（1.5.2 tts 通道合成时好时坏 → 无声），先兜底
	voiceID, groupKey = m.playableVoice(ctx, c, r, b, voiceID, groupKey)
	// speak 按选定通道下发一次朗读，返回实际通道（重复播报复用）。
	// utteranceID 仅 tts-utterances 通道非空（等待播放完成按任务状态机轮询）；
	// chatID 仅 chat 通道非空（用户停止时用它取消设备会话）。
	var utteranceID, chatID string
	speak := func() (string, error) {
		if mode == "tts" {
			if c.TTSUtteranceAvailable() {
				// ≥1.5.3：独立文本播报（TTS ONLY），用当前槽位角色的声音朗读任意文本
				res, err := c.TTSUtteranceCtx(ctx, b.Slot, text, "")
				if err != nil {
					return "tts", err
				}
				utteranceID = res.UtteranceID
				log.Printf("[playback] machine=%s slot=%d tts via tts/utterances (utterance_id=%s)",
					b.MachineID, b.Slot, utteranceID)
				return clt01.ChannelTTSUtterance, nil
			}
			// <1.5.3：无 utterances 端点，回退 tts_audition 试听通道直读
			utteranceID = ""
			if err := c.TTSAuditionCtx(ctx, voiceID, text, groupKey); err != nil {
				return "tts", err
			}
			return "tts", nil
		}
		res, err := c.PlayChat(ctx, cc, text, &clt01.PlayChatOptions{Language: r.Language, VoiceID: voiceID, GroupKey: groupKey})
		if err != nil {
			return "chat", err
		}
		chatID = res.ChatID
		return res.Channel, nil
	}
	bStart := time.Now()
	baseline := playbackBaseline(ctx, c, mode)
	tBaseline = time.Since(bStart)
	submitStart := time.Now()
	channel, perr := speak()
	// 一台设备同时只有一个「激活槽位」：给同一台机器绑了多个角色时，别的角色 sync 过就会把
	// 激活位抢走，本角色再播就报 SLOT_MISMATCH(409)（实测 musk/trump 被新角色挤掉）。
	// 这里自愈：把自己的槽位重新激活一次再重试播报（只重试一次，避免抖动）。
	if perr != nil && strings.Contains(perr.Error(), "SLOT_MISMATCH") {
		// 激活槽位被别的角色抢走：强制应用本角色包（SetInfo + SwitchSlot）后重试一次。
		// 不能用 SelectCloth/OutfitApply 顶：1.5.3 固件换装要走三件套 model-apply，
		// SelectCloth 会报 OUTFIT_APPLY_REQUIRED(400)（实测）。
		if serr := m.applyRolePackage(r, b, false); serr == nil {
			log.Printf("[playback] slot %d 激活槽位被占，已强制应用角色包（角色+声音）并重试播报", b.Slot)
			if !sleepCtx(ctx, 1500*time.Millisecond) {
				return errPlaybackStopped
			}
			channel, perr = speak()
		} else {
			log.Printf("[playback] slot %d 强制应用角色包失败: %v", b.Slot, serr)
		}
	}
	tSubmit = time.Since(submitStart)
	// 首条播报慢定位日志：受理前各步耗时（不含设备起播本身）
	log.Printf("[playback] steps machine=%s slot=%d tweet=%s role_pkg=%s rewrite=%s baseline=%s submit=%s total_before_submit=%s chars=%d fast_path=%v",
		b.MachineID, b.Slot, tw.ID, tRolePkg.Round(time.Millisecond), tRewrite.Round(time.Millisecond),
		tBaseline.Round(time.Millisecond), tSubmit.Round(time.Millisecond),
		time.Since(step).Round(time.Millisecond), len([]rune(text)), tRewrite == 0 && tBaseline < 200*time.Millisecond)
	if perr != nil {
		// 用户停止（ctx 取消）导致的失败不是播报错误
		if ctx.Err() != nil {
			entry.Error = "stopped"
			return errPlaybackStopped
		}
		entry.Error = "broadcast failed: " + perr.Error()
		return errors.New(entry.Error)
	}
	if actual := playbackModeOf(channel); actual != mode {
		// 固件实际通道与预判不同（版本探测刚升格等），基线与等待方式跟随实际通道
		mode = actual
		baseline = playbackBaseline(ctx, c, mode)
	}
	// 播报期间监听停止请求：ChatRoom stop 取消 ctx 时必须让设备立刻停声，
	// 否则要等这一句自然念完（用户感知就是「按了没反应」）。
	stopWatch := watchDeviceStop(ctx, c, mode, chatID, utteranceID)
	out := waitPlaybackDone(ctx, c, cc, mode, baseline, text, utteranceID)
	stopWatch()
	if out.Stopped {
		// 用户手动停止：既不重探固件（不是设备错），也不标失败
		entry.Error = "stopped"
		log.Printf("[watch] %s broadcast %s stopped by user (machine=%s, slot=%d)", r.Username, tw.ID, b.MachineID, b.Slot)
		return errPlaybackStopped
	}
	if !out.OK {
		// 设备报错/没确认播放：后台重探固件版本 —— 设备刷机降级（如 1.5.3→1.5.2）后
		// DB 里记录的旧版本会让通道选错（chat/turns 打到 1.5.2 固件上必 ERROR），先自愈
		m.kickFirmwareReprobe(b.MachineID)
	}
	if out.Failed {
		// 设备明确报失败：下发成功但没出声 —— 必须记失败，别让队列/前端以为播完了
		entry.Error = out.errMsg()
		return errors.New(entry.Error)
	}
	if !out.OK {
		// 没抓到播放态（可能只是状态窗口没覆盖）：不算失败，但要在日志与队列里留痕
		entry.Error = out.errMsg()
		log.Printf("[watch] %s broadcast %s playback unverified: reason=%s last_state=%s",
			r.Username, tw.ID, out.Reason, out.State)
	}
	entry.OK = true
	log.Printf("[watch] %s broadcast %s (role=%s, channel=%s, speak_mode=%s, machine=%s, slot=%d, playback=%s)",
		r.Username, tw.ID, r.Name, channel, override, b.MachineID, b.Slot, out.Reason)
	// 重复播报：仅重复朗读，不重复下发角色包。
	// Stream Watch 用 LiveRepeatCount/Gap，Feed Watch 用 RepeatCount/Gap，互不借用。
	repeatN, repeatGap := r.RepeatCount, r.RepeatGap
	if source == "live" || tw.Source == model.LiveSource {
		repeatN, repeatGap = r.LiveRepeatCount, r.LiveRepeatGap
	}
	if n := repeatN; n > 1 {
		gap := time.Duration(repeatGap) * time.Second
		if gap <= 0 {
			gap = 30 * time.Second
		}
		for i := 1; i < n; i++ {
			if !sleepCtx(ctx, gap) {
				return errPlaybackStopped
			}
			baseline := playbackBaseline(ctx, c, mode) // 每次重复朗读单独取基线并等播完
			if _, err := speak(); err != nil {
				if ctx.Err() != nil {
					return errPlaybackStopped
				}
				log.Printf("[watch] repeat broadcast %d/%d failed: %v", i+1, n, err)
				return nil
			}
			stopWatch := watchDeviceStop(ctx, c, mode, chatID, utteranceID)
			ro := waitPlaybackDone(ctx, c, cc, mode, baseline, text, utteranceID)
			stopWatch()
			if ro.Stopped {
				return errPlaybackStopped
			}
			if ro.Failed {
				log.Printf("[watch] repeat broadcast %d/%d device failed: %s", i+1, n, ro.errMsg())
				return errors.New(ro.errMsg())
			}
			log.Printf("[watch] %s repeat broadcast %d/%d", r.Username, i+1, n)
		}
	}
	return nil
}

// applySpeakTransform 播报前的角色级文本转换：
//  1. 正则替换：SpeakRegex 每行一条 pattern=>replacement（Go regexp，支持 (?i) 内联 flag；
//     编译失败的行记日志跳过，不阻断播报）；
//  2. 模板包装：按播报通道 mode 取角色模板（SpeakTemplateFor：chat/tts 各自字段），含
//     {{TWEET}} 占位符则整体替换；无占位符则模板在前、正文换行追加。
//
// 全部可配置、可选；未配置时原样返回。
func (m *Manager) applySpeakTransform(r *model.Role, tw model.Tweet, text, mode string) string {
	out := text
	for i, line := range strings.Split(r.SpeakRegex, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pat, rep, ok := strings.Cut(line, "=>")
		if !ok {
			log.Printf("[watch] %s speak_regex line %d missing '=>' separator, skipped", r.Username, i+1)
			continue
		}
		re, err := regexp.Compile(strings.TrimSpace(pat))
		if err != nil {
			log.Printf("[watch] %s speak_regex line %d bad pattern: %v", r.Username, i+1, err)
			continue
		}
		out = re.ReplaceAllString(out, rep)
	}
	tpl := r.SpeakTemplateFor(mode)
	if tpl == "" {
		return out
	}
	// 占位符：{{TWEET}} 正文 · {{NAME}} 角色名 · {{USERNAME}} 账号 · {{SEX}} 性别 ·
	// {{TIME}} 发帖时间 · {{LIKES}} 点赞数（未知为 0）
	sex := ""
	switch r.Sex {
	case 1:
		sex = "male"
	case 2:
		sex = "female"
	case 3:
		sex = "neutral"
	}
	when := tw.CreatedAt
	// 宽口径解析（RFC3339 / Twitter 经典格式 / unix 时间戳 / 微博中文时间）；
	// 解析不出来就原文输出，不 panic 也不报错。
	if t, ok := parseTweetTime(tw.CreatedAt); ok {
		when = t.Format("2006-01-02 15:04")
	}
	rep := strings.NewReplacer(
		"{{TWEET}}", out,
		"{{NAME}}", r.Name,
		"{{USERNAME}}", r.Username,
		"{{SEX}}", sex,
		"{{TIME}}", when,
		"{{LIKES}}", fmt.Sprintf("%d", tw.Likes),
	)
	return rep.Replace(tpl)
}

// playbackModeOf SDK 播报通道 → 宿主播放状态监控模式（基线/等待接口按通道不同）。
func playbackModeOf(channel string) string {
	if channel == clt01.ChannelChatTurns {
		return "chat"
	}
	if channel == clt01.ChannelTTSUtterance {
		return "tts-utterances"
	}
	return "tts"
}
