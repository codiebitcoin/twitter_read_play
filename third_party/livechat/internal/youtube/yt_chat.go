// Package youtube 集成自 https://github.com/abhinavxd/youtube-live-chat-downloader (MIT License)
// 在原库基础上修复：
//  1. ParseInitialData 非 200 状态时返回 nil error 的 bug
//  2. ioutil 已废弃 → io.ReadAll
//  3. strings.Trim(x, "ytcfg.set(") 按字符集裁剪的隐患 → 显式剥离前后缀
//
// 并新增：
//   - 可注入自定义 *http.Client（支持代理）
//   - 全链路 context.Context 取消/超时
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ReloadContinuation struct {
	ReloadContinuationData struct {
		Continuation string `json:"continuation"`
	} `json:"reloadContinuationData"`
}

type InnerTubeContext struct {
	Client struct {
		Hl               string `json:"hl"`
		Gl               string `json:"gl"`
		RemoteHost       string `json:"remoteHost"`
		DeviceMake       string `json:"deviceMake"`
		DeviceModel      string `json:"deviceModel"`
		VisitorData      string `json:"visitorData"`
		UserAgent        string `json:"userAgent"`
		ClientName       string `json:"clientName"`
		ClientVersion    string `json:"clientVersion"`
		OsName           string `json:"osName"`
		OsVersion        string `json:"osVersion"`
		OriginalUrl      string `json:"originalUrl"`
		Platform         string `json:"platform"`
		ClientFormFactor string `json:"clientFormFactor"`
		ConfigInfo       struct {
			AppInstallData string `json:"appInstallData"`
		} `json:"configInfo"`
	} `json:"client"`
}

type YtCfg struct {
	INNERTUBE_API_KEY string
	INNERTUBE_CONTEXT InnerTubeContext
	// [FIX] 真实页面此字段为数字，原库声明为 string 会导致解析失败
	INNERTUBE_CONTEXT_CLIENT_NAME any
	INNERTUBE_CLIENT_VERSION      string
	ID_TOKEN                      string
}

type Context struct {
	Context      InnerTubeContext `json:"context"`
	Continuation string           `json:"continuation"`
}

type ContinuationChat struct {
	TimedContinuationData struct {
		Continuation string `json:"continuation"`
		TimeoutMs    int    `json:"timeoutMs"`
	} `json:"timedContinuationData"`
	InvalidationContinuationData struct {
		Continuation string `json:"continuation"`
		TimeoutMs    int    `json:"timeoutMs"`
	} `json:"invalidationContinuationData"`
}

type Actions struct {
	AddChatItemAction struct {
		Item struct {
			LiveChatTextMessageRenderer struct {
				Message struct {
					Runs []Runs `json:"runs"`
				} `json:"message"`
				AuthorName struct {
					SimpleText string `json:"simpleText"`
				}
				TimestampUsec string `json:"timestampUsec"`
			} `json:"liveChatTextMessageRenderer"`
		} `json:"item"`
	} `json:"addChatItemAction"`
}

type Runs struct {
	Text  string `json:"text,omitempty"`
	Emoji struct {
		EmojiId       string `json:"emojiId"`
		IsCustomEmoji bool   `json:"isCustomEmoji,omitempty"`
		Image         struct {
			Thumbnails []struct {
				Url string `json:"url,omitempty"`
			}
		}
	} `json:"emoji,omitempty"`
}

type ChatMessagesResponse struct {
	ContinuationContents struct {
		LiveChatContinuation struct {
			Actions       []Actions          `json:"actions"`
			Continuations []ContinuationChat `json:"continuations"`
		} `json:"liveChatContinuation"`
	} `json:"continuationContents"`
}

type ChatMessage struct {
	AuthorName string
	Message    string
	Timestamp  time.Time
}

type InitialData struct {
	Contents struct {
		TwoColumnWatchNextResults struct {
			ConversationBar struct {
				LiveChatRenderer struct {
					Continuations []ReloadContinuation `json:"continuations"`
				} `json:"liveChatRenderer"`
			} `json:"conversationBar"`
		} `json:"twoColumnWatchNextResults"`
	} `json:"contents"`
}

var (
	LIVE_CHAT_URL = `https://www.youtube.com/youtubei/v1/live_chat/get_%s?key=%s`
	// Google 可能要求过 CAPTCHA 或（欧盟用户）CONSENT 同意页，
	// 可通过 AddCookies 注入 cookie 绕过。
	customCookies []*http.Cookie
	// httpClient 可通过 SetClient 注入（默认走环境变量代理）。
	httpClient = &http.Client{}

	ErrLiveStreamOver error = errors.New("live stream over")
	ErrStreamNotLive  error = errors.New("stream not live")
)

const (
	API_TYPE           = "live_chat"
	YT_CFG_REGEX       = `ytcfg\.set\s*\(\s*({.+?})\s*\)\s*;`
	INITIAL_DATA_REGEX = `(?:window\s*\[\s*["\']ytInitialData["\']\s*\]|ytInitialData)\s*=\s*({.+?})\s*;\s*(?:var\s+meta|</script|\n)`
)

// SetClient 注入自定义 HTTP 客户端（例如配置代理）。
func SetClient(c *http.Client) { httpClient = c }

// AddCookies 注入额外 cookie（可选，用于绕过 CONSENT/CAPTCHA）。
func AddCookies(cookies []*http.Cookie) { customCookies = cookies }

func regexSearch(regex string, str string) []string {
	r, _ := regexp.Compile(regex)
	var out []string
	// 返回第 1 个捕获组的内容，而不是整个匹配
	for _, m := range r.FindAllStringSubmatch(str, -1) {
		out = append(out, m[1])
	}
	return out
}

func parseMicroSeconds(timeStampStr string) time.Time {
	tm, _ := strconv.ParseInt(timeStampStr, 10, 64)
	tm = tm / 1000
	return time.Unix(tm/1000, (tm%1000)*int64(time.Millisecond))
}

func fetchChatMessages(ctx context.Context, initialContinuationInfo string, ytCfg YtCfg) ([]ChatMessage, string, int, error) {
	apiKey := ytCfg.INNERTUBE_API_KEY
	continuationUrl := fmt.Sprintf(LIVE_CHAT_URL, API_TYPE, apiKey)
	innertubeContext := ytCfg.INNERTUBE_CONTEXT

	context := Context{innertubeContext, initialContinuationInfo}
	b, err := json.Marshal(context)
	if err != nil {
		return nil, "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, continuationUrl, bytes.NewReader(b))
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", 0, fmt.Errorf("fetch chat messages: status %d, body: %.200s", resp.StatusCode, string(body))
	}
	var chatMsgResp ChatMessagesResponse
	if err := json.Unmarshal(body, &chatMsgResp); err != nil {
		return nil, "", 0, fmt.Errorf("parse chat response: %w", err)
	}

	chatMessages := []ChatMessage{}
	for _, action := range chatMsgResp.ContinuationContents.LiveChatContinuation.Actions {
		renderer := action.AddChatItemAction.Item.LiveChatTextMessageRenderer
		runs := renderer.Message.Runs
		if len(runs) == 0 {
			continue
		}
		msg := ChatMessage{
			AuthorName: renderer.AuthorName.SimpleText,
			Timestamp:  parseMicroSeconds(renderer.TimestampUsec),
		}
		var sb strings.Builder
		for _, run := range runs {
			switch {
			case run.Text != "":
				sb.WriteString(run.Text)
			case run.Emoji.IsCustomEmoji:
				// 自定义 emoji 是小 PNG 图片，只能以图片 URL 形式保留
				n := len(run.Emoji.Image.Thumbnails)
				if n == 2 {
					sb.WriteString(" " + run.Emoji.Image.Thumbnails[1].Url + " ")
				} else if n == 1 {
					sb.WriteString(" " + run.Emoji.Image.Thumbnails[0].Url + " ")
				}
			default:
				sb.WriteString(run.Emoji.EmojiId)
			}
		}
		msg.Message = sb.String()
		chatMessages = append(chatMessages, msg)
	}

	// 无 continuation 返回 → 直播已结束
	conts := chatMsgResp.ContinuationContents.LiveChatContinuation.Continuations
	if len(conts) == 0 {
		return nil, "", 0, ErrLiveStreamOver
	}
	timeoutMs := 5
	c := conts[0]
	if c.TimedContinuationData.Continuation == "" {
		initialContinuationInfo = c.InvalidationContinuationData.Continuation
		timeoutMs = c.InvalidationContinuationData.TimeoutMs
	} else {
		initialContinuationInfo = c.TimedContinuationData.Continuation
		timeoutMs = c.TimedContinuationData.TimeoutMs
	}
	return chatMessages, initialContinuationInfo, timeoutMs, nil
}

// ParseInitialData 解析直播页 HTML，返回首个 continuation 令牌与 innertube 配置。
func ParseInitialData(ctx context.Context, videoUrl string) (string, YtCfg, error) {
	if _, err := url.Parse(videoUrl); err != nil {
		return "", YtCfg{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoUrl, nil)
	if err != nil {
		return "", YtCfg{}, err
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124 Safari/537.36")
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for _, cookie := range customCookies {
		req.AddCookie(cookie)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", YtCfg{}, err
	}
	defer resp.Body.Close()
	intArr, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", YtCfg{}, err
	}
	// [FIX] 原库此处非 200 时返回了 nil error
	if resp.StatusCode != http.StatusOK {
		return "", YtCfg{}, fmt.Errorf("fetch watch page: status %d", resp.StatusCode)
	}
	html := string(intArr)

	initialDataArr := regexSearch(INITIAL_DATA_REGEX, html)
	if len(initialDataArr) == 0 {
		return "", YtCfg{}, ErrStreamNotLive
	}
	initialData := strings.TrimSpace(initialDataArr[0])
	initialData = strings.TrimSuffix(initialData, ";")

	cfgMatches := regexSearch(YT_CFG_REGEX, html)
	if len(cfgMatches) == 0 {
		return "", YtCfg{}, errors.New("ytcfg not found in page")
	}
	ytCfg := strings.TrimSpace(cfgMatches[0])

	var _ytCfg YtCfg
	if err := json.Unmarshal([]byte(ytCfg), &_ytCfg); err != nil {
		return "", YtCfg{}, fmt.Errorf("parse ytcfg: %w", err)
	}

	var _initialData InitialData
	if err := json.Unmarshal([]byte(initialData), &_initialData); err != nil {
		return "", YtCfg{}, fmt.Errorf("parse ytInitialData: %w", err)
	}

	continuations := _initialData.Contents.TwoColumnWatchNextResults.ConversationBar.LiveChatRenderer.Continuations
	if len(continuations) == 0 || continuations[0].ReloadContinuationData.Continuation == "" {
		return "", YtCfg{}, ErrStreamNotLive
	}
	return continuations[0].ReloadContinuationData.Continuation, _ytCfg, nil
}

// FetchContinuationChat 用 continuation 拉一批弹幕；按服务端 timeoutMs 休眠后返回。
func FetchContinuationChat(ctx context.Context, continuation string, ytCfg YtCfg) ([]ChatMessage, string, error) {
	chatMessages, continuation, timeoutMs, err := fetchChatMessages(ctx, continuation, ytCfg)
	if err != nil {
		return nil, "", err
	}
	if timeoutMs > 0 {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		}
	} else {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return chatMessages, continuation, nil
}
