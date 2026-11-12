// Package dbg 全局 debug 开关：设置页「Debug 日志」控制，保存即生效（不用重启）。
// 开启后，登录/抓取链路的关键步骤会往标准 log（桌面模式即 desktop.log）里
// 多写 [debug] 行：窗口开关、探针原始结果、fetch 状态码、加载失败原因——
// 这些是「登录不成功」「后台没数据」类问题唯一能远程定位的线索。
package dbg

import (
	"log"
	"strings"
	"sync/atomic"
)

var on atomic.Bool

// Set 设置开关（设置保存时调用，实时生效）。
func Set(v bool) { on.Store(v) }

// Enabled 是否开启。
func Enabled() bool { return on.Load() }

// Logf 开启时打一行 [debug] 日志；未开启是零成本空操作。
func Logf(format string, args ...any) {
	if on.Load() {
		log.Printf("[debug] "+format, args...)
	}
}

// Head 截断文本用于日志：超长 body 只留头部，避免日志膨胀。
func Head(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
