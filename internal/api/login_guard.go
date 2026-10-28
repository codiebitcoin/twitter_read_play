package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"sync"
	"time"
)

// 登录防爆破：同一来源 IP 连续失败达到阈值后，在冷却窗口内直接拒绝 /api/login。
// 进程内存态即可（重启清零）；桌面模式不走登录，不受影响。经反代部署时所有请求
// 共享代理的 RemoteAddr，阈值按「保守偏宽」取，避免误伤正常用户。
const (
	loginMaxFails   = 10
	loginFailWindow = time.Minute
)

type loginFails struct {
	count   int
	resetAt time.Time
}

type loginGuard struct {
	mu    sync.Mutex
	fails map[string]*loginFails
}

func newLoginGuard() *loginGuard {
	return &loginGuard{fails: map[string]*loginFails{}}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limited 该 IP 是否处于冷却期（达到失败阈值且窗口未过）。
func (g *loginGuard) limited(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, ok := g.fails[ip]
	if !ok {
		return false
	}
	if time.Now().After(f.resetAt) {
		delete(g.fails, ip)
		return false
	}
	return f.count >= loginMaxFails
}

// fail 记一次失败；窗口从第一次失败起算。
func (g *loginGuard) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	f, ok := g.fails[ip]
	if !ok || now.After(f.resetAt) {
		g.fails[ip] = &loginFails{count: 1, resetAt: now.Add(loginFailWindow)}
		return
	}
	f.count++
}

// clear 登录成功后清掉该 IP 的失败计数。
func (g *loginGuard) clear(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, ip)
}

// credEq 常量时间比较两条凭据。先各自 SHA-256 定长，避免长度差泄露
// （ConstantTimeCompare 长度不同会立即返回）。
func credEq(a, b string) bool {
	hb := sha256.Sum256([]byte(b))
	h := sha256.Sum256([]byte(a))
	return subtle.ConstantTimeCompare(h[:], hb[:]) == 1
}
