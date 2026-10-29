package book

import (
	"log"
	"time"

	"twitter_read_play/internal/model"
)

// 朗读进度：按 (角色, 书) 记录断点与随机去重表。存 SQLite blobs「book_progress」，
// 进程启动时载入内存，改动后异步落盘（朗读一段一次，量级很小）。

// keyOf (角色, 书) → 存储键。
func keyOf(roleID, bookID string) string { return roleID + "|" + bookID }

// Progress 取某角色在某本书上的进度（不存在返回零值）。
func (l *Library) Progress(roleID, bookID string) model.BookPlayback {
	l.playMu.Lock()
	defer l.playMu.Unlock()
	return l.getProgressLocked(roleID, bookID)
}

func (l *Library) getProgressLocked(roleID, bookID string) model.BookPlayback {
	if pb, ok := l.plays[keyOf(roleID, bookID)]; ok {
		return copyProgress(pb)
	}
	return model.BookPlayback{BookID: bookID}
}

// UpdateProgress 修改进度并落盘（fn 返回 error 时不保存）。返回保存后的副本。
func (l *Library) UpdateProgress(roleID, bookID string, fn func(*model.BookPlayback)) model.BookPlayback {
	l.playMu.Lock()
	defer l.playMu.Unlock()
	pb := l.getProgressLocked(roleID, bookID)
	fn(&pb)
	if pb.Visited == nil {
		pb.Visited = map[string]bool{}
	}
	pb.BookID = bookID
	pb.UpdatedAt = time.Now()
	l.plays[keyOf(roleID, bookID)] = copyProgress(pb)
	l.saveProgressLocked()
	return pb
}

// MarkPlayed 标记一段已播完（随机去重 + 断点推进）。
// MarkPlayed 标记「章 chapter 的第 seg 段」已播（写进随机去重表）。
// 断点不在这里推进（由播放流程的 Stage 回调按进度设置），避免「随机播一段」把整本断点跳走；
// 同章内随机播过较早的段时，断点只往前不往后。
func (l *Library) MarkPlayed(roleID, bookID string, chapter, seg int, nParas int) model.BookPlayback {
	return l.UpdateProgress(roleID, bookID, func(pb *model.BookPlayback) {
		if pb.Visited == nil {
			pb.Visited = map[string]bool{}
		}
		pb.Visited[visitKey(chapter, seg)] = true
		if pb.Chapter < chapter || (pb.Chapter == chapter && pb.Segment < seg+1) {
			pb.Chapter, pb.Segment = chapter, seg+1
		}
		if nParas > 0 && pb.Chapter == chapter && pb.Segment >= nParas {
			// 本章最后一段播完：推进到下一章开头（章末不留空段）
			pb.Chapter, pb.Segment = chapter+1, 0
		}
		pb.LastPlayed = time.Now()
	})
}

// ResetVisited 清空随机去重表（「随机」播完一整轮后从头再来）。
func (l *Library) ResetVisited(roleID, bookID string) {
	l.UpdateProgress(roleID, bookID, func(pb *model.BookPlayback) { pb.Visited = map[string]bool{} })
}

// SetPosition 直接设置断点（前端点章 / 点段）。
func (l *Library) SetPosition(roleID, bookID string, chapter, seg int) {
	l.UpdateProgress(roleID, bookID, func(pb *model.BookPlayback) { pb.Chapter, pb.Segment = chapter, seg })
}

// ResetProgress 从头开始。
func (l *Library) ResetProgress(roleID, bookID string) {
	l.UpdateProgress(roleID, bookID, func(pb *model.BookPlayback) {
		pb.Chapter, pb.Segment, pb.Queue, pb.Visited = 0, 0, nil, map[string]bool{}
	})
}

// saveProgressLocked 落盘（调用方持 playMu）。内容只有几十字节~几 KB，同步写也不慢；
// 写失败只记日志（进度丢了不影响功能）。
func (l *Library) saveProgressLocked() {
	if err := l.st.Save("book_progress", l.plays); err != nil {
		log.Printf("[book] save progress: %v", err)
	}
}

// LoadProgress 启动时载入。
func (l *Library) LoadProgress() {
	l.playMu.Lock()
	defer l.playMu.Unlock()
	var m map[string]model.BookPlayback
	if err := l.st.Load("book_progress", &m); err != nil || m == nil {
		return
	}
	for k, v := range m {
		l.plays[k] = copyProgress(v)
	}
}

// ForgetRole 角色删除时清掉它的进度。
func (l *Library) ForgetRole(roleID string) {
	l.playMu.Lock()
	defer l.playMu.Unlock()
	changed := false
	for k := range l.plays {
		if len(k) > len(roleID) && k[:len(roleID)+1] == roleID+"|" {
			delete(l.plays, k)
			changed = true
		}
	}
	if changed {
		l.saveProgressLocked()
	}
}

// copyProgress 深拷贝（Visited/Queue 是引用类型，避免调用方误改内存态）。
func copyProgress(pb model.BookPlayback) model.BookPlayback {
	if pb.Visited != nil {
		m := make(map[string]bool, len(pb.Visited))
		for k, v := range pb.Visited {
			m[k] = v
		}
		pb.Visited = m
	}
	if pb.Queue != nil {
		pb.Queue = append([]int(nil), pb.Queue...)
	}
	return pb
}
