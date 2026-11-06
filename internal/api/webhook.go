// socialdata.tools / social_read Monitoring API webhook 接收端。
//
// 反向通知替代轮询：POST /api/webhook/socialdata（免登录，公网可达）。
// 每次推送带 X-Timestamp / X-Event-Id / X-Signature（HMAC-SHA256，v1= 前缀），
// signing string = "{X-Event-Id}.{X-Timestamp}.{raw body}"，secret 在设置页配置
// （socialdata.tools dashboard 同步设置；未配置 secret 时拒绝一切推送——M1）。
// 事件：new_tweet（socialdata，Twitter 形状）/ new_post（social_read 统一帖子对象）。
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/twitter"
	"twitter_read_play/internal/watch"
)

const webhookToleranceSec = 300

// webhookSecret 取设置页配置的签名密钥。
func (s *Server) webhookSecret() string {
	v, _ := s.Store.GetSetting("socialdata_webhook_secret")
	return v
}

// effectiveWebhookBase 公网地址：设置覆盖 → watch.DefaultWebhookBase。
func (s *Server) effectiveWebhookBase() string {
	if v, _ := s.Store.GetSetting("socialdata_webhook_base"); v != "" {
		return v
	}
	return watch.DefaultWebhookBase
}

// warnNoSecretOnce 未配置 secret 的告警只打一次（该端点是公网免登录入口）。
var warnNoSecretOnce sync.Once

// verifySignature 校验 X-Signature。secret 未配置时直接拒绝（M1：该端点免登录
// 且公网可达，任何人可伪造推文让设备播报、伪造超大推文 ID 把 LastTweetID 推前，
// 之后真推文全被跳过）——先用登录口令在设置页配好 secret 再启用反向通知。
func (s *Server) verifySignature(r *http.Request, raw []byte) bool {
	secret := s.webhookSecret()
	if secret == "" {
		warnNoSecretOnce.Do(func() {
			log.Printf("[webhook] 未配置 socialdata_webhook_secret：/api/webhook/socialdata 拒绝一切推送（请在设置页配置 secret）")
		})
		return false
	}
	ts := r.Header.Get("X-Timestamp")
	eventID := r.Header.Get("X-Event-Id")
	sig := r.Header.Get("X-Signature")
	if ts == "" || eventID == "" || sig == "" {
		return false
	}
	sig = strings.TrimPrefix(sig, "v1=")
	// 防重放：时间戳窗口
	tsv, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || math.Abs(float64(time.Now().Unix()-tsv)) > webhookToleranceSec {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(eventID + "." + ts + "."))
	mac.Write(raw)
	expected := mac.Sum(nil)
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) != len(expected) {
		return false
	}
	return hmac.Equal(got, expected)
}

// socialdataWebhook 接收监控推送，两种载荷都支持：
//   - socialdata.tools：event=new_tweet，data 为 Twitter API 形状（id_str/full_text/user.screen_name）
//   - social_read：    event=new_post，data 为统一帖子对象，meta.platform = weibo/xiaohongshu/…
//
// 都按 monitored_username + 平台路由到角色并播报；非推文事件（关注/资料变更）直接确认。
func (s *Server) socialdataWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// 校验签名（防伪造/重放），失败直接 401
	if !s.verifySignature(r, raw) {
		w.WriteHeader(http.StatusUnauthorized)
		s.json(w, map[string]string{"error": "invalid signature"})
		return
	}
	// 先 200：官方不重试，慢处理等同故障；播报放到后台
	var ev struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
		Meta  struct {
			Platform          string `json:"platform"`
			MonitoredUsername string `json:"monitored_username"`
			MonitorID         string `json:"monitor_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		s.json(w, map[string]bool{"ok": true})
		return
	}
	switch ev.Event {
	case "new_tweet":
		// socialdata.tools 形状（保持原行为不动）
		var data struct {
			IDStr    string `json:"id_str"`
			FullText string `json:"full_text"`
			Text     string `json:"text"`
			User     struct {
				ScreenName string `json:"screen_name"`
			} `json:"user"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			s.json(w, map[string]bool{"ok": true})
			return
		}
		username := ev.Meta.MonitoredUsername
		if username == "" {
			username = data.User.ScreenName
		}
		text := data.FullText
		if text == "" {
			text = data.Text
		}
		tw := model.Tweet{ID: data.IDStr, User: strings.ToLower(username), Text: model.CleanText(text, 300)}
		hit := s.Watches.HandleTweetEvent("twitter", username, tw)
		s.json(w, map[string]bool{"ok": true, "matched": hit})
	case "new_post":
		// social_read 统一帖子对象（微博/小红书）
		tw, err := twitter.ParseSocialReadPost(ev.Data)
		if err != nil || tw.ID == "" {
			s.json(w, map[string]bool{"ok": true})
			return
		}
		username := ev.Meta.MonitoredUsername
		if username == "" {
			username = tw.User
		}
		platform := ev.Meta.Platform
		if platform == "" {
			platform = "twitter" // 没带平台按 twitter 处理（老推送兼容）
		}
		hit := s.Watches.HandleTweetEvent(platform, username, tw)
		s.json(w, map[string]bool{"ok": true, "matched": hit})
	default:
		s.json(w, map[string]bool{"ok": true}) // 非推文事件（关注/资料变更）直接确认
	}
}

// roleMonitorCreate POST /api/roles/{id}/monitor：创建 socialdata webhook 监控（模式 B，按用户）。
func (s *Server) roleMonitorCreate(w http.ResponseWriter, r *http.Request) {
	if err := s.Watches.CreateMonitor(r.PathValue("id")); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// roleMonitorDelete DELETE /api/roles/{id}/monitor：删除监控，回到轮询模式。
func (s *Server) roleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Watches.DeleteMonitor(r.PathValue("id")); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// searchMonitorGet GET /api/monitor/search：共享 search monitor 状态。
func (s *Server) searchMonitorGet(w http.ResponseWriter, r *http.Request) {
	s.json(w, s.Watches.SearchMonitorStatus())
}

// searchMonitorSync POST /api/monitor/search：按当前角色列表重建共享 search monitor（模式 C）。
func (s *Server) searchMonitorSync(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshFrequency int `json:"refresh_frequency"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if err := s.Watches.SyncSearchMonitor(in.RefreshFrequency); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, s.Watches.SearchMonitorStatus())
}

// searchMonitorDelete DELETE /api/monitor/search：删除共享监控，全部回到轮询。
func (s *Server) searchMonitorDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Watches.DeleteSearchMonitor(); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}
