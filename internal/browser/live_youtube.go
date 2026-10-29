package browser

import (
	"encoding/json"
	"strings"
	"time"

	"twitter_read_play/internal/source"
)

// ParseYouTubeChat 从拦截到的 liveChat/get_live_chat 响应里抽出弹幕。
func ParseYouTubeChat(caps []Captured) []source.LiveMessage {
	out := make([]source.LiveMessage, 0)
	for _, c := range caps {
		if len(c.JSON) == 0 {
			continue
		}
		var root any
		if err := json.Unmarshal(c.JSON, &root); err != nil {
			continue
		}
		actions := getSlice(getPathMap(root, "continuationContents", "liveChatContinuation"), "actions")
		for _, raw := range actions {
			action := asMap(raw)
			if add := getMap(action, "addChatItemAction"); add != nil {
				appendYouTubeItem(getMap(add, "item"), &out)
			}
			if replay := getMap(action, "replayChatItemAction"); replay != nil {
				for _, rawInner := range getSlice(replay, "actions") {
					inner := getMap(asMap(rawInner), "addChatItemAction")
					appendYouTubeItem(getMap(inner, "item"), &out)
				}
			}
		}
	}
	return out
}

func appendYouTubeItem(item map[string]any, out *[]source.LiveMessage) {
	if item == nil {
		return
	}
	for _, key := range []string{
		"liveChatTextMessageRenderer",
		"liveChatPaidMessageRenderer",
		"liveChatPaidStickerRenderer",
		"liveChatMembershipItemRenderer",
		"liveChatSponsorshipsGiftPurchaseAnnouncementRenderer",
	} {
		r := getMap(item, key)
		if r == nil {
			continue
		}
		m := source.LiveMessage{
			ID:     getStr(r, "id"),
			User:   firstNonEmpty(getStr(getMap(r, "authorName"), "simpleText"), "anonymous"),
			Text:   youtubeRuns(getMap(r, "message")),
			Kind:   "text",
			Source: "youtube_browser",
		}
		if m.Text == "" {
			m.Text = youtubeRuns(getMap(r, "headerSubtext"))
		}
		switch key {
		case "liveChatPaidMessageRenderer", "liveChatPaidStickerRenderer":
			m.Kind = "superchat"
			m.Amount = getStr(getMap(r, "purchaseAmountText"), "simpleText")
		case "liveChatMembershipItemRenderer":
			m.Kind = "sub"
		case "liveChatSponsorshipsGiftPurchaseAnnouncementRenderer":
			m.Kind = "gift"
		}
		if usec := int64(floatValue(field(r, "timestampUsec"))); usec > 0 {
			m.At = time.UnixMicro(usec)
		} else {
			m.At = time.Now()
		}
		if strings.TrimSpace(m.Text) == "" && m.Kind == "text" {
			continue
		}
		*out = append(*out, m)
		return
	}
}

func youtubeRuns(message map[string]any) string {
	if message == nil {
		return ""
	}
	var sb strings.Builder
	for _, raw := range getSlice(message, "runs") {
		sb.WriteString(getStr(asMap(raw), "text"))
	}
	if sb.Len() == 0 {
		sb.WriteString(getStr(message, "simpleText"))
	}
	return strings.TrimSpace(sb.String())
}
