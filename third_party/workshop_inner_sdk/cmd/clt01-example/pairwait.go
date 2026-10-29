package main

// pairwait 模式：请求配对后不读终端，而是轮询码文件里出现的 6 位码再确认。
// 用法: clt01-example pairwait <device_addr> <code_file> [timeout_seconds]
// 适合无人值守场景：设备屏幕出码后，任何一方把码写进 code_file 即完成配对。

import (
	"fmt"
	"os"
	"strings"
	"time"

	clt01 "workshop_inner_sdk"
)

func runPairwait(addr, codeFile string, timeout time.Duration) error {
	c, err := clt01.New(addr)
	if err != nil {
		return err
	}
	challengeID, _, err := c.RequestPairing()
	if err != nil {
		return fmt.Errorf("pair request: %w", err)
	}
	fmt.Printf("challenge %s — 等待配对码写入 %s（超时 %s）\n", challengeID, codeFile, timeout)
	deadline := time.Now().Add(timeout)
	var code string
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(codeFile); err == nil {
			code = strings.TrimSpace(string(b))
			if len(code) >= 6 {
				code = code[:6]
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if code == "" {
		_ = c.CancelPairing(challengeID)
		return fmt.Errorf("超时未收到配对码")
	}
	creds, err := c.ConfirmPairing(challengeID, code, clt01.AuthScopePermanent)
	if err != nil {
		return fmt.Errorf("pair confirm: %w", err)
	}
	store, err := clt01.NewStore()
	if err != nil {
		return err
	}
	if err := store.Save(creds); err != nil {
		return err
	}
	_ = os.Remove(codeFile)
	fmt.Printf("配对成功: device=%s user=%s（凭据已保存到 %s）\n", creds.DeviceID, creds.UserID, store.Path)
	return nil
}
