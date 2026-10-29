package livechat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/source"
)

var liveInspectClient = &http.Client{Timeout: 12 * time.Second}

// inspectBilibiliRoom 读 B 站公开房间信息：标题 / 在线人数 / 开播状态。
func inspectBilibiliRoom(ctx context.Context, ch source.Channel, t source.Target) (source.AccountProfile, error) {
	id := strings.TrimSpace(t.ID)
	if id == "" {
		if n := parseBiliRoomID(t.Raw); n > 0 {
			id = strconv.Itoa(n)
		}
	}
	roomID, err := strconv.Atoi(id)
	if err != nil || roomID <= 0 {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUnsupported, ChannelID: ch.ID, AdapterID: "bilibili_blivedm",
			Message: "无法从目标解析出 B 站房间号",
		}
	}
	u := fmt.Sprintf("https://api.live.bilibili.com/room/v1/Room/get_info?room_id=%d", roomID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return source.AccountProfile{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; trp/1.0)")
	resp, err := liveInspectClient.Do(req)
	if err != nil {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUpstream, ChannelID: ch.ID, AdapterID: "bilibili_blivedm",
			Message: "B 站房间信息请求失败: " + err.Error(), Err: err,
		}
	}
	defer resp.Body.Close()
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			RoomID      int    `json:"room_id"`
			UID         int    `json:"uid"`
			Title       string `json:"title"`
			LiveStatus  int    `json:"live_status"`
			Online      int    `json:"online"`
			UserCover   string `json:"user_cover"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUpstream, ChannelID: ch.ID, AdapterID: "bilibili_blivedm",
			Message: "B 站房间信息解析失败: " + err.Error(), Err: err,
		}
	}
	if out.Code != 0 || out.Data.RoomID == 0 {
		msg := out.Message
		if msg == "" {
			msg = fmt.Sprintf("B 站房间 %d 不存在", roomID)
		}
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectNotFound, ChannelID: ch.ID, AdapterID: "bilibili_blivedm",
			Message: msg,
		}
	}
	live := out.Data.LiveStatus == 1
	state := "未开播"
	if live {
		state = "直播中"
	}
	desc := cleanLiveHTML(out.Data.Description)
	bio := state
	if desc != "" {
		bio = state + " · " + desc
	}
	return source.AccountProfile{
		Platform:     source.PlatformBilibili,
		Kind:         source.KindLiveRoom,
		ID:           strconv.Itoa(out.Data.RoomID),
		Handle:       fmt.Sprintf("房间 %d", out.Data.RoomID),
		DisplayName:  firstNonEmptyString(out.Data.Title, fmt.Sprintf("房间 %d", out.Data.RoomID)),
		AvatarURL:    out.Data.UserCover,
		Bio:          bio,
		Followers:    int64(out.Data.Online),
		Verified:     live,
		CanonicalURL: fmt.Sprintf("https://live.bilibili.com/%d", out.Data.RoomID),
		Source:       "bilibili_blivedm",
		FetchedAt:    time.Now(),
	}, nil
}

var liveHTMLTagRe = regexp.MustCompile(`<[^>]+>`)

func cleanLiveHTML(s string) string {
	s = liveHTMLTagRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return strings.Join(strings.Fields(s), " ")
}

func parseBiliRoomID(raw string) int {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if n, err := strconv.Atoi(seg); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
