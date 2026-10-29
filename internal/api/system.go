// Package api：macOS「本地网络」授权辅助。
//
// macOS 15 起，应用首次访问局域网（设备扫描、配对都算）会弹「本地网络」权限弹窗；
// 用户点「不允许/拒绝」后扫描与配对会静默失败（表现为扫不到设备、配对超时），
// 只能去 系统设置 → 隐私与安全性 → 本地网络 手动打开授权。
//
// 这里给前端两样东西：
//   - /api/setup/status 的 host_os=darwin：前端据此在配对入口显示提示与按钮；
//   - POST /api/system/open/local-network：一键打开系统设置里的「本地网络」面板。
package api

import (
	"errors"
	"log"
	"net/http"
	"os/exec"
	"runtime"
)

// deepLink 系统设置「本地网络」面板（System Settings 的扩展 id）。
// 旧版系统不认识该 id 时会打开系统设置首页 —— 前端提示里带了完整路径兜底。
const localNetworkDeepLink = "x-apple.systempreferences:com.apple.LocalNetwork-Settings.extension"

// openLocalNetworkSettings POST /api/system/open/local-network
// 打开 macOS 系统设置的「本地网络」授权面板（仅 darwin；其他系统明确报错）。
func (s *Server) openLocalNetworkSettings(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "darwin" {
		s.err(w, http.StatusBadRequest, errors.New("local network permission pane only exists on macOS"))
		return
	}
	// open 是 macOS 自带工具；URL 是常量，没有用户输入，无注入面。
	if err := exec.Command("open", localNetworkDeepLink).Start(); err != nil {
		log.Printf("[pair] 打开系统设置「本地网络」面板失败: %v", err)
		s.err(w, http.StatusInternalServerError, errors.New("failed to open System Settings: "+err.Error()))
		return
	}
	log.Printf("[pair] 已打开系统设置「本地网络」面板（用户需允许本应用访问局域网）")
	s.json(w, map[string]bool{"ok": true})
}
