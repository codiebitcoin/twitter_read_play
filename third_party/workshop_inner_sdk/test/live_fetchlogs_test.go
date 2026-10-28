package clt01_test

import (
	. "workshop_inner_sdk"
	"context"
	"os"
	"testing"
	"time"
)

// 抓设备反馈包（日志）用于分析，CLT01_LOG_OUT 指定输出
func TestFetchDeviceLogs(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := c.Feedback(ctx, &FeedbackOptions{
		Title: "clone diag", Description: "voice clone 502 analysis",
		IncludeBasicLogs: true, IncludeSystemLogs: true, Timeout: 8 * time.Minute,
	})
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	out := envOr("CLT01_LOG_OUT")
	if out == "" {
		out = "/tmp/252-logs2.zip"
	}
	if err := os.WriteFile(out, res.Archive, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("logs saved: %s (%d bytes) report=%s", out, len(res.Archive), res.ReportID)
}
