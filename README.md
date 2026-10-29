# twitter_read_play

Twitter 新推文监控 → Codie 设备播报服务。服务端（Docker / 普通二进制）与桌面客户端
（Windows / macOS / Linux）共用同一份代码与前端。

- **设备**：一台 Codie 设备 = 一份授权与授权配置。页面上添加设备地址后一键触发配对
  （输入设备上的 6 位配对码完成）。
- **角色**：`system prompt / voice_clone / voice / model` 整体一个包，可编辑。
- **监控**：多个社媒用户 → 各自映射到角色 + 设备，定时轮询拉取新帖子，下角色包到设备后
  经 `chat/text` 让 Codie 播报。
- **首页导航 / 添加向导**：`/` 是导航首页（配置状态、设备/角色列表、快速入口、最近动态）；
  添加设备支持扫描局域网自动发现 Codie（`GET /api/machines/discover`），无需手工找 IP。
  详见 [docs/navigation-wizard.md](docs/navigation-wizard.md)。

> 本仓库当前构建**暂时隐藏了「图书」与「直播」相关的前端入口**（后端接口与数据保留）。
> 开关在 `front/src/features.js`（`SHOW_BOOK` / `SHOW_LIVE`），改回 `true` 即恢复显示。
> 相关后端说明见 [docs/book.md](docs/book.md)、[docs/livechat.md](docs/livechat.md)。

## 结构

```
main.go                         入口（源码构建默认服务模式；打包产物双击默认桌面窗口模式）
internal/bootstrap/             管理器组装 + 两种模式启动（desktop.go = MyGo 窗口）
internal/machine/               机器与配对管理（调用 workshop_inner_sdk）
internal/role/                  角色包 CRUD + 内置模板
internal/watch/                 轮询调度 + 播报管线
internal/book/                  图书：解码/拆章节（txt·md·epub）+ 设备朗读（按角色隔离）
cmd/importrole/                 离线导入预置角色包（prompt + VRM + 语音样本）
internal/twitter/               社媒 provider（socialdata / social_read / RSS 等）
internal/api/                   REST API
internal/guides/                直播来源申请教程（html 嵌入；SQLite provider_guides 当索引）
front/                          页面（React + Vite，构建产物 front/dist 由 go:embed 嵌入）
  src/features.js               前端功能开关（图书 / 直播入口）
  components/HomePage.jsx       首页导航（配置状态 / 设备 / 角色 / 最近动态）
  components/wizard/            分步添加向导（设备 → 角色 → 槽位 → 同步）
mygo.json / resources/          桌面端打包配置与图标
third_party/workshop_inner_sdk  设备 HTTP SDK（内联源码，只支持 1.5.3+ 固件）
third_party/livechat            直播弹幕来源
.github/workflows/release.yml   桌面端三平台打包（Windows / Linux / macOS）
```

## 本地运行

```bash
make front          # 前端 → front/dist（go:embed 需要）
make build          # 或 go build -o dist/twitter_read_play .
./dist/twitter_read_play -addr :8080 -data ./data
# 打开 http://localhost:8080，登录后进入控制台
```

登录账号密码优先级：页面「设置 → 登录账号」保存值 > `AUTH_USER` / `AUTH_PASS` env > 默认 `admin / 11111`。

## 桌面客户端（Windows / macOS / Linux）

同一份代码两种模式：桌面模式起一个 127.0.0.1 随机端口的本机服务，再用系统 WebView
（WebView2 / WKWebView / WebKitGTK）开窗口加载同一份嵌入前端，功能与服务端一致。

双击 `.app` / `.exe` / AppImage 默认就是桌面模式；要拿这些产物当服务端跑，
显式 `-desktop=false`。细节见 [docs/desktop.md](docs/desktop.md)。

```bash
make desktop-run        # 本机以桌面窗口跑起来
make desktop-windows    # Windows exe + NSIS 安装器
make desktop-linux      # Linux .deb / tar.gz + AppImage
make desktop-mac        # macOS .app + dmg（签名/公证也走这条）
```

| 平台 | 产物 | 运行时依赖 |
|---|---|---|
| Windows | `Twitter Read Play.exe`（免安装，GUI、图标、版本信息已嵌入）+ `Twitter Read Play Setup <版本> amd64.exe`（NSIS 安装器） | WebView2（Win10/11 自带） |
| Linux | `-x86_64.AppImage`，另附 `.deb` / `tar.gz` / `install.sh` | WebKitGTK（`libwebkit2gtk-4.0-37` 或 `-4.1-0`） |
| macOS | `Twitter Read Play.app`（通用二进制）+ `.dmg` | 无 |

### 自动打包（GitHub Actions）

`.github/workflows/release.yml`：

- **打 tag**（如 `git tag v1.0.0 && git push origin v1.0.0`）自动构建三平台产物并附加到 GitHub Release。
- **手动触发**（Actions → Release → Run workflow）只上传构建工件，不发 Release。

产物：Windows 免安装 exe + NSIS 安装器、Linux deb + tar.gz + AppImage、macOS app + dmg。

## Docker

```bash
docker build -t twitter_read_play .
docker run -d -p 8080:8080 -v $PWD/data:/app/data \
  -e AUTH_USER=admin -e AUTH_PASS=11111 twitter_read_play
```

账号密码一律通过 env `AUTH_USER` / `AUTH_PASS` 设置；页面「设置 → 登录账号」保存的值优先级最高，
其次 env，最后默认 `admin / 11111`（存 SQLite，重启不丢）。

## API（摘要）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/machines | 添加机器 `{name, addr, auth_scope}` |
| POST | /api/machines/:id/pair/start | 触发配对（设备弹窗） |
| POST | /api/machines/:id/pair/confirm | `{code}` 6 位码确认 |
| GET | /api/roles · POST /api/roles · DELETE /api/roles/:id | 角色包 CRUD |
| POST | /api/watches · POST /api/watches/:id/poll | 监控 CRUD / 立即拉取 |
| POST/DELETE | /api/roles/:id/monitor | 单用户 webhook 监控 |
| GET/POST/DELETE | /api/monitor/search | 共享 search monitor |
| POST | /api/webhook/socialdata | socialdata.tools 反向推送入口（HMAC 签名校验） |
| GET | /api/logs | 播报日志 |

## Webhook 反向通知（socialdata.tools Monitoring API）

- 公网地址**没有默认值**，必须先在设置页填写自己的公网地址（如 `https://trp.example.com`），
  推送地址 = 公网地址 + `/api/webhook/socialdata`；未填时创建/同步监控会直接报错
- 模式 C（推荐）：设置页「同步 search monitor」一条监控打包全部角色用户，`refresh_frequency` 建议 300s
- 模式 B：角色列表点 🔔 为单用户建独立 user-tweets 监控
- 签名校验：设置页填入与 socialdata.tools dashboard 一致的 webhook secret（HMAC-SHA256，5 分钟防重放窗口）
- 注意：推送失败不重试；webhook 生效期间对应角色自动停止轮询
