# 桌面客户端（Go + MyGo）

同一份代码两种跑法，前端与 API 完全共用：

| 模式 | 入口 | 说明 |
|---|---|---|
| 服务模式 | `twitter_read_play -addr :8080` | Docker / K8s 部署，浏览器访问；源码构建（`make run` / `go run`）的默认 |
| 桌面模式 | `twitter_read_play -desktop` | 起 127.0.0.1 随机端口本机服务，再用系统 WebView 开窗口加载它；**打包产物双击启动的默认** |

桌面模式没有第二套前端：窗口加载的就是 `go:embed all:front/dist` 里那份 React 产物，
页面通过 HTTP 调用同进程内的 API，所以 Docker 版能用的功能桌面版一模一样（机器配对、
角色、监控、日志、webhook 回调等）。

登录：桌面模式若**没有**配置任何账号密码（既无 `AUTH_USER`/`AUTH_PASS`，SQLite 设置里
也没有），会直接放行、开窗即进控制台；一旦配置了账号密码，仍然照常要求登录。
服务端部署不带这个放行逻辑（`api.Server.Desktop` 仅在 `-desktop` 时置位）。

```
main.go                       两种模式入口（-desktop 切桌面）
internal/bootstrap/bootstrap.go   store / managers / 路由组装 + 两种模式启动
internal/bootstrap/desktop.go     MyGo 窗口（标题、尺寸、StateKey 记忆窗口位置）
mygo.json                      MyGo 打包配置（名称 / 标识 / 版本 / 图标 / Linux 元数据）
resources/icon.png             应用图标（1024×1024，各平台自动转换 icns / ico / png）
scripts/make-appimage.sh       Linux 产物 → AppImage
scripts/make-dmg.sh            .app → dmg（macOS 用 hdiutil；Linux 用 Rock Ridge 镜像）
Dockerfile.desktop             三平台打包流水线（CI 用）
```

## 双击启动怎么进桌面模式

MyGo 不支持给打包产物传启动参数（`mygo.json` 只有 macOS 能写 `Info.plist`，
Linux / Windows 都没有 args 字段，桌面入口 `.desktop` / NSIS 快捷方式也不带参数），
所以双击 `.app` / `.exe` / AppImage 时进程既没有 `-desktop` 也没有环境变量。
默认值由 `bootstrap.DesktopDefault()` 按**安装位置**判断
（`internal/bootstrap/packaged.go`，单测 `packaged_test.go`）：

| 平台 | 判定 | 备注 |
|---|---|---|
| macOS | 可执行文件在 `.app/Contents/MacOS/` 里 | 同时 `mygo.json` 给 bundle 写了 `LSEnvironment.TRP_DESKTOP=1`（双保险；App Translocation 的随机路径也带这段，同样命中） |
| Windows | 一律桌面模式 | 本仓库 Windows 只出桌面产物，服务端走 Linux 容器 |
| Linux | `$APPIMAGE`、`/opt/<name>/`（deb）、`~/.local/`（tar.gz 的 install.sh） | Docker 的 `/app/main`、开发态路径都不命中 |

判定只看安装位置，不看 stdio / tty——容器里同样没有终端，按那个判会把服务端认成桌面端。
从这些位置跑服务端请显式 `-desktop=false` 或 `TRP_DESKTOP=0`；显式 `-desktop` 永远优先于默认值。

### 踩过的坑：双击 dmg 里的 app「闪退」

早期版本没有这个默认值，双击后进程掉进服务模式：

```
Finder 把工作目录设成只读的 /  → 数据目录用相对路径 "data"
→ store.New 报 mkdir data: read-only file system → log.Fatal → 退出码 1
```

表现是双击没反应（或一闪而过），而且**不是崩溃**——`~/Library/Logs/DiagnosticReports`
里没有 crash report，要看 `log show` 才能看到 `termination reported by launchd`。
排查手法（本机打包复现）：

```bash
open "build/darwin-universal/Twitter Read Play.app"      # 双击等价：进程 20ms 后退出
cd / && "build/darwin-universal/Twitter Read Play.app/Contents/MacOS/Twitter Read Play"
# → 2026/10/13 mkdir data: read-only file system，退出码 1
log show --last 2m --predicate 'process == "Twitter Read Play"' --style compact
```

## Windows 白屏 / 桌面端日志

正式 exe 是 `windowsgui` 子系统（没有控制台），`log` 写到 stderr 等于丢掉，用户只能看到白屏。
所以桌面模式从启动第一行起就把日志同时写进数据目录（单文件 5MB，超出在下次启动时轮转一次）：

| 平台 | 日志文件 |
|---|---|
| Windows | `%APPDATA%\TwitterReadPlay\logs\desktop.log` |
| macOS | `~/Library/Application Support/TwitterReadPlay/logs/desktop.log` |
| Linux | `~/.config/TwitterReadPlay/logs/desktop.log` |

排查顺序：

1. **看窗口本身**：主页面加载失败（`OnDidFailLoad`）时不再白屏，窗口里会显示诊断页——
   失败的 URL、WebView2 错误码/描述、本次注入的浏览器参数、日志路径，以及一个「重试」按钮。
   渲染进程崩溃（`OnRenderProcessGone`）会自动重载一次并记日志。
2. **看 `desktop.log`**：启动版本、数据目录、端口、进程代理、注入的 WebView2 参数、
   主页面/平台窗口的加载失败都会落在这里。`[webview]` 前缀就是 WebView 侧的记录。
3. **WebView2 的 Crashpad**：`%LOCALAPPDATA%\Twitter Read Play\WebView2\EBWebView\Crashpad\reports`
   只有在浏览器/渲染进程**崩溃**时才有 `.dmp`。这个目录存在但 `reports/`、`attachments/` 为空，
   说明不是崩溃，而是页面没能加载 —— 按第 1、2 步看具体错误。

### 代理

桌面端有**两套代理**，互不影响：

- **Go 进程的 HTTP 客户端**（socialdata、Codely 播报、OAuth 换 token 等）：走设置页的
  `http_proxy` / `no_http`，由 `netcfg` 写进进程环境。
- **WebView2 / WKWebView / WebKitGTK**（主窗口 + 平台登录/抓取窗口）：走**系统代理**
  （Clash / v2ray / 公司 GPO 等）。设置页里的 `http_proxy` 管不到它。

Windows 上启动时会给 WebView2 注入
`--proxy-bypass-list=localhost;127.0.0.1;[::1];<local>`（通过
`WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS`，因为 mygo 没暴露
`CoreWebView2EnvironmentOptions.AdditionalBrowserArguments`），保证窗口永远能取到本机服务，
同时内置浏览器访问外网仍走系统代理。

如果系统代理在更底层拦截（GPO / TUN 模式），回环绕过不生效，可以临时让 WebView2 **彻底不用代理**：

- 设环境变量 `TRP_WEBVIEW_NO_PROXY=1`，或
- 新建空文件 `%APPDATA%\TwitterReadPlay\webview_no_proxy`，

然后重启应用。注意这会让内置浏览器访问 X / YouTube 等站点时也不走代理——只用来确认问题，
确认后删掉文件即可恢复。若用户自己已经设置了 `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS`，
程序会保留原参数；已有的 `--no-proxy-server` / `--proxy-bypass-list` 不会被重复添加或覆盖。

## 技术选型

![桌面窗口加载嵌入前端](screenshot-desktop.png)

[MyGo](https://github.com/egoist/mygo)（`github.com/egoist/mygo`）：纯 Go、**无 cgo**，
所以任何一台机器都能交叉编译出三平台产物；窗口用系统自带 WebView：

| 平台 | WebView | 运行时依赖 |
|---|---|---|
| Windows 10/11 | WebView2 | 系统自带（老版本 Win10 可能需装 WebView2 Runtime） |
| macOS 12+ | WKWebView | 无 |
| Linux | WebKitGTK | `libwebkit2gtk-4.0-37`（Debian 12 / Ubuntu 22.04）或 `libwebkit2gtk-4.1-0`（Ubuntu 24.04+） |

## 本地开发

```bash
make front                 # 构建前端到 front/dist（go:embed 需要它存在）
make desktop-run           # 本机以桌面窗口模式启动（-desktop -data ./data）
go run github.com/egoist/mygo/cmd/mygo@v0.4.5 doctor   # 检查 WebView / 打包工具
```

桌面模式数据目录默认 `os.UserConfigDir()/TwitterReadPlay`
（macOS `~/Library/Application Support/`、Windows `%APPDATA%`、Linux `~/.config/`），
可用 `-data` 覆盖。

## 打包

```bash
make desktop-windows       # build/windows-amd64/Twitter Read Play.exe
make desktop-appimage      # build/linux-amd64/* + build/*.AppImage
make desktop               # 一次出三平台（Linux 机器上即可）
make desktop-mac           # 必须在 macOS 上跑：.app + 原生压缩 dmg
```

产物（`mygo build` 默认输出到 `build/`）：

| 平台 | 产物 |
|---|---|
| Windows | `Twitter Read Play.exe`（GUI 子系统、图标与版本信息已嵌入；装 NSIS 还会出 Setup 安装器） |
| Linux | `twitter-read-play`、`.desktop`、`.deb`、`-linux-amd64.tar.gz` + `install.sh`、以及 `-x86_64.AppImage` |
| macOS | `Twitter Read Play.app`（通用二进制）+ `Twitter Read Play-<版本>.dmg` |

dmg 的两种来源：

- **macOS 上**（`make desktop-mac` / CI 的 macOS 作业）：`hdiutil` 生成 Apple 原生压缩
  UDZO 镜像，可签名、可公证。
- **Linux 上**（`make desktop` / CI 的桌面作业）：`genisoimage -R -apple` 生成带 Rock Ridge
  与 Apple 扩展的镜像并命名 `.dmg`，macOS 可直接挂载、拖拽安装（保留可执行位）；
  同时产出 `.app.tar.gz` 兜底。若要 Apple 原生压缩 dmg，请在 Mac 上重跑一次。

本机打包依赖：`squashfs-tools`（mksquashfs）、`genisoimage`、`appimagetool`
（内网拉不到 GitHub releases 时，用 `APPIMAGE_TOOL=/path/to/appimagetool` 指定本地副本；
CI 里直接从 `appimagecrafters/appimage-builder` 镜像取，无需下载）。

Windows 安装器：在 Linux/macOS 上装 `nsis`（`apt install nsis` / `brew install makensis`）
后 `mygo build` 会额外产出 `Twitter Read Play Setup <版本> amd64.exe`。

## CI（GitHub Actions）

`.github/workflows/release.yml` 在打 tag 时构建并附加产物到 Release：

1. **windows**（`windows-latest`）：`npm ci && npm run build` → `mygo build -platform windows/amd64`
   产出免安装 `Twitter Read Play.exe` 与 NSIS 安装器 `Twitter Read Play Setup <版本> amd64.exe`
   （runner 自带 NSIS；缺失时 mygo 会自动下载）。
2. **linux**（`ubuntu-latest`）：`mygo build -platform linux/amd64` 产出 `*.deb`、`*.tar.gz`
   与可执行文件，再用 `scripts/make-appimage.sh` 打成 `AppImage`。
3. **macos**（`macos-latest`）：`mygo build -platform darwin/universal` 用 `hdiutil`
   出原生压缩 `dmg`（未签名；要签名/公证需在 `mygo.json` 配 `macos.signingIdentity`
   与 `macos.notarize`，并注入证书到 runner）。
4. **release** 作业把三个平台的产物汇总上传到 GitHub Release；`workflow_dispatch`
   也可手动触发，产物只作为构建工件（artifact）保留。

要点：

- MyGo 是纯 Go、无 cgo，三个平台都能在各自 runner 上本地编译；NSIS 安装器用
  `makensis` 原生生成，不需要 wine。
- 版本号取自 tag（`v1.2.3` → `1.2.3`），会写回 `mygo.json` 再打包。
- 本机等价命令：`make desktop-windows` / `make desktop-linux` / `make desktop-mac`。
