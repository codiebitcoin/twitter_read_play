package api

import (
	"net/http"

	"twitter_read_play/internal/store"
	"twitter_read_play/internal/watch"
)

// parody 模式接口：LLM 按 prompt 生成仿写推文，独立列表（source=parody），
// 与真实推文（broadcast/random 模式）完全分开；生成后走统一播报队列。

// roleParodyList GET /api/roles/{id}/parody：仿写推文列表（新→旧）。
func (s *Server) roleParodyList(w http.ResponseWriter, r *http.Request) {
	rows := s.Watches.ParodyTweets(r.PathValue("id"), 200)
	if rows == nil {
		rows = []store.TweetRow{} // 前端直接当数组用，别返回 null
	}
	s.json(w, map[string]any{"items": rows})
}

// roleParodyGenerate POST /api/roles/{id}/parody/generate：生成一条仿写推文（同步，等 LLM）。
// 生成成功后按 ?broadcast=1 决定是否立刻入队播报。
func (s *Server) roleParodyGenerate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tw, err := s.Watches.GenerateParody(id)
	if err != nil {
		s.err(w, 500, err)
		return
	}
	out := map[string]any{"ok": true, "tweet": tw}
	if r.URL.Query().Get("broadcast") == "1" {
		items, err := s.Watches.EnqueueBroadcast(id, tw.ID, watch.BroadcastOpts{}, "parody")
		if err != nil {
			s.err(w, 500, err)
			return
		}
		out["queued"] = len(items)
		out["items"] = items
	}
	s.json(w, out)
}

// roleParodyDelete DELETE /api/roles/{id}/parody/{tid}：删掉一条仿写推文。
func (s *Server) roleParodyDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Watches.DeleteParodyTweet(r.PathValue("id"), r.PathValue("tid")); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}
