package clt01_test

import (
	. "workshop_inner_sdk"
	"context"
	"os"
	"testing"
	"time"
)

// 联网冒烟测试（默认跳过）：
//
//	CLT01_LIVE_ADDR=192.168.1.10:8081 \
//	CLT01_LIVE_SECRET=... CLT01_LIVE_SESSION=... CLT01_LIVE_USER=... \
//	go test -run TestLive -v ./...
func liveClient(t *testing.T) *Client {
	t.Helper()
	addr := os.Getenv("CLT01_LIVE_ADDR")
	if addr == "" {
		t.Skip("未设置 CLT01_LIVE_ADDR，跳过在线测试")
	}
	c, err := New(addr)
	if err != nil {
		t.Fatal(err)
	}
	if s := os.Getenv("CLT01_LIVE_SECRET"); s != "" {
		c.Secret = s
		c.SessionID = os.Getenv("CLT01_LIVE_SESSION")
		c.UserID = os.Getenv("CLT01_LIVE_USER")
	}
	return c
}

func TestLivePingBootstrap(t *testing.T) {
	c := liveClient(t)
	if err := c.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	boot, err := c.Bootstrap()
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Logf("bootstrap: %+v", boot)
}

func TestLiveSignedSystemInfo(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("未设置 CLT01_LIVE_SECRET，跳过签名测试")
	}
	info, err := c.SystemInfo()
	if err != nil {
		t.Fatalf("system info: %v", err)
	}
	if info.UserID == "" {
		t.Fatal("system info 缺少 user_id")
	}
	t.Logf("system: user=%s tz=%s caps=%d", info.Username, info.Timezone, len(info.Capabilities))
}

func TestLiveBadSignatureRejected(t *testing.T) {
	c := liveClient(t)
	c.Secret = "00000000000000000000000000000000"
	_, err := c.SystemInfo()
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("期望 APIError，得到 %v", err)
	}
	if apiErr.Code != "LOCAL_SECRET_MISMATCH" {
		t.Fatalf("期望 LOCAL_SECRET_MISMATCH，得到 %s", apiErr.Code)
	}
}

func TestLivePairRequestAndCancel(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 只验证 request → cancel 链路可用（不输入配对码，避免占用设备窗口）
	chID, _, err := c.RequestPairing()
	if err != nil {
		t.Fatalf("pair request: %v", err)
	}
	defer func() { _ = c.CancelPairing(chID) }()
	if chID == "" {
		t.Fatal("challenge_id 为空")
	}
	_ = ctx
}
