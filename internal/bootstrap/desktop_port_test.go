package bootstrap

import (
	"net"
	"testing"
)

// 桌面端口从 7991 起：OAuth 回调要登记进 Twitch/Google 后台，必须稳定可预期。
func TestListenDesktopStartsAt7991(t *testing.T) {
	ln, err := listenDesktop()
	if err != nil {
		t.Fatalf("listenDesktop: %v", err)
	}
	defer ln.Close()
	got := ln.Addr().(*net.TCPAddr).Port
	if got != desktopStartPort {
		t.Fatalf("起始端口 = %d, want %d", got, desktopStartPort)
	}
}

// 7991 被占时顺延 +1，不能失败也不能落回随机端口。
func TestListenDesktopSkipsOccupied(t *testing.T) {
	hold, err := net.Listen("tcp", "127.0.0.1:7991")
	if err != nil {
		t.Skipf("7991 无法占用（可能已被其他进程监听）: %v", err)
	}
	defer hold.Close()
	ln, err := listenDesktop()
	if err != nil {
		t.Fatalf("listenDesktop: %v", err)
	}
	defer ln.Close()
	got := ln.Addr().(*net.TCPAddr).Port
	if got != desktopStartPort+1 {
		t.Fatalf("7991 被占时应顺延到 %d, got %d", desktopStartPort+1, got)
	}
}
