package browser

import "context"

// Driver 打开/操作 WebView。桌面实现用 mygo；Web 模式不需要本地 Driver。
type Driver interface {
	Available() bool
	Open(opts OpenOptions) (Window, error)
}

// OpenOptions 打开一个窗口/WebView。
type OpenOptions struct {
	Title         string
	URL           string
	PreloadScript string
	UserAgent     string
	Hidden        bool
	Width, Height int
	// StateKey 记住窗口位置/大小（MyGo 的 window-state.json）。
	StateKey string
}

// Window 一个页面窗口的最小操作集（由 mygo 适配）。
type Window interface {
	LoadURL(url string) error
	Eval(ctx context.Context, code string) (any, error)
	Show()
	Hide()
	Focus()
	Destroy()
	IsDestroyed() bool
	OnClosed(fn func())
}
