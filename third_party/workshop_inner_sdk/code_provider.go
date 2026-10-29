package clt01

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// StdinCodeProvider 终端交互式收集配对码（CLI 场景的默认实现）。
type StdinCodeProvider struct {
	// Prompt 为空时使用默认提示语。
	Prompt string
	// In/Out 缺省用 os.Stdin/os.Stdout。
	In  *os.File
	Out *os.File
}

// WaitForCode 阻塞读取用户输入的 6 位配对码。
func (p StdinCodeProvider) WaitForCode(ctx context.Context, challengeID string) (string, error) {
	in, out := p.In, p.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	prompt := p.Prompt
	if prompt == "" {
		prompt = "请输入设备屏幕上的 6 位配对码"
	}
	ch := make(chan string, 1)
	go func() {
		fmt.Fprintf(out, "%s (challenge %s): ", prompt, challengeID[:8])
		reader := bufio.NewReader(in)
		line, _ := reader.ReadString('\n')
		ch <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case code := <-ch:
		if code == "" {
			return "", fmt.Errorf("clt01: 配对码为空")
		}
		return code, nil
	}
}
