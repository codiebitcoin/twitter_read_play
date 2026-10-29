// Package store 基于 SQLite 的持久化（data/trp.db）。
// blobs 表存实体 JSON（machines/roles/watches/logs），kv 表存配置，sessions 表存登录会话。
package store

import (
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// maxCachedTweets 每个角色保留的推文缓存上限。
// Feed「加载更早的推文」最多只能翻到这里；调大即可保留更深的历史（每千条约几 MB）。
const maxCachedTweets = 5000

type Store struct {
	db  *sql.DB
	mu  sync.Mutex
	Dir string // 数据目录（data/），供资源文件落盘使用
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dir+"/trp.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS blobs (name TEXT PRIMARY KEY, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, user TEXT NOT NULL, expires INTEGER NOT NULL)`,
		// 配置教程索引：kind → 嵌入的 html 基名。正文在 internal/guides/html/<file>.<lang>.html
		`CREATE TABLE IF NOT EXISTS provider_guides (kind TEXT PRIMARY KEY, file TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
		// 推文缓存：列表秒开（读库），上游刷新/翻译均在后台
		`CREATE TABLE IF NOT EXISTS tweets (
			role_id TEXT NOT NULL, tid TEXT NOT NULL, user TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT '',
			fetched_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (role_id, tid))`,
		`CREATE INDEX IF NOT EXISTS idx_tweets_role ON tweets(role_id, tid)`,
		// 引用推文字段（老库 ALTER 兼容，失败=已存在）
		`ALTER TABLE tweets ADD COLUMN quoted_user TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tweets ADD COLUMN quoted_text TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tweets ADD COLUMN quoted_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tweets ADD COLUMN selected INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE tweets ADD COLUMN selected_parody INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tweets ADD COLUMN edited TEXT NOT NULL DEFAULT ''`,
		// 转发推文字段（RT 目标内容，RT 的 full_text 常被截断）
		`ALTER TABLE tweets ADD COLUMN rt_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tweets ADD COLUMN rt_user TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tweets ADD COLUMN rt_text TEXT NOT NULL DEFAULT ''`,
		// 媒体类型（photo/video/animated_gif）：纯媒体推文正文为空，靠它识别并兜底播报
		`ALTER TABLE tweets ADD COLUMN media TEXT NOT NULL DEFAULT ''`,
		// 点赞数（上游给就存，每次拉取覆盖；random 模式「点赞≥N」用）
		`ALTER TABLE tweets ADD COLUMN likes INTEGER NOT NULL DEFAULT 0`,
		// 社媒统一字段（social_read：微博/小红书）
		// images：图片 URL 列表的 JSON 数组文本；link：帖子原始链接
		`ALTER TABLE tweets ADD COLUMN images TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tweets ADD COLUMN link TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tweets ADD COLUMN comment_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tweets ADD COLUMN share_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tweets ADD COLUMN collect_count INTEGER NOT NULL DEFAULT 0`,
		// 上游平台（twitter/weibo/xiaohongshu/rss）：接口要能区分这条帖子来自哪个平台
		`ALTER TABLE tweets ADD COLUMN platform TEXT NOT NULL DEFAULT ''`,
		// 数据来源：real=真实推文（broadcast/random）| parody=AI 仿写（parody 模式，独立列表）
		`ALTER TABLE tweets ADD COLUMN source TEXT NOT NULL DEFAULT 'real'`,
		`CREATE TABLE IF NOT EXISTS tweet_trans (
			role_id TEXT NOT NULL, tid TEXT NOT NULL, lang TEXT NOT NULL, text TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (role_id, tid, lang))`,
	} {
		if _, err := db.Exec(q); err != nil {
			// ALTER 幂等：列已存在视为成功
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return nil, err
		}
	}
	return &Store{db: db, Dir: dir}, nil
}

// Load 读取 name 的 JSON 到 out（不存在时保持 out 零值）。
func (s *Store) Load(name string, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var data string
	err := s.db.QueryRow(`SELECT data FROM blobs WHERE name=?`, name).Scan(&data)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), out)
}

// Save 原子写入 name 的 JSON。
func (s *Store) Save(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`INSERT INTO blobs(name,data) VALUES(?,?)
		ON CONFLICT(name) DO UPDATE SET data=excluded.data`, name, string(b))
	return err
}

// GetSetting 读配置。
func (s *Store) GetSetting(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v string
	err := s.db.QueryRow(`SELECT v FROM kv WHERE k=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting 写配置。
func (s *Store) SetSetting(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO kv(k,v) VALUES(?,?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
	return err
}

// CreateSession 登录会话，有效期 7 天。
func (s *Store) CreateSession(token, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO sessions(token,user,expires) VALUES(?,?,?)`,
		token, user, time.Now().Add(7*24*time.Hour).Unix())
	return err
}

// ValidSession 校验会话是否有效（顺带清理过期）。
func (s *Store) ValidSession(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec(`DELETE FROM sessions WHERE expires < ?`, time.Now().Unix())
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token=? AND expires>=?`,
		token, time.Now().Unix()).Scan(&n)
	return err == nil && n > 0
}

// DropSession 注销。
func (s *Store) DropSession(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec(`DELETE FROM sessions WHERE token=?`, token)
}

// ---- 推文缓存 ----

// SaveTweets 累积保存推文缓存（fetch 后调用）。
// 不再全量 DELETE：历史推文保留在库里，「加载更多」翻旧推文才有数据；每角色上限 500 条。
func (s *Store) SaveTweets(roleID string, tweets []TweetRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	srcs := map[string]bool{}
	for _, tw := range tweets {
		src := tw.Source
		if src == "" {
			src = "real"
		}
		srcs[src] = true
		// images 落库为 JSON 数组文本（空列表存空串，读取时反序列化）
		images := ""
		if len(tw.Images) > 0 {
			if b, err := json.Marshal(tw.Images); err == nil {
				images = string(b)
			}
		}
		// UPSERT：只覆盖「上游拥有的」字段，保留用户数据（selected 勾选 / edited 编辑）。
		// 原实现用 INSERT OR REPLACE —— SQLite 那是「先删后插」，未列出的列会回到默认值，
		// 于是每轮上游刷新都会把用户取消的勾选重置成勾选、把编辑过的正文清空（实测复现）。
		// likes 属于上游字段：有就覆盖（点赞数会随时间涨）。
		// images/link 用 CASE 保留旧值：webhook 精简载荷不带配图时不能把已存的图清掉。
		if _, err := tx.Exec(`INSERT INTO tweets(role_id,tid,user,text,created_at,fetched_at,quoted_user,quoted_text,quoted_id,rt_id,rt_user,rt_text,media,likes,source,images,link,comment_count,share_count,collect_count,platform)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(role_id,tid) DO UPDATE SET
				user=excluded.user, text=excluded.text, created_at=excluded.created_at, fetched_at=excluded.fetched_at,
				quoted_user=excluded.quoted_user, quoted_text=excluded.quoted_text, quoted_id=excluded.quoted_id,
				rt_id=excluded.rt_id, rt_user=excluded.rt_user, rt_text=excluded.rt_text, media=excluded.media,
				likes=MAX(excluded.likes, tweets.likes),
				images=CASE WHEN excluded.images!='' THEN excluded.images ELSE tweets.images END,
				link=CASE WHEN excluded.link!='' THEN excluded.link ELSE tweets.link END,
				comment_count=MAX(excluded.comment_count, tweets.comment_count),
				share_count=MAX(excluded.share_count, tweets.share_count),
				collect_count=MAX(excluded.collect_count, tweets.collect_count),
				platform=CASE WHEN excluded.platform!='' THEN excluded.platform ELSE tweets.platform END`,
			roleID, tw.ID, tw.User, tw.Text, tw.CreatedAt, now, tw.QuotedUser, tw.QuotedText, tw.QuotedID, tw.RetweetID, tw.RetweetUser, tw.RetweetText, tw.Media, tw.Likes, src,
			images, tw.Link, tw.CommentCount, tw.ShareCount, tw.CollectCount, tw.Platform); err != nil {
			tx.Rollback()
			return err
		}
	}
	// 修剪：只留最新 maxCachedTweets 条（按 source 各算各的：parody 生成量小，不能被真实推文挤掉）。
	// 注意：这个上限必须大于「翻页能翻到的深度」，否则 loadOlder 付费拉回的历史推文
	// 会被这里立刻删掉（前端表现为「没有更早的推文了」，且每次都白花一次上游调用）。
	for src := range srcs {
		if _, err := tx.Exec(`DELETE FROM tweets WHERE role_id=? AND COALESCE(source,'real')=? AND tid NOT IN
			(SELECT tid FROM tweets WHERE role_id=? AND COALESCE(source,'real')=? ORDER BY tid DESC LIMIT ?)`,
			roleID, src, roleID, src, maxCachedTweets); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// tweetCols 推文缓存的标准列清单（所有读路径共用，避免各处 SELECT/Scan 漏列）。
const tweetCols = `tid, user, text, created_at,
	COALESCE(quoted_user,''), COALESCE(quoted_text,''), COALESCE(quoted_id,''), selected, COALESCE(edited,''), COALESCE(selected_parody,0),
	COALESCE(rt_id,''), COALESCE(rt_user,''), COALESCE(rt_text,''), COALESCE(media,''),
	likes, COALESCE(source,'real'),
	COALESCE(images,''), COALESCE(link,''), comment_count, share_count, collect_count, COALESCE(platform,'')`

// scanTweet 按 tweetCols 的顺序扫描一行。
func scanTweet(sc interface{ Scan(...any) error }) (TweetRow, error) {
	var tw TweetRow
	var images string
	err := sc.Scan(&tw.ID, &tw.User, &tw.Text, &tw.CreatedAt, &tw.QuotedUser, &tw.QuotedText, &tw.QuotedID, &tw.Selected, &tw.Edited,
		&tw.SelectedParody, &tw.RetweetID, &tw.RetweetUser, &tw.RetweetText, &tw.Media, &tw.Likes, &tw.Source,
		&images, &tw.Link, &tw.CommentCount, &tw.ShareCount, &tw.CollectCount, &tw.Platform)
	if images != "" {
		_ = json.Unmarshal([]byte(images), &tw.Images) // 脏数据忽略：图片只是展示用
	}
	return tw, err
}

// FilterTweets 按模式规则从缓存取推文（新→旧，最多 limit 条）。
// SQL 侧只做能用索引的过滤（来源 / 勾选 / 点赞）；关键词与日期区间交给调用方在 Go 侧判
// （created_at 格式因上游而异，字符串比较不可靠）。
func (s *Store) FilterTweets(roleID, source string, onlyChecked bool, minLikes, limit int) []TweetRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > maxCachedTweets {
		limit = maxCachedTweets
	}
	q := `SELECT ` + tweetCols + ` FROM tweets WHERE role_id=?`
	args := []any{roleID}
	if source != "" {
		q += ` AND source=?`
		args = append(args, source)
	}
	if onlyChecked {
		q += ` AND selected=1`
	}
	if minLikes > 0 {
		q += ` AND likes>=?`
		args = append(args, minLikes)
	}
	q += ` ORDER BY tid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		log.Println("[store] FilterTweets query error:", err)
		return nil
	}
	defer rows.Close()
	var out []TweetRow
	for rows.Next() {
		if tw, err := scanTweet(rows); err == nil {
			out = append(out, tw)
		}
	}
	return out
}

// ListTweets 读某角色缓存的推文（含翻译，lang 可空）。
// 游标分页：before 非空时返回 tid < before 的更早推文；limit<=0 或 >200 时按 50。
func (s *Store) ListTweets(roleID, lang, before, kw string, limit int) []TweetRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	kwCond := ""
	var extra []any
	if kw != "" {
		like := " LIKE '%'||?||'%'"
		kwCond = " AND (COALESCE(edited,'')" + like + " OR COALESCE(text,'')" + like +
			" OR COALESCE(quoted_text,'')" + like + " OR COALESCE(rt_text,'')" + like + ")"
		for i := 0; i < 4; i++ {
			extra = append(extra, kw)
		}
	}
	args := []any{roleID, before, before}
	args = append(args, extra...)
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+tweetCols+` FROM tweets
		WHERE role_id=? AND COALESCE(source,'real')='real' AND (?='' OR tid<?)`+kwCond+`
		ORDER BY tid DESC LIMIT ?`, args...)
	if err != nil {
		log.Println("[store] ListTweets query error:", err)
		return nil
	}
	defer rows.Close()
	var out []TweetRow
	for rows.Next() {
		if tw, err := scanTweet(rows); err == nil {
			out = append(out, tw)
		}
	}
	if lang == "" {
		return out
	}
	for i := range out {
		var tr string
		if err := s.db.QueryRow(`SELECT text FROM tweet_trans WHERE role_id=? AND tid=? AND lang=?`, roleID, out[i].ID, lang).Scan(&tr); err == nil {
			out[i].Translation = tr
		}
	}
	return out
}

// TweetByID 按 tid 取单条缓存推文（含中文翻译）。
// 手动播报「这条」用：上游最新一页（~20 条）里没有的旧推文，只要缓存过就能播。
func (s *Store) TweetByID(roleID, tid string) (TweetRow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tw, err := scanTweet(s.db.QueryRow(`SELECT `+tweetCols+` FROM tweets WHERE role_id=? AND tid=?`, roleID, tid))
	if err != nil {
		return TweetRow{}, false
	}
	var tr string
	if err := s.db.QueryRow(`SELECT text FROM tweet_trans WHERE role_id=? AND tid=? AND lang='zh'`, roleID, tid).Scan(&tr); err == nil {
		tw.Translation = tr
	}
	return tw, true
}

// OldestTweetID 缓存中最早的推文 id（判断翻页是否需要拉上游）。
func (s *Store) OldestTweetID(roleID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tid string
	// 只算真实推文：parody 生成行的 tid 是零填充的合成 id（0000001791987324559），
	// 会把 MIN(tid) 拉到极小值，导致 loadOlder 的 "库里已有更老内容" 守卫恒真 → 永远不翻历史。
	if err := s.db.QueryRow(`SELECT MIN(tid) FROM tweets WHERE role_id=? AND COALESCE(source,'real')='real'`, roleID).Scan(&tid); err != nil {
		return ""
	}
	return tid
}

// TweetCount 某角色缓存的推文条数（判断是否首拉）。
func (s *Store) TweetCount(roleID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tweets WHERE role_id=?`, roleID).Scan(&n)
	return n
}

// SaveTranslation 存翻译。
func (s *Store) SaveTranslation(roleID, tid, lang, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO tweet_trans(role_id,tid,lang,text) VALUES(?,?,?,?)
		ON CONFLICT(role_id,tid,lang) DO UPDATE SET text=excluded.text`, roleID, tid, lang, text)
	return err
}

// MissingTranslations 需要翻译的推文（缓存无对应语言译文）。
func (s *Store) MissingTranslations(roleID, lang string, limit int) []TweetRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT t.tid, t.text FROM tweets t
		WHERE t.role_id=? AND NOT EXISTS (SELECT 1 FROM tweet_trans x WHERE x.role_id=t.role_id AND x.tid=t.tid AND x.lang=?)
		ORDER BY t.tid DESC LIMIT ?`, roleID, lang, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TweetRow
	for rows.Next() {
		var tw TweetRow
		if err := rows.Scan(&tw.ID, &tw.Text); err == nil {
			out = append(out, tw)
		}
	}
	return out
}

// TweetRow 推文缓存行。
// DeleteRoleData 清掉某个角色的推文缓存与翻译（角色被删除时调用，避免库里留下
// 指向已不存在角色的孤儿行）。
func (s *Store) DeleteRoleData(roleID string) {
	if roleID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM tweets WHERE role_id=?`, roleID)
	_, _ = s.db.Exec(`DELETE FROM tweet_trans WHERE role_id=?`, roleID)
}

type TweetRow struct {
	Platform       string `json:"platform,omitempty"` // twitter / weibo / xiaohongshu（接口要能区分上游）
	ID             string `json:"id"`
	User           string `json:"user,omitempty"`
	Text           string `json:"text"`
	CreatedAt      string `json:"created_at,omitempty"`
	QuotedID       string `json:"quoted_id,omitempty"`
	QuotedUser     string `json:"quoted_user,omitempty"`
	QuotedText     string `json:"quoted_text,omitempty"`
	RetweetID      string `json:"retweet_id,omitempty"`
	RetweetUser    string `json:"retweet_user,omitempty"`
	RetweetText    string `json:"retweet_text,omitempty"`
	Selected       bool   `json:"selected"`
	SelectedParody bool   `json:"selected_parody"`
	Edited         string `json:"edited,omitempty"`
	Media          string `json:"media,omitempty"` // photo/video/animated_gif（纯媒体推文正文为空）
	// Likes 点赞数（上游给就存，每次拉取取较大值；拿不到就是 0）。
	Likes int `json:"likes,omitempty"`
	// ---- 社媒统一字段（social_read：微博/小红书）----
	Images       []string `json:"images,omitempty"`        // 配图 URL 列表（库内为 JSON 数组文本）
	Link         string   `json:"link,omitempty"`          // 帖子原始链接
	CommentCount int      `json:"comment_count,omitempty"` // 评论数
	ShareCount   int      `json:"share_count,omitempty"`   // 转发/分享数
	CollectCount int      `json:"collect_count,omitempty"` // 收藏数
	// Source 数据来源：real=真实推文 | parody=AI 仿写（parody 模式独立列表）。
	Source      string `json:"source,omitempty"`
	Translation string `json:"translation,omitempty"`
}

// DeleteTweet 删掉一条缓存推文（parody 模式删除生成的仿写推文用；翻译一并清）。
func (s *Store) DeleteTweet(roleID, tid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM tweets WHERE role_id=? AND tid=?`, roleID, tid); err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM tweet_trans WHERE role_id=? AND tid=?`, roleID, tid)
	return nil
}

// ListCheckedTweets 某池勾选中的推文（pool: random|parody），新→旧。
// 游标分页：before 非空时返回 tid < before 的更早推文；limit<=0 表示不分页（取全部，
// 兼容 Feed 顶部池区一次展示全量的旧行为）。
func (s *Store) ListCheckedTweets(roleID, lang, pool, before string, limit int) []TweetRow {
	col := "selected"
	if pool == "parody" {
		col = "COALESCE(selected_parody,0)"
	}
	q := `SELECT ` + tweetCols + ` FROM tweets
		WHERE role_id=? AND COALESCE(source,'real')='real' AND ` + col + `=1 AND (?='' OR tid<?)
		ORDER BY tid DESC`
	args := []any{roleID, before, before}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(q, args...)
	if err != nil {
		log.Println("[store] ListCheckedTweets query error:", err)
		return nil
	}
	defer rows.Close()
	var out []TweetRow
	for rows.Next() {
		if tw, err := scanTweet(rows); err == nil {
			out = append(out, tw)
		}
	}
	if lang == "" {
		return out
	}
	for i := range out {
		var tr string
		if err := s.db.QueryRow(`SELECT text FROM tweet_trans WHERE role_id=? AND tid=? AND lang=?`, roleID, out[i].ID, lang).Scan(&tr); err == nil {
			out[i].Translation = tr
		}
	}
	return out
}

// CountCheckedTweets 某池勾选中的推文总数（池列表分页时显示「已加载 x / 共 N 条」）。
func (s *Store) CountCheckedTweets(roleID, pool string) int {
	col := "selected"
	if pool == "parody" {
		col = "COALESCE(selected_parody,0)"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tweets
		WHERE role_id=? AND COALESCE(source,'real')='real' AND `+col+`=1`, roleID).Scan(&n); err != nil {
		log.Println("[store] CountCheckedTweets query error:", err)
		return 0
	}
	return n
}

// CountTweets 缓存里匹配 kw 的真实推文总数（kw 为空=全部；搜索结果分页显示进度）。
// 过滤条件与 ListTweets 保持一致，否则「已加载 x / 共 N」会与实际翻页结果对不上。
func (s *Store) CountTweets(roleID, kw string) int {
	args := []any{roleID}
	kwCond := ""
	if kw != "" {
		like := " LIKE '%'||?||'%'"
		kwCond = " AND (COALESCE(edited,'')" + like + " OR COALESCE(text,'')" + like +
			" OR COALESCE(quoted_text,'')" + like + " OR COALESCE(rt_text,'')" + like + ")"
		for i := 0; i < 4; i++ {
			args = append(args, kw)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tweets
		WHERE role_id=? AND COALESCE(source,'real')='real'`+kwCond, args...).Scan(&n); err != nil {
		log.Println("[store] CountTweets query error:", err)
		return 0
	}
	return n
}

// ParodyBackgroundTweets parody 背景池（selected_parody;新→旧,最多 limit 条）——与随机池数据隔离。
func (s *Store) ParodyBackgroundTweets(roleID, lang string, limit int) []TweetRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > maxCachedTweets {
		limit = maxCachedTweets
	}
	rows, err := s.db.Query(`SELECT `+tweetCols+` FROM tweets
		WHERE role_id=? AND COALESCE(selected_parody,0)=1 AND COALESCE(source,'real')='real' ORDER BY tid DESC LIMIT ?`, roleID, limit)
	if err != nil {
		log.Println("[store] ParodyBackgroundTweets query error:", err)
		return nil
	}
	defer rows.Close()
	var out []TweetRow
	for rows.Next() {
		if tw, err := scanTweet(rows); err == nil {
			out = append(out, tw)
		}
	}
	if lang == "" {
		return out
	}
	for i := range out {
		var tr string
		if err := s.db.QueryRow(`SELECT text FROM tweet_trans WHERE role_id=? AND tid=? AND lang=?`, roleID, out[i].ID, lang).Scan(&tr); err == nil {
			out[i].Translation = tr
		}
	}
	return out
}

// SetTweetSelected 勾选/取消勾选某条推文（参与随机播报）。
func (s *Store) SetTweetSelected(roleID, tid, pool string, selected bool) error {
	col := "selected"
	if pool == "parody" {
		col = "selected_parody"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sel := 0
	if selected {
		sel = 1
	}
	_, err := s.db.Exec(`UPDATE tweets SET `+col+`=? WHERE role_id=? AND tid=?`, sel, roleID, tid)
	return err
}

// SelectedTweetIDs 某角色勾选中的推文 id 集合。
func (s *Store) SelectedTweetIDs(roleID string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT tid FROM tweets WHERE role_id=? AND selected=1`, roleID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var tid string
		if rows.Scan(&tid) == nil {
			out[tid] = true
		}
	}
	return out
}

// SelectedTweets 某角色「已勾选」的推文池（新→旧，最多 limit 条）。
// 随机播报的候选池用它：勾选是用户的显式选择，不该只在上游最新一页（~20 条）里挑。
// lang 传空可跳过翻译查询（播报不需要翻译）。
func (s *Store) SelectedTweets(roleID, lang string, limit int) []TweetRow { // random 播报池
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > maxCachedTweets {
		limit = maxCachedTweets
	}
	rows, err := s.db.Query(`SELECT `+tweetCols+` FROM tweets
		WHERE role_id=? AND selected=1 AND COALESCE(source,'real')='real' ORDER BY tid DESC LIMIT ?`, roleID, limit)
	if err != nil {
		log.Println("[store] SelectedTweets query error:", err)
		return nil
	}
	defer rows.Close()
	var out []TweetRow
	for rows.Next() {
		if tw, err := scanTweet(rows); err == nil {
			out = append(out, tw)
		}
	}
	if lang == "" {
		return out
	}
	for i := range out {
		var tr string
		if err := s.db.QueryRow(`SELECT text FROM tweet_trans WHERE role_id=? AND tid=? AND lang=?`, roleID, out[i].ID, lang).Scan(&tr); err == nil {
			out[i].Translation = tr
		}
	}
	return out
}

// SaveTweetEdit 保存推文编辑文本（保留原文 text 不动；edited 空串=清除编辑）。
func (s *Store) SaveTweetEdit(roleID, tid, edited string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE tweets SET edited=? WHERE role_id=? AND tid=?`, edited, roleID, tid)
	return err
}

// UpsertGuide 登记某 kind 对应的教程 HTML 基名。API 读这张表找文件，加教程 = 加一行 + 丢 html。
func (s *Store) UpsertGuide(kind, file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO provider_guides(kind,file,updated_at) VALUES(?,?,?)
		ON CONFLICT(kind) DO UPDATE SET file=excluded.file, updated_at=excluded.updated_at`,
		kind, file, time.Now().Unix())
	return err
}

// GuideFile 返回 kind 登记的 HTML 基名（没有教程时 ok=false）。
func (s *Store) GuideFile(kind string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var file string
	err := s.db.QueryRow(`SELECT file FROM provider_guides WHERE kind=?`, kind).Scan(&file)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return file, true, nil
}
