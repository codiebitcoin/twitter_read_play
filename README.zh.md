# Twitter Read Play

监控社媒账号，让 **Codie** 设备用自定义人设和音色把新帖播报出来——桌面应用（Windows / macOS / Linux）或自建服务端，两者共用同一份代码与界面。

[English](README.md) · 中文 — 带截图的完整手册：[guide/USAGE.zh.md](guide/USAGE.zh.md)（[English](guide/USAGE.md) · PDF 在 `guide/`）

![首页](guide/img/03-home.png)

## 快速开始

**桌面端** —— 从最新 [GitHub Release](https://github.com/codiebitcoin/twitter_read_play/releases) 下载安装包：Windows `.exe` / NSIS 安装器、macOS `.dmg`、Linux `.deb` / AppImage。打开应用即进控制台窗口。

**服务端（Docker）** —— 用 GHCR 现成镜像一条命令启动：

```bash
docker run -d -p 8080:8080 -v trp-data:/app/data \
  -e AUTH_USER=admin -e AUTH_PASS=change-me \
  ghcr.io/codiebitcoin/twitter_read_play:latest
# 打开 http://localhost:8080 登录
```

## 使用流程

1. **初始化并登录** —— 首次打开会要求设置管理员账号密码（也可用 `AUTH_USER` / `AUTH_PASS` env 预置，密码以 bcrypt 哈希存储），之后到 设置 → 登录账号 修改。
2. **添加设备** —— 「＋ 添加」向导会扫描局域网发现 Codie，按提示完成配对（输入设备上显示的 6 位配对码）。
3. **设置角色** —— 角色是一个整体包：人设 prompt、克隆音色、VRM 形象。
4. **绑定槽位** —— 把角色绑到设备的某个 slot，然后同步（音色克隆 + VRM）到设备。
5. **指定账号** —— 添加数据源（如 socialdata.tools API Key）与账号 handle，开启监听。
6. 服务按间隔轮询到新帖后，**设备会按该人设与音色朗读出来**；播报记录见「日志」页。

登录密码优先级：页面「设置 → 登录账号」保存值（bcrypt 哈希存储）> `AUTH_USER` / `AUTH_PASS` env（明文，仅供启动预置）；两者都未配置时，首次打开登录页会要求设置账号。没有内置默认密码。

## 桌面端安装包

| 平台 | 产物 | 运行时依赖 |
|---|---|---|
| Windows | `Twitter Read Play.exe`（免安装）+ NSIS 安装器 | WebView2（Win10/11 自带） |
| Linux | `.AppImage`、`.deb`、`.tar.gz`、`install.sh` | WebKitGTK（`libwebkit2gtk-4.1-0`） |
| macOS | `Twitter Read Play.app`（通用二进制）+ `.dmg` | 无 |

## 从源码构建

```bash
git clone --recurse-submodules https://github.com/codiebitcoin/twitter_read_play.git
cd twitter_read_play
make front build   # 前端 front/ → 构建产物由 go:embed 嵌入
./dist/twitter_read_play -addr :8080 -data ./data
```

## 自动打包（GitHub Actions）

`.github/workflows/release.yml`：

- **打 tag**（`git tag v1.0.0 && git push origin v1.0.0`）构建三平台产物并附加到 Release，同时把服务端镜像推到 GHCR（`ghcr.io/codiebitcoin/twitter_read_play:<版本>` + `:latest`）。
- **手动触发**（Actions → Release → Run workflow）只上传构建工件，不发 Release、不推镜像。

## 文档

- 使用手册（带截图）：[guide/USAGE.zh.md](guide/USAGE.zh.md)（中文）· [guide/USAGE.md](guide/USAGE.md)（English）· PDF 在 `guide/`
- 桌面端原理：[docs/desktop.md](docs/desktop.md)
- 添加向导：[docs/navigation-wizard.md](docs/navigation-wizard.md)
