package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"twitter_read_play/internal/watch"
)

// ---- ChatRoom 多角色对话接播 ----
// play/stop：把页面编辑的对话逐行入队播报队列（组内跨机器严格串行），停止=丢弃该组待播。
// scripts：对话脚本持久化（页面可编辑），存 store blob「chatroom_scripts」。

// chatScript 一份对话脚本。
type chatScript struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Lines     []watch.ChatLine `json:"lines"`
	UpdatedAt time.Time        `json:"updated_at"`
}

var chatScriptsMu sync.Mutex // 脚本文件读改写串行化

// loadChatScripts 读全部脚本（无文件/解析失败都返回空列表）。
func (s *Server) loadChatScripts() []chatScript {
	var out []chatScript
	_ = s.Store.Load("chatroom_scripts", &out)
	return out
}

// saveChatScripts 原子写回全部脚本。
func (s *Server) saveChatScripts(list []chatScript) error {
	return s.Store.Save("chatroom_scripts", list)
}

// chatRoomPlay POST /api/chatroom/play：body { lines: [{role_id, text}], interval_seconds, repeats, forever }。
// repeats 缺省 = 播一轮；forever=true = 一直重复（直到 /stop）。
func (s *Server) chatRoomPlay(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines           []watch.ChatLine `json:"lines"`
		IntervalSeconds float64          `json:"interval_seconds"`
		Repeats         int              `json:"repeats"`
		Forever         bool             `json:"forever"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	opts := watch.ChatRoomOpts{
		Interval: time.Duration(in.IntervalSeconds * float64(time.Second)),
		Repeats:  in.Repeats,
	}
	if in.Forever {
		opts.Repeats = watch.ChatRoomRepeatForever
	}
	groupID, items, err := s.Watches.EnqueueChatRoomWithOpts(in.Lines, opts)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "group_id": groupID, "queued": len(items), "items": items})
}

// chatRoomStop POST /api/chatroom/stop?id={group_id}：停止续排并丢弃该组待播条目（正在播的不动）。
func (s *Server) chatRoomStop(w http.ResponseWriter, r *http.Request) {
	n := s.Watches.StopChatRoom(r.URL.Query().Get("id"))
	s.json(w, map[string]any{"ok": true, "cleared": n})
}

// chatScriptList GET /api/chatroom/scripts：脚本列表。
func (s *Server) chatScriptList(w http.ResponseWriter, r *http.Request) {
	chatScriptsMu.Lock()
	defer chatScriptsMu.Unlock()
	list := s.loadChatScripts()
	if list == nil {
		list = []chatScript{}
	}
	s.json(w, map[string]any{"scripts": list})
}

// chatScriptSave POST /api/chatroom/scripts：保存/另存（body 带 id=更新，不带=新建）。
func (s *Server) chatScriptSave(w http.ResponseWriter, r *http.Request) {
	var in chatScript
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, 400, err)
		return
	}
	chatScriptsMu.Lock()
	defer chatScriptsMu.Unlock()
	list := s.loadChatScripts()
	if in.ID == "" {
		in.ID = fmt.Sprintf("cs-%d", time.Now().UnixNano())
	}
	found := false
	for i := range list {
		if list[i].ID == in.ID {
			list[i].Name, list[i].Lines, list[i].UpdatedAt = in.Name, in.Lines, time.Now()
			found = true
			in = list[i]
			break
		}
	}
	if !found {
		in.UpdatedAt = time.Now()
		list = append(list, in)
	}
	if err := s.saveChatScripts(list); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "script": in})
}

// chatScriptDelete DELETE /api/chatroom/scripts/{id}。
func (s *Server) chatScriptDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	chatScriptsMu.Lock()
	defer chatScriptsMu.Unlock()
	list := s.loadChatScripts()
	out := list[:0]
	removed := false
	for _, sc := range list {
		if sc.ID == id {
			removed = true
			continue
		}
		out = append(out, sc)
	}
	if removed {
		if err := s.saveChatScripts(out); err != nil {
			s.err(w, 500, err)
			return
		}
	}
	s.json(w, map[string]any{"ok": true, "removed": removed})
}
