package watch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"twitter_read_play/internal/llm"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

// ---- parody 模式：用 LLM 按 prompt 生成仿写推文 ----
// 生成结果落 tweets 表（source=parody），与真实推文是两套独立数据：
// broadcast/random 模式只看 source=real，parody tab 只看 source=parody。
// 三个模式产出的任务最终都进同一个播报队列。

// parody 的默认模板常量在 model 包（model.DefaultParodyPrompt），因为 role 包初始化种子角色
// 时也要用（watch → model 单向依赖，避免 role → watch 循环）。
//
// 模板变量（renderParodyPrompt 负责替换）：
//
//	{{FIXED_USER}}    @用户名
//	{{NAME}}          角色名
//	{{DESCRIPTION}}   角色描述
//	{{CONTEXT_TWEETS}} 引用的帖子（背景池勾选的真实推文,编号列表;无则占位说明）

// llmConfig 从全局 Settings 读 LLM 配置。
func (m *Manager) llmConfig() llm.Config {
	get := func(k string) string {
		v, _ := m.st.GetSetting(k)
		return strings.TrimSpace(v)
	}
	return llm.Config{BaseURL: get("llm_base_url"), APIKey: get("llm_api_key"), Model: get("llm_model")}
}

// renderParodyPrompt 把唯一那条生成 prompt 模板里的变量替换成实际值:
//
//	{{FIXED_USER}} → @username;{{NAME}} → 角色名;{{DESCRIPTION}} → 角色描述;
//	{{CONTEXT_TWEETS}} → 背景池推文编号列表(空池 → "(none)")
func renderParodyPrompt(tpl string, r *model.Role, ctxTweets []model.Tweet) string {
	var ctx string
	if len(ctxTweets) > 0 {
		var b strings.Builder
		for i, t := range ctxTweets {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, strings.TrimSpace(t.EffectiveText())))
		}
		ctx = b.String()
	} else {
		ctx = "(none — write from the persona description alone)"
	}
	rep := strings.NewReplacer(
		"{{FIXED_USER}}", "@"+r.Username,
		"{{NAME}}", r.Name,
		"{{DESCRIPTION}}", orDefault(r.Description, "(no description)"),
		"{{CONTEXT_TWEETS}}", ctx,
	)
	return rep.Replace(tpl)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// GenerateParody 生成一条仿写推文并落库（source=parody）。
// 背景：ParodyBackground 开（或旧字段 ParodyContextN>0）时，取 parody 背景池里的真实推文作为风格参考。
func (m *Manager) GenerateParody(roleID string) (model.Tweet, error) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return model.Tweet{}, err
	}
	cfg := m.llmConfig()
	if !cfg.Ready() {
		return model.Tweet{}, fmt.Errorf("LLM API 未配置完整（base_url / api_key / model，在 Settings 页填）")
	}

	// 生成指令:只有一个 prompt —— 角色的 parody_prompt(空=内置默认模板 model.DefaultParodyPrompt),
	// 渲染变量后作为 system 下发。绝不用 Persona 的 r.Prompt:那是「朗读播报」指令
	// (内置 musk 原文就是 "Start by saying you are reading your latest post, then read it,
	// then add one short witty comment"),拿它当生成 system 会让模型输出
	// 「Alright. I'm reading my latest post.」开场白 + 结尾点评,而不是一条纯推文。
	tpl := strings.TrimSpace(r.ParodyPrompt)
	if tpl == "" {
		tpl = model.DefaultParodyPrompt
	}
	// 背景推文:ParodyBackground 开 → 用 parody 池里挂的全部推文(封顶 50);
	// 旧字段 ParodyContextN>0 仍兼容(取最近 N 条)。
	n := 0
	if r.ParodyBackground {
		n = 50
	} else {
		n = r.ParodyContextN
	}
	ctxTweets := m.parodyContext(r, n)

	// 变量替换:{{FIXED_USER}} / {{NAME}} / {{DESCRIPTION}} / {{CONTEXT_TWEETS}} 全部注入。
	system := renderParodyPrompt(tpl, r, ctxTweets)
	// 兼容旧自定义 prompt:模板里没写 {{CONTEXT_TWEETS}} 时,保持旧行为——背景帖子拼在开头。
	// 判空必须用解析后的 tpl,不能用 r.ParodyPrompt:走内置默认模板时 r.ParodyPrompt 是空串,
	// 旧写法会把背景帖子重复注入一遍。
	if len(ctxTweets) > 0 &&
		!strings.Contains(strings.ToUpper(tpl), "{{CONTEXT_TWEETS}}") &&
		!strings.Contains(strings.ToUpper(system), "YOUR PREVIOUS POSTS") {
		var b strings.Builder
		b.WriteString("Your previous posts (for style reference):\n")
		for i, t := range ctxTweets {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, strings.TrimSpace(t.EffectiveText())))
		}
		b.WriteString("\n")
		system = b.String() + system
	}
	// user 段只是一句触发语:所有指令都在那唯一的 prompt 里,这里不再放第二个模板。
	user := "Write the next post now."

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	text, err := llm.Chat(ctx, cfg, system, user)
	if err != nil {
		return model.Tweet{}, err
	}
	text = cleanLLMText(text)

	// 合成 id：19 位毫秒（与雪花 id 同宽，列表按 tid DESC 排序正确）
	tw := model.Tweet{
		ID:        fmt.Sprintf("%019d", time.Now().UnixMilli()),
		User:      r.Username,
		Text:      text,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Source:    "parody",
	}
	if err := m.st.SaveTweets(roleID, []store.TweetRow{{
		ID: tw.ID, User: tw.User, Text: tw.Text, CreatedAt: tw.CreatedAt, Source: "parody",
	}}); err != nil {
		return model.Tweet{}, err
	}
	log.Printf("[parody] %s generated %s (%d chars)", r.Username, tw.ID, len([]rune(text)))
	return tw, nil
}

// parodyContext 取最近 n 条「已勾选」的真实推文作为风格背景（不产生上游计费）。
func (m *Manager) parodyContext(r *model.Role, n int) []model.Tweet {
	rows := m.st.ParodyBackgroundTweets(r.ID, "", n)
	out := make([]model.Tweet, 0, len(rows))
	for _, row := range rows {
		tw := rowToTweet(row)
		if strings.TrimSpace(tw.EffectiveText()) != "" {
			out = append(out, tw)
		}
	}
	return out
}

// parodyLabelPrefixes 模型爱加的元信息前缀（只在明确匹配时剥掉，避免误伤正文——
// 比如正文真以 "Note:" 开头时不该被砍）。
var parodyLabelPrefixes = []string{
	"post:", "tweet:", "text:", "output:",
	"here is your post:", "here's your post:", "here is a post:", "here's a post:",
	"here is the post:", "here's the post:", "new post:",
}

// parodyPreambleMarkers 「朗读式开场白」的宣告语。模型（尤其被朗读指令带偏时）爱先来一句
// "Alright. I'm reading my latest post. Here we go."，再换行贴正文——这段不是推文内容，要砍掉。
var parodyPreambleMarkers = []string{
	"reading my latest post", "reading my post", "reading this post", "reading the post",
	"i'm reading", "i am reading", "let me read",
	"here's my latest post", "here is my latest post", "here is the post", "here's the post",
}

// stripParodyPreamble 砍掉首行的朗读宣告白（只在首行命中宣告语、且后面还有正文时才砍，
// 避免把正文本身误删）。
func stripParodyPreamble(s string) string {
	lines := strings.SplitN(s, "\n", 2)
	if len(lines) < 2 {
		return s
	}
	first := strings.TrimSpace(lines[0])
	rest := strings.TrimSpace(lines[1])
	if first == "" || rest == "" || len([]rune(first)) > 160 {
		return s
	}
	low := strings.ToLower(first)
	for _, m := range parodyPreambleMarkers {
		if strings.Contains(low, m) {
			return rest
		}
	}
	return s
}

// cleanLLMText 去掉模型爱加的包裹：``` 代码块、朗读式开场白、首尾引号、以及 "Post:" 之类元信息前缀。
// 输出只保留推文正文——开场白/标签/引号一律清掉。
func cleanLLMText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	s = stripParodyPreamble(s)
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, p := range parodyLabelPrefixes {
		if strings.HasPrefix(lower, p) {
			s = strings.TrimSpace(s[len(p):])
			lower = strings.ToLower(s)
			break
		}
	}
	s = strings.Trim(s, "\"'“”「」")
	return strings.TrimSpace(s)
}

// ParodyTweets parody 模式的独立列表（新→旧）。
func (m *Manager) ParodyTweets(roleID string, limit int) []store.TweetRow {
	return m.st.FilterTweets(roleID, "parody", false, 0, limit)
}

// DeleteParodyTweet 删掉一条仿写推文。
func (m *Manager) DeleteParodyTweet(roleID, tid string) error {
	return m.st.DeleteTweet(roleID, tid)
}

// maybeParodyMode parody 模式自动生成+播报：开关打开且到间隔时生成一条并入队。
// LLM 调用是秒级网络请求，必须异步，不能阻塞 scan 的轮询循环。
func (m *Manager) maybeParodyMode(roleID string) {
	r, err := m.roles.Get(roleID)
	if err != nil || !r.ParodyEnabled || r.ParodyInterval <= 0 {
		return
	}
	if !r.LastParodyAt.IsZero() && time.Since(r.LastParodyAt) < time.Duration(r.ParodyInterval)*time.Second {
		return
	}
	// 先落时间戳再生成：生成可能失败/很慢，先记时间避免每轮扫描重复触发
	m.markParodyAt(roleID)
	go func() {
		tw, err := m.GenerateParody(roleID)
		if err != nil {
			log.Printf("[parody] auto generate failed: %v", err)
			return
		}
		r2, err := m.roles.Get(roleID)
		if err != nil {
			return
		}
		m.enqueueItems(r2, tw, m.enabledBindings(r2), "parody")
	}()
}

// markParodyAt 记录 parody 模式上次生成时间。
func (m *Manager) markParodyAt(roleID string) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return
	}
	r.LastParodyAt = time.Now()
	_ = m.roles.Upsert(r)
}
