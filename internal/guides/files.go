package guides

// Files 是打进二进制的教程清单：kind → html 基名。
// 正文在语言变体 html/<base>.<lang>.html（如 google.en.html / google.zh.html）；
// Serve 按请求语言挑变体。
//
// kind 对齐直播页：google / twitch 是顶部 OAuth 卡；
// youtube_api / twitch_eventsub / chat_downloader / bilibili_blivedm 是弹幕源 kind（同一套教程可复用）。
// twitch_irc 已移除：IRC 6697 被常见代理节点黑洞，Twitch 改走 twitch_browser 页面轮询。
var Files = map[string]string{
	"google":           "google",
	"youtube_api":      "google",
	"twitch":           "twitch",
	"twitch_eventsub":  "twitch",
	"chat_downloader":  "chat_downloader",
	"youtube_noauth":   "youtube_noauth",
	"bilibili":         "bilibili",
	"bilibili_blivedm": "bilibili",
}

// Langs 已打包的语言，优先顺序。英语是默认/兜底；中文镜像实操记录。
// 加语言 = 丢 html/<kind>.<lang>.html 并在这里登记。
var Langs = []string{"en", "zh"}
