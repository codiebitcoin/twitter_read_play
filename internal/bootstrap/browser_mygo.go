package bootstrap

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"twitter_read_play/internal/browser"
)

// mygoDriver 用 mygo 窗口实现 browser.Driver。
// 登录窗可见，工作窗默认隐藏；页面共享 mygo 的持久化 profile。
type mygoDriver struct {
	ready chan struct{}
	once  sync.Once
}

func newMygoDriver() *mygoDriver {
	return &mygoDriver{ready: make(chan struct{})}
}

// markReady 在 mygo.App.WhenReady 里调用。
func (d *mygoDriver) markReady() { d.once.Do(func() { close(d.ready) }) }

func (d *mygoDriver) Available() bool { return true }

func (d *mygoDriver) Open(opts browser.OpenOptions) (browser.Window, error) {
	select {
	case <-d.ready:
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("mygo 尚未就绪")
	}
	width, height := opts.Width, opts.Height
	if width <= 0 {
		width = 1024
	}
	if height <= 0 {
		height = 768
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:    opts.Title,
		URL:      opts.URL,
		Width:    width,
		Height:   height,
		Hidden:   opts.Hidden,
		StateKey: opts.StateKey,
		Page: mygo.PageOptions{
			PreloadScript: opts.PreloadScript,
			UserAgent:     opts.UserAgent,
		},
	})
	if win == nil {
		return nil, fmt.Errorf("创建窗口失败")
	}
	// 浏览器窗口（平台登录 / 抓取）失败时也要留痕：这些窗口多数是隐藏的，
	// 失败了只会表现为「没数据」，日志里连一条记录都没有。
	if page := win.Page(); page != nil {
		page.OnDidFailLoad(func(e *mygo.LoadError) {
			if e == nil {
				return
			}
			log.Printf("[webview] 窗口 %q 加载失败: url=%s code=%d desc=%s",
				opts.Title, safeURL(e.URL), e.Code, e.Description)
		})
		page.OnRenderProcessGone(func(reason string) {
			log.Printf("[webview] 窗口 %q 渲染进程退出: %s", opts.Title, reason)
		})
	}
	// MyGo 默认把 target=_blank / window.open 交给系统浏览器（Mac 上就是 Chrome）。
	// 登录链路（例如 X → Sign in with Google）必须留在应用内，否则 OAuth 弹窗
	// 和主 WebView 不是同一个 profile，登录态回不来。
	win.Page().SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		u, err := url.Parse(req.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil
		}
		w, h := req.Width, req.Height
		if w <= 0 {
			w = 900
		}
		if h <= 0 {
			h = 720
		}
		var parent *mygo.Window
		if !opts.Hidden {
			parent = win
		}
		return &mygo.WindowOptions{
			Title:  opts.Title,
			Width:  w,
			Height: h,
			Parent: parent,
			Page: mygo.PageOptions{
				PreloadScript: opts.PreloadScript,
				UserAgent:     opts.UserAgent,
			},
		}
	})
	return &mygoWindow{w: win}, nil
}

type mygoWindow struct{ w *mygo.Window }

func (w *mygoWindow) LoadURL(u string) error { return w.w.Page().LoadURL(u) }

func (w *mygoWindow) Eval(ctx context.Context, code string) (any, error) {
	return w.w.Page().EvalContext(ctx, code)
}

func (w *mygoWindow) Show()  { w.w.Show() }
func (w *mygoWindow) Hide()  { w.w.Hide() }
func (w *mygoWindow) Focus() { w.w.Focus() }

func (w *mygoWindow) Destroy() { w.w.Destroy() }

func (w *mygoWindow) IsDestroyed() bool { return w.w.IsDestroyed() }

func (w *mygoWindow) OnClosed(fn func()) { w.w.OnClosed(fn) }
