package livechat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// ChatDownloader 借用 chat_downloader 库拉弹幕（可选来源，免认证的兜底方案）。
//
// 前置条件：本机 python3 装了 chat-downloader（pip install chat-downloader）。
// 实现方式：spawn 一个内联 python 脚本，把 get_chat(url) 的生成器逐条打印成
// JSON 行，Go 侧解析。相比拿 CLI 输出文件，stdout 行式协议最稳、天然支持流式。
// Channel 直接透传给 get_chat：YouTube 直播/回放/首播、Twitch 直播/VOD/剪辑都行。
type ChatDownloader struct {
	Cfg SourceConfig
}

func (c *ChatDownloader) Name() string { return KindChatDownloader }

const chatDLScript = `
import sys, json
from chat_downloader import ChatDownloader

url = sys.argv[1]
chat = ChatDownloader().get_chat(url)
for m in chat:
    try:
        sys.stdout.write(json.dumps(m, default=str) + "\n")
        sys.stdout.flush()
    except BrokenPipeError:
        sys.exit(0)
`

// Stream 阻塞拉取直到 ctx 取消。子进程退出（直播结束/库报错）即返回 error，
// 由 Manager 退避重启 —— 回放场景一次跑完返回 nil 之外的 error 也没关系，
// 反正重拉一次就是从头再读一遍（对回放反而幂等）。
func (c *ChatDownloader) Stream(ctx context.Context, on func(Message)) error {
	if base := strings.TrimSpace(c.Cfg.BaseURL); base != "" {
		return c.streamService(ctx, on, base)
	}
	py := strings.TrimSpace(c.Cfg.PythonPath)
	if py == "" {
		py = "python3"
	}
	args := []string{"-c", chatDLScript, strings.TrimSpace(c.Cfg.Channel)}
	for _, a := range strings.Fields(c.Cfg.ExtraArgs) {
		args = append(args, a)
	}
	cmd := exec.CommandContext(ctx, py, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf strings.Builder
	cmd.Stderr = &errWriter{b: &errBuf}
	log.Printf("[livechat/chatdl] 启动 %s（channel=%s）", py, c.Cfg.Channel)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("chat_downloader: 启动 %s 失败（pip install chat-downloader 装了吗？）: %w", py, err)
	}
	// ctx 取消时杀干净进程组（python 子进程不响应 context kill 时兜底）
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if m, ok := parseChatDLLine(sc.Bytes()); ok {
			m.Source = KindChatDownloader
			m.Channel = c.Cfg.Channel
			on(m)
		}
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		// 取尾部几行：python traceback 通常很长，开头就够定位
		if lines := strings.Split(msg, "\n"); len(lines) > 6 {
			msg = strings.Join(lines[len(lines)-6:], "\n")
		}
		return fmt.Errorf("chat_downloader: %s", msg)
	}
	return ctx.Err()
}

type errWriter struct{ b *strings.Builder }

func (w *errWriter) Write(p []byte) (int, error) {
	if w.b.Len() < 64*1024 { // 截断保护
		w.b.Write(p)
	}
	return len(p), nil
}

// serviceChatURL 把服务根地址和直播链接拼成请求。
// 地址里写了 {url} 就替换；否则追加查询参数 url=。
func serviceChatURL(base, channel string) (string, error) {
	base = strings.TrimSpace(base)
	if strings.Contains(base, "{url}") {
		return strings.ReplaceAll(base, "{url}", url.QueryEscape(channel)), nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("chat_downloader: 服务地址不是 http(s) URL")
	}
	q := u.Query()
	q.Set("url", channel)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// streamService 向第三方 HTTP 服务拉弹幕。约定：GET 后按行返回 chat-downloader 同款 JSON。
func (c *ChatDownloader) streamService(ctx context.Context, on func(Message), base string) error {
	return streamHTTPLines(ctx, base, c.Cfg.Channel, KindChatDownloader, on)
}

// httpLineStreamer 填了服务地址时，B 站（以及同类）走 HTTP 拉弹幕，不走官方长连接。
type httpLineStreamer struct {
	kind, channel, base string
}

func (h *httpLineStreamer) Name() string { return h.kind }

func (h *httpLineStreamer) Stream(ctx context.Context, on func(Message)) error {
	return streamHTTPLines(ctx, h.base, h.channel, h.kind, on)
}

func streamHTTPLines(ctx context.Context, base, channel, kind string, on func(Message)) error {
	endpoint, err := serviceChatURL(base, strings.TrimSpace(channel))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: 请求服务失败: %w", kind, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: 服务 HTTP %d: %s", kind, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	log.Printf("[livechat/%s] HTTP %s", kind, endpoint)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if m, ok := parseChatDLLine(sc.Bytes()); ok {
			m.Source = kind
			m.Channel = channel
			on(m)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("%s: %w", kind, err)
	}
	return ctx.Err()
}

// parseChatDLLine chat-downloader 的单条消息 JSON → Message。
// 已知字段：message（碎片数组或字符串）/ message_id、author.name、timestamp（unix 秒）、
// type（text_message / super_chat / paid_message / member ...）、money.text（打赏金额）。
func parseChatDLLine(line []byte) (Message, bool) {
	var it struct {
		Message   json.RawMessage `json:"message"`
		MessageID string          `json:"message_id"`
		Type      string          `json:"type"`
		Author    struct {
			Name string `json:"name"`
		} `json:"author"`
		Timestamp float64 `json:"timestamp"`
		Money     struct {
			Text string `json:"text"`
		} `json:"money"`
	}
	if err := json.Unmarshal(line, &it); err != nil {
		return Message{}, false
	}
	m := Message{
		ID:   it.MessageID,
		User: it.Author.Name,
		Text: chatDLText(it.Message),
		Kind: "text",
		At:   time.Now(),
	}
	if it.Timestamp > 0 {
		// chat-downloader 对部分站点给毫秒，>1e12 视为毫秒
		if it.Timestamp > 1e12 {
			m.At = time.UnixMilli(int64(it.Timestamp))
		} else {
			m.At = time.Unix(int64(it.Timestamp), 0)
		}
	}
	if it.Type != "" {
		switch {
		case strings.Contains(it.Type, "super") || strings.Contains(it.Type, "paid"):
			m.Kind = "superchat"
			m.Amount = it.Money.Text
		case strings.Contains(it.Type, "member") || strings.Contains(it.Type, "sponsor"):
			m.Kind = "sub"
		}
	}
	if m.Text == "" {
		return Message{}, false
	}
	if m.User == "" {
		m.User = "anonymous"
	}
	return m, true
}

// chatDLText message 字段：字符串直接用；碎片数组拼所有 text 子项。
func chatDLText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var frags []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &frags); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, f := range frags {
		sb.WriteString(f.Text)
	}
	return strings.TrimSpace(sb.String())
}
