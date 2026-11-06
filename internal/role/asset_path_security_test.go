package role

import (
	"testing"

	"twitter_read_play/internal/store"
)

// H3 回归：AssetPath 历史上只是 Dir + "/" + rel 裸拼接，icon 等字段直接取客户端
// JSON，PUT {"icon":"../app.db"} 后 GET /icon 即可读走存有全部密钥的 SQLite，
// 删除素材接口可删任意文件。清洗后穿越串必须解析失败（返回 ""）。
func TestAssetPathRejectsTraversal(t *testing.T) {
	cases := []struct {
		rel   string
		valid bool
	}{
		{"roles/r-1/model-1700000000.vrm", true},
		{"roles/musk/model.vrm", true},
		{"roles/musk/icon.png", true},
		{"roles/musk/voice-1700000000.mp3", true},
		{"", false},
		{"../app.db", false},
		{"roles/../app.db", false},
		{"roles/r-1/../../app.db", false},
		{"/etc/passwd", false},
		{"roles/r-1/sub/x.vrm", false},
		{"roles/r-1/", false},
		{"roles/r-1", false},
		{"roles/r%31/x.vrm", false},
		{`roles/r-1/..\.vrm`, false},
		{"roles/r-1/.", false},
		{"roles/r-1/..", false},
		{"roles/r-1/.hidden", true}, // 点开头的合法文件名（[\w.-]+ 允许）
	}
	for _, c := range cases {
		if got := assetRelValid(c.rel); got != c.valid {
			t.Errorf("assetRelValid(%q) = %v, want %v", c.rel, got, c.valid)
		}
	}
	// 穿越串的绝对路径不允许落在 data/ 内
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if p := (&Manager{st: st}).AssetPath("../app.db"); p != "" {
		t.Errorf("AssetPath traversal = %q, want empty", p)
	}
}

// 角色名下的归属校验：别的角色的资源路径也不能写进来。
func TestAssetOwned(t *testing.T) {
	if !AssetOwned("r-1", "roles/r-1/icon.png") {
		t.Error("own asset must be owned")
	}
	if AssetOwned("r-2", "roles/r-1/icon.png") {
		t.Error("another role's asset must not be owned")
	}
	if AssetOwned("", "roles/r-1/icon.png") {
		t.Error("new role (empty id) must not accept any asset path")
	}
}
