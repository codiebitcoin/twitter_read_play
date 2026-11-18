//go:build !windows

package bootstrap

// windowsSoftwareRenderingPreferred 非 Windows 平台没有 WebView2，无需软件渲染兜底。
func windowsSoftwareRenderingPreferred() bool { return false }
