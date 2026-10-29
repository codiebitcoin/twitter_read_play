package api

import (
	"encoding/json"
	"net/http"

	"twitter_read_play/internal/watch"
)

// 播报队列接口：所有播报（手动/随机/自动）统一入队，按机器分组串行消费。
// 前端全局弹窗轮询 GET，展示每台机器还有几条在排、哪条在播、哪条失败。

// broadcastQueueGet GET /api/broadcast/queue：队列快照（入队顺序，前端自行按机器分组）。
func (s *Server) broadcastQueueGet(w http.ResponseWriter, r *http.Request) {
	items := s.Watches.BroadcastQueue()
	if items == nil {
		items = []watch.QueueItem{}
	}
	s.json(w, map[string]any{"items": items})
}

// broadcastQueueClear DELETE /api/broadcast/queue[?machine_id=X][&role_id=Y]：丢弃待播条目
// （已在播的那条不动）。machine_id / role_id 都不传 = 清空所有机器的待播；
// role_id 用于角色页「清空本角色待播」（Stream 弹幕刷屏时一键清）。
func (s *Server) broadcastQueueClear(w http.ResponseWriter, r *http.Request) {
	n := 0
	if rid := r.URL.Query().Get("role_id"); rid != "" {
		n = s.Watches.ClearBroadcastQueueByRole(rid)
	} else {
		n = s.Watches.ClearBroadcastQueue(r.URL.Query().Get("machine_id"))
	}
	s.json(w, map[string]any{"ok": true, "cleared": n})
}

// broadcastQueueDeleteItem DELETE /api/broadcast/queue/{id}：删掉一条待播条目
// （正在播/已完成的不给删，返回 removed=false）。
func (s *Server) broadcastQueueDeleteItem(w http.ResponseWriter, r *http.Request) {
	removed := s.Watches.RemoveBroadcastQueueItem(r.PathValue("id"))
	s.json(w, map[string]any{"ok": true, "removed": removed})
}

// messageQueueGet GET /api/messages/queue：消息队列 = 播报队列 + 设备操作队列
// （PATCH设置 / Check&fix / VRM上传 / voice_clone，见 watch.OpQueue）。
// 每条带 type（broadcast / op），前端 MessageQueue 面板按机器分组统一展示。
func (s *Server) messageQueueGet(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	add := func(rows any, typ string) {
		b, err := json.Marshal(rows)
		if err != nil {
			return
		}
		var out []map[string]any
		if json.Unmarshal(b, &out) != nil {
			return
		}
		for _, m := range out {
			m["type"] = typ
			items = append(items, m)
		}
	}
	add(s.Watches.BroadcastQueue(), "broadcast")
	add(s.Watches.OpQueue(), "op")
	s.json(w, map[string]any{"items": items})
}
