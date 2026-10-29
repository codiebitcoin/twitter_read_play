package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DesktopDefault 返回桌面模式的默认值（"1" 开 / "0" 关），作为 -desktop 的默认，
// 可被 TRP_DESKTOP 环境变量或显式 -desktop 覆盖。
//
// 为什么需要它：MyGo 不支持给打包产物传启动参数——mygo.json 只有 macOS 能写
// Info.plist，Linux / Windows 都没有 args 字段，桌面入口（.desktop、NSIS 快捷方式）
// 也不会带参数。所以双击 .app / .exe / AppImage 时，进程既没有 -desktop 也没有
// 环境变量，会掉进服务模式：
//
//	数据目录用相对路径 "data"，而 Finder 启动的工作目录是只读的 /
//	→ store.New 报 "mkdir data: read-only file system" → log.Fatal → 退出码 1
//
// 现象就是「双击没反应 / 闪退」（不是真崩溃，没有 crash report）。
// 因此由程序自己认出「我是被打包成桌面应用启动的」。
func DesktopDefault() string {
	exe, err := os.Executable()
	if err != nil {
		return "0"
	}
	if PackagedDesktop(exe, runtime.GOOS, os.Getenv("APPIMAGE")) {
		return "1"
	}
	return "0"
}

// PackagedDesktop 判断 exe 是否是「打包好的桌面应用」，而不是 go run / make run /
// 容器里的服务端进程。参数显式传入，便于按平台写单测。
//
// 判定只看安装位置，不看环境（stdio / tty 在容器里同样不是终端，不可靠）：
//   - darwin：在 .app/Contents/MacOS/ 里。App Translocation 的随机路径也带这段，
//     同样命中；make run / go run 的路径不含，保持服务模式。
//   - windows：一律桌面模式——本仓库 Windows 只出桌面产物，服务端走 Linux 容器。
//   - linux：AppImage（$APPIMAGE）、deb 的 /opt/<name>/、tar.gz install.sh 的
//     ~/.local/<name>.app/。Docker 的 /app/main 与开发态路径都不命中。
//
// 从这些位置跑服务端请显式 -desktop=false 或 TRP_DESKTOP=0。
func PackagedDesktop(exe, goos, appImage string) bool {
	exe = filepath.ToSlash(exe)
	switch goos {
	case "darwin":
		return strings.Contains(exe, ".app/Contents/MacOS/")
	case "windows":
		return true
	case "linux":
		if appImage != "" {
			return true
		}
		return strings.HasPrefix(exe, "/opt/") || strings.Contains(exe, "/.local/")
	}
	return false
}
