// Package bilibili 用社区 SDK blivedm-go 拉 B 站公开直播弹幕。
// B 站没有对第三方开放的官方弹幕 pull API；公开房间走 WebSocket。
// Cookie（SESSDATA + bili_jct）可选：不填也能收到弹幕（昵称可能被打码）；填了更稳、信息更全。
package bilibili

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	biliclient "github.com/Akegarasu/blivedm-go/client"
	bilimsg "github.com/Akegarasu/blivedm-go/message"

	"livechat/pkg/provider"
)

type Options struct {
	// Cookie 可选。填了须含 SESSDATA 与 bili_jct；留空则游客连接。
	Cookie string
}

type Provider struct{ opts Options }

func New(opts Options) *Provider { return &Provider{opts: opts} }

func (p *Provider) Name() string { return "bilibili_blivedm" }

// CookieOK 检查 Cookie 是否含连接弹幕所需的两枚关键字段。
func CookieOK(ck string) error {
	ck = strings.TrimSpace(ck)
	if ck == "" {
		return fmt.Errorf("B 站弹幕需要登录 Cookie（SESSDATA + bili_jct）。游客 getDanmuInfo 会 -352：房间能连上但弹幕为空")
	}
	if !strings.Contains(ck, "SESSDATA") || !strings.Contains(ck, "bili_jct") {
		return fmt.Errorf("B 站 Cookie 缺少 SESSDATA 或 bili_jct（SDK 会报「账号未登录」）")
	}
	return nil
}

// ParseRoomID 从房间号或 live.bilibili.com / b23.tv URL 解析房间号。
func ParseRoomID(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	u, err := url.Parse(s)
	if err != nil {
		return 0
	}
	host := strings.ToLower(u.Host)
	if strings.HasPrefix(host, "live.bilibili.com") || host == "b23.tv" || strings.HasSuffix(host, ".b23.tv") {
		for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if n, err := strconv.Atoi(seg); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func (p *Provider) Stream(ctx context.Context, target string) (<-chan provider.ChatMessage, <-chan error, error) {
	if strings.TrimSpace(p.opts.Cookie) != "" {
		if err := CookieOK(p.opts.Cookie); err != nil {
			return nil, nil, err
		}
	}
	roomID := ParseRoomID(target)
	if roomID == 0 {
		return nil, nil, fmt.Errorf("bilibili: 无法从 %q 解析房间号", target)
	}
	c := biliclient.NewClient(roomID)
	c.SetCookie(p.opts.Cookie)

	msgs := make(chan provider.ChatMessage, 64)
	errs := make(chan error, 1)
	label := strconv.Itoa(roomID)
	var sendMu sync.Mutex
	closed := false

	// level：用户等级（B 站 UL）。弹幕带 Sender.UserLevel，SC/礼物/舰长事件不带 → 0。
	emit := func(kind, author, text, amount, raw string, level int) {
		text = strings.TrimSpace(text)
		if text == "" && amount == "" {
			return
		}
		if author == "" {
			author = "anonymous"
		}
		if kind == "" {
			kind = "text"
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return
		}
		select {
		case msgs <- provider.ChatMessage{
			Platform:  "bilibili",
			Author:    author,
			Text:      text,
			Kind:      kind,
			Amount:    amount,
			UserLevel: level,
			TS:        time.Now(),
			Raw:       raw,
		}:
		case <-ctx.Done():
		default:
			// 缓冲满时丢最旧路径：不阻塞，避免关连接时和 close 抢锁
		}
	}

	c.OnDanmaku(func(d *bilimsg.Danmaku) {
		if d == nil {
			return
		}
		user := "anonymous"
		level := 0
		if d.Sender != nil {
			if d.Sender.Uname != "" {
				user = d.Sender.Uname
			}
			level = int(d.Sender.UserLevel)
		}
		emit("text", user, d.Content, "", label+":"+d.Content, level)
	})
	c.OnSuperChat(func(sc *bilimsg.SuperChat) {
		if sc == nil {
			return
		}
		emit("superchat", sc.UserInfo.Uname, sc.Message, fmt.Sprintf("¥%d", sc.Price), fmt.Sprintf("sc:%d", sc.Id), 0)
	})
	c.OnGift(func(g *bilimsg.Gift) {
		if g == nil {
			return
		}
		amount := ""
		if g.Price > 0 {
			amount = fmt.Sprintf("¥%d", g.Price*g.Num)
		}
		emit("gift", g.Uname, fmt.Sprintf("%s × %d", g.GiftName, g.Num), amount, fmt.Sprintf("gift:%d:%d", g.GiftId, g.Num), 0)
	})
	c.OnGuardBuy(func(g *bilimsg.GuardBuy) {
		if g == nil {
			return
		}
		amount := ""
		if g.Price > 0 {
			amount = fmt.Sprintf("¥%d", g.Price*g.Num)
		}
		name := g.GiftName
		if name == "" {
			name = "guard"
		}
		emit("sub", g.Username, fmt.Sprintf("%s × %d", name, g.Num), amount, fmt.Sprintf("guard:%d", g.Uid), 0)
	})

	go func() {
		defer func() {
			c.Stop()
			sendMu.Lock()
			if !closed {
				closed = true
				close(msgs)
			}
			sendMu.Unlock()
		}()
		if err := c.Start(); err != nil {
			select {
			case errs <- fmt.Errorf("bilibili: 连接房间 %d 失败: %w", roomID, err):
			default:
			}
			return
		}
		log.Printf("[livechat/bilibili] 连接房间 %d（sdk=blivedm-go）", roomID)
		<-ctx.Done()
	}()
	return msgs, errs, nil
}
