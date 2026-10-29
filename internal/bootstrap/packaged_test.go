package bootstrap

import "testing"

// 打包产物双击启动时没有 -desktop、也没有环境变量，只能靠安装位置认出来；
// 判错的代价是「双击闪退」（掉进服务模式，cwd 只读 → mkdir data 失败）。
func TestPackagedDesktop(t *testing.T) {
	cases := []struct {
		name     string
		exe      string
		goos     string
		appImage string
		want     bool
	}{
		{
			name: "macOS 双击 .app",
			exe:  "/Applications/Twitter Read Play.app/Contents/MacOS/Twitter Read Play",
			goos: "darwin", want: true,
		},
		{
			// Gatekeeper 路径随机化：仍带 .app/Contents/MacOS/
			name: "macOS App Translocation",
			exe:  "/private/var/folders/xy/abc/T/AppTranslocation/1234-5678/d/Twitter Read Play.app/Contents/MacOS/Twitter Read Play",
			goos: "darwin", want: true,
		},
		{
			name: "macOS 开发态 make run",
			exe:  "/Users/me/Project/twitter_read_play/dist/twitter_read_play",
			goos: "darwin", want: false,
		},
		{
			name: "macOS go run 临时目录",
			exe:  "/var/folders/xy/abc/T/go-build123/b001/exe/twitter_read_play",
			goos: "darwin", want: false,
		},
		{
			// 本仓库 Windows 只出桌面产物，服务端走 Linux 容器
			name: "Windows 免安装 exe",
			exe:  `C:\Users\me\Downloads\Twitter Read Play.exe`,
			goos: "windows", want: true,
		},
		{
			name: "Linux AppImage",
			exe:  "/tmp/.mount_trpXXXX/usr/bin/twitter-read-play",
			goos: "linux", appImage: "/home/me/Twitter Read Play-x86_64.AppImage", want: true,
		},
		{
			name: "Linux deb 安装到 /opt",
			exe:  "/opt/Twitter Read Play/twitter-read-play",
			goos: "linux", want: true,
		},
		{
			name: "Linux tar.gz install.sh 装到 ~/.local",
			exe:  "/home/me/.local/Twitter Read Play.app/twitter-read-play",
			goos: "linux", want: true,
		},
		{
			name: "Linux Docker 容器",
			exe:  "/app/main",
			goos: "linux", want: false,
		},
		{
			name: "Linux 开发态 make run",
			exe:  "/home/me/twitter_read_play/dist/twitter_read_play",
			goos: "linux", want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PackagedDesktop(c.exe, c.goos, c.appImage); got != c.want {
				t.Errorf("PackagedDesktop(%q, %q, %q) = %v, want %v", c.exe, c.goos, c.appImage, got, c.want)
			}
		})
	}
}
