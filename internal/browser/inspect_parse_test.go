package browser

import (
	"encoding/json"
	"testing"
)

func TestParseXUser(t *testing.T) {
	raw := `{"data":{"user":{"result":{"rest_id":"44196397","legacy":{
	  "screen_name":"elonmusk","name":"Elon Musk",
	  "profile_image_url_https":"https://img/e.jpg",
	  "description":"bio","followers_count":123,"friends_count":4,
	  "statuses_count":56,"verified":true
	}}}}}`
	p, ok := ParseXUser([]Captured{{JSON: json.RawMessage(raw)}})
	if !ok {
		t.Fatal("want parsed")
	}
	if p.ID != "44196397" || p.Handle != "@elonmusk" || p.DisplayName != "Elon Musk" {
		t.Fatalf("unexpected: %+v", p)
	}
	if p.Followers != 123 || p.Following != 4 || p.Posts != 56 || !p.Verified {
		t.Fatalf("unexpected counts: %+v", p)
	}
	if p.AvatarURL != "https://img/e.jpg" || p.Bio != "bio" {
		t.Fatalf("unexpected profile: %+v", p)
	}
}

// 远程浏览器 capture 拿不到加载期请求时，从渲染后的主页 DOM 读取资料。
func TestParseXUserDOM(t *testing.T) {
	raw := `{"href":"https://x.com/GenshinImpact","handle":"GenshinImpact","name":"Genshin Impact",
	  "bio":"In the world of Teyvat","avatar":"https://img/a.jpg","verified":true,
	  "followers":"563.8万 关注者","following":"18 正在关注",
	  "body":"主页 Genshin Impact7,636 帖子 查看新帖子"}`

	p, ok := ParseXUserDOM(raw, "elonmusk") // 句柄不匹配
	if ok {
		t.Fatalf("句柄不匹配时应拒绝: %+v", p)
	}
	p, ok = ParseXUserDOM(raw, "GenshinImpact")
	if !ok {
		t.Fatal("want parsed")
	}
	if p.Handle != "@GenshinImpact" || p.DisplayName != "Genshin Impact" || p.Bio != "In the world of Teyvat" {
		t.Fatalf("unexpected: %+v", p)
	}
	if p.Followers != 5638000 || p.Following != 18 || p.Posts != 7636 || !p.Verified {
		t.Fatalf("unexpected counts: %+v", p)
	}
	if !p.Partial {
		t.Fatal("DOM 兜底应标记 Partial")
	}

	// 页面停在别的地址（导航静默失败）时不能把那一页当成目标
	offPage := `{"href":"https://x.com/home","handle":"GenshinImpact","name":"Genshin Impact"}`
	if _, ok := ParseXUserDOM(offPage, "GenshinImpact"); ok {
		t.Fatal("页面不在目标主页时应拒绝")
	}

	// 英文界面：1.2M / 12K
	en := `{"href":"https://x.com/someone","handle":"someone","name":"Someone",
	  "followers":"1.2M Followers","following":"345 Following","body":"Someone12K posts"}`
	pe, ok := ParseXUserDOM(en, "someone")
	if !ok {
		t.Fatal("want parsed (en)")
	}
	if pe.Followers != 1200000 || pe.Following != 345 || pe.Posts != 12000 {
		t.Fatalf("unexpected en counts: %+v", pe)
	}
}

func TestParseWeiboUser(t *testing.T) {
	raw := `{"data":{"userInfo":{
	  "id":123,"screen_name":"测试用户","profile_image_url":"https://img/w.jpg",
	  "description":"简介","followers_count":"1.2万","friends_count":"34",
	  "statuses_count":56,"verified":true,"verified_reason":"认证"
	}}}`
	p, ok := ParseWeiboUser([]Captured{{JSON: json.RawMessage(raw)}})
	if !ok {
		t.Fatal("want parsed")
	}
	if p.ID != "123" || p.Handle != "测试用户" || p.Followers != 12000 || p.Following != 34 || p.Posts != 56 {
		t.Fatalf("unexpected: %+v", p)
	}
	if !p.Verified || p.VerifiedNote != "认证" {
		t.Fatalf("verified: %+v", p)
	}
}

func TestParseXHSUser(t *testing.T) {
	raw := `{"data":{"basic_info":{
	  "nickname":"小红","images":"https://img/x.jpg","desc":"简介",
	  "fans":"2.3万","follows":10,"note_num":12,"red_id":"abc","user_id":"u1"
	}}}`
	p, ok := ParseXHSUser([]Captured{{JSON: json.RawMessage(raw)}})
	if !ok {
		t.Fatal("want parsed")
	}
	if p.ID != "u1" || p.Handle != "小红" || p.Followers != 23000 || p.Posts != 12 {
		t.Fatalf("unexpected: %+v", p)
	}
}

func TestParseXHSPageState(t *testing.T) {
	p, ok := ParseXHSPageState(`{"basicInfo":{"nickname":"小红","images":"https://img/x.jpg","fans":"2.3万"}}`)
	if !ok {
		t.Fatal("want parsed")
	}
	if p.Handle != "小红" || p.Followers != 23000 {
		t.Fatalf("unexpected: %+v", p)
	}
}
