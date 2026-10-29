package watch

import (
	"strings"
	"testing"

	"twitter_read_play/internal/model"
)

// 唯一的生成 prompt(DefaultParodyPrompt)必须支持变量替换:身份三件套 + 背景推文列表。
func TestRenderParodyPrompt(t *testing.T) {
	r := &model.Role{Name: "Elon Musk", Username: "elonmusk", Description: "Rockets & AI"}
	tweets := []model.Tweet{
		{ID: "1", Text: "hello world"},
		{ID: "2", Text: "second post"},
	}
	out := renderParodyPrompt(model.DefaultParodyPrompt, r, tweets)
	for _, want := range []string{"@elonmusk", "Elon Musk", "Rockets & AI", "1. hello world", "2. second post"} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染结果缺 %q", want)
		}
	}
	if strings.Contains(out, "{{") {
		t.Error("模板变量未替换干净")
	}
	// 空池 → (none...) 占位
	out2 := renderParodyPrompt(model.DefaultParodyPrompt, r, nil)
	if !strings.Contains(out2, "(none") {
		t.Error("空背景池应填占位说明")
	}
}

// 没配 prompt 的角色(比如只填了名字的 Elon Musk)也要渲染出带自己名字/用户名的默认人设。
func TestDefaultParodyPromptRendersPersonaForElonMusk(t *testing.T) {
	r := &model.Role{Name: "Elon Musk", Username: "elonmusk"} // Description 故意留空
	out := renderParodyPrompt(model.DefaultParodyPrompt, r, nil)
	for _, want := range []string{"Elon Musk", "@elonmusk", "(no description)"} {
		if !strings.Contains(out, want) {
			t.Errorf("默认 prompt 渲染缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") {
		t.Error("模板变量未替换干净")
	}
	// 输出约束必须在默认模板里:只出正文,禁开场白/标签。
	for _, want := range []string{"Output ONLY the tweet text", "Banned openings", "no closing remark"} {
		if !strings.Contains(out, want) {
			t.Errorf("默认 prompt 缺输出约束 %q", want)
		}
	}
}

// 模型被朗读指令带偏时爱先来一句「Alright. I'm reading my latest post. Here we go.」,
// 再换行贴正文——cleanLLMText 要把这段开场白砍掉,只留推文。
func TestCleanLLMTextStripsReadingPreamble(t *testing.T) {
	in := "Alright. I'm reading my latest post. Here we go.\n\n\"Rockets are basically cars that decided gravity was optional.\""
	got := cleanLLMText(in)
	if got != "Rockets are basically cars that decided gravity was optional." {
		t.Errorf("开场白未清干净:\n got %q\nwant %q", got, "Rockets are basically cars that decided gravity was optional.")
	}
	for _, banned := range []string{"reading my latest post", "Here we go", "Alright"} {
		if strings.Contains(got, banned) {
			t.Errorf("输出仍含开场白片段 %q: %q", banned, got)
		}
	}
}

// 标签前缀 + 引号包裹的清洗（原有行为不能回归）。
func TestCleanLLMTextStripsLabelAndQuotes(t *testing.T) {
	for in, want := range map[string]string{
		`Post: "To Mars, obviously."`:            "To Mars, obviously.",
		"Here is the post:\nTo Mars, obviously.": "To Mars, obviously.",
	} {
		if got := cleanLLMText(in); got != want {
			t.Errorf("cleanLLMText(%q) = %q, want %q", in, got, want)
		}
	}
}

// 多行正文只要首行没命中「朗读宣告语」就一个字都不能动（避免误伤正文）。
func TestCleanLLMTextKeepsLegitMultiline(t *testing.T) {
	in := "Rockets are cars that ignored gravity.\n\nMars window opens in 26 months."
	if got := cleanLLMText(in); got != in {
		t.Errorf("正常多行正文被误改:\n got %q\nwant %q", got, in)
	}
}

// 只有一行且命中宣告语时没有正文可留,不能把内容清空。
func TestCleanLLMTextKeepsSingleLine(t *testing.T) {
	in := "I'm reading my latest post."
	if got := cleanLLMText(in); got != in {
		t.Errorf("单行内容不该被清空: got %q, want %q", got, in)
	}
}
