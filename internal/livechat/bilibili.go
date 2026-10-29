package livechat

import "livechat/pkg/provider/bilibili"

// ParseBiliRoomID 从房间号或 live.bilibili.com URL 解析房间号。
func ParseBiliRoomID(s string) int { return bilibili.ParseRoomID(s) }

func biliCookieOK(ck string) error { return bilibili.CookieOK(ck) }
