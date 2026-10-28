# workshop_inner_sdk（clt01）

CLT01 设备（`clt01_embed_backend`）局域网访问 Go SDK：函数式调用、完整配对流程封装、凭据管理、Web 控制台（含 `/config/pair` 授权页）反向代理内嵌。

> 模块名 `workshop_inner_sdk`（原 `workshop_api` 已并入本仓库，作为内联源码维护）。
> **只支持 1.5.3+ 固件**，接口默认全部走最新版本；测试代码在 `test/` 目录。

## 安装

本 SDK 随主仓库以内联源码维护（`third_party/workshop_inner_sdk`），通过主仓库 `go.mod` 的
`replace workshop_inner_sdk => ./third_party/workshop_inner_sdk` 引用，无需单独安装。

## 快速开始

### 1. 函数式配对（输入 IP → 输入 6 位配对码，后台自动走完整流程）

```go
package main

import (
    "context"
    "fmt"
    "time"

    clt01 "workshop_inner_sdk"
)

func main() {
    c, _ := clt01.New("192.168.1.10:8081")

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    // 后台自动执行：ping → bootstrap → pair/request（设备弹配对窗）
    //   → StdinCodeProvider 等待用户输入 6 位码 → pair/confirm 换取凭据
    creds, err := c.Pair(ctx, clt01.StdinCodeProvider{}, clt01.AuthScopePermanent)
    if err != nil {
        panic(err)
    }

    store, _ := clt01.NewStore() // 默认 ~/.clt01sdk/credentials.json
    _ = store.Save(creds)

    fmt.Println("配对成功:", creds.DeviceID, creds.UserID)
}
```

GUI 程序自行实现配对码收集：

```go
creds, err := c.Pair(ctx, clt01.CodeFunc(func(ctx context.Context, challengeID string) (string, error) {
    return myDialog.AskPairingCode(challengeID) // 返回用户输入的 6 位码
}), clt01.AuthScopeSession) // session=12 小时短期凭据；permanent=长期
```

### 2. 已配对设备直接调用（凭据复用）

```go
c, err := clt01.ClientFromStore("192.168.1.10:8081")
info, err := c.SystemInfo()
fmt.Println(info.Username, info.Capabilities)

// 通用签名请求
data, err := c.Do("GET", "/v1/api/data/user/chat/history/dates", nil, "")
data, err = c.Do("POST", "/v1/api/data/user/chat/text", []byte(`{"text":"你好"}`), clt01.PayloadModeJSON)
```

完整接口清单见 `clt01_embed_backend/docs/web/LOCAL_API_REFERENCE.md`。

### 3. 内嵌设备授权页面（/config/pair）

```go
mux := http.NewServeMux()
mux.Handle("/", c.ConsoleProxy())          // 反向代理设备控制台
http.ListenAndServe("127.0.0.1:8090", mux)
// 浏览器 / WebView 打开 http://127.0.0.1:8090/config/pair
```

或直接拿页面地址给 WebView 加载：`c.ConsoleURL("/config/pair")`。
页面内配对与签名由设备自带前端完成，凭据存于浏览器 localStorage，与 SDK 凭据独立。

## API 一览

| 符号 | 说明 |
|---|---|
| `New(addr) (*Client, error)` | 创建客户端（addr 可带可不带 `http://`） |
| `Client.Pair(ctx, CodeProvider, scope) (*Credentials, error)` | 完整配对流程（自动串联全部后台调用） |
| `Client.RequestPairing / ConfirmPairing / CancelPairing` | 配对三步，可拆开自定义编排 |
| `Client.Ping / Bootstrap` | 免鉴权探测与引导信息 |
| `Client.SystemInfo()` | 签名获取系统信息 |
| `Client.Do / GetJSON / PostJSON / GetRaw` | 通用签名请求，返回统一信封 data |
| `Client.Apply(*Credentials)` | 应用凭据 |
| `Client.ConsoleProxy() / ConsoleURL(path)` | 内嵌控制台反向代理 / 页面地址 |
| `Store`（`NewStore / Save / Load / Delete`） | 凭据持久化（0600，默认 `~/.clt01sdk/credentials.json`） |
| `ClientFromStore(addr)` | 从凭据库直接构建已授权客户端 |
| `StdinCodeProvider / CodeFunc` | 配对码收集实现 |
| `MCPServer`（`NewMCPServer / ServeStdio / Handler`） | MCP 服务端模式：stdio / Streamable HTTP（见「MCP 模式」章节） |
| `Sign(secret, method, path, ts, nonce, payloadMode, bodyHash, userID)` | 暴露签名算法本身 |
| `Credentials.SessionExpired()` | session 凭据过期判断 |

错误统一为 `*APIError`（`StatusCode` / `Code` / `Message`），可按 `Code`（如 `LOCAL_SESSION_EXPIRED`、`PAIRING_REQUEST_RATE_LIMITED`）分支处理。

## 测试

测试代码统一放在 `test/` 目录（外部测试包 `clt01_test`）：

```bash
# 单元测试（无需设备）
go test ./test/

# 在线冒烟（对真实设备，默认跳过）
CLT01_LIVE_ADDR=192.168.1.10:8081 \
CLT01_LIVE_SECRET=<local_secret> CLT01_LIVE_SESSION=<session_id> CLT01_LIVE_USER=<user_id> \
go test ./test/ -run TestLive -v
```

在线用例覆盖：ping/bootstrap、签名请求、坏签名拒绝（`LOCAL_SECRET_MISMATCH`）、pair request→cancel 链路。

## CLI 示例

```bash
go run ./cmd/clt01-example pair 192.168.1.10:8081             # 交互式配对并保存凭据
go run ./cmd/clt01-example info 192.168.1.10:8081             # 用已存凭据查询设备
go run ./cmd/clt01-example console 192.168.1.10:8081 :8090    # 内嵌控制台代理
```

## MCP 模式（模型上下文协议服务端）

SDK 内建 MCP 服务端（零外部依赖，手写 JSON-RPC 2.0），把配对 / 角色配置 / 播报等能力
以 MCP tools 暴露给 Cursor / Claude Desktop 等模型客户端，对话即可操控设备。

### 工具清单

| 工具 | 说明 |
|---|---|
| `clt01_ping` | 检查设备可达（免鉴权 ping + bootstrap） |
| `clt01_pair_start` | 请求设备弹出 6 位配对码窗口（5 分钟有效） |
| `clt01_pair_confirm` | 用配对码完成配对并保存凭据（scope=session/permanent） |
| `clt01_pair_cancel` / `clt01_pair_status` | 取消配对挑战 / 查询本地凭据状态 |
| `clt01_system_info` | 读取系统信息（需已配对） |
| `clt01_character_get / set / list` | 读取 / 原子设置（SetInfo 语义）/ 列出角色槽配置 |
| `clt01_voice_groups` | 音色分组与 voice_id 列表 |
| `clt01_play_chat` | 播报文本（chat/turns 通道，返回 chat_id） |
| `clt01_play_state` / `clt01_play_cancel` | 播放状态查询 / 取消播报 |
| `clt01_tts_say` | TTS 直读（不经角色 LLM 管线） |
| `clt01_raw_request` | 通用签名请求逃生口（任意 LOCAL API） |

每个工具参数可选 `addr` 覆盖默认设备地址；签名调用复用 `~/.clt01sdk/credentials.json`。

### 启动

```bash
# 构建
go build -o ./clt01-mcp ./cmd/clt01-mcp

# 先配对（签名工具需要凭据），或在 MCP 对话里走 pair_start → pair_confirm
go run ./cmd/clt01-example pair 192.168.1.10:8081

# 方式一：stdio 子进程模式（Cursor / Claude Desktop，推荐）
./clt01-mcp stdio 192.168.1.10:8081

# 方式二：Streamable HTTP 模式（局域网/远程复用，端点 /mcp，默认监听 :8091）
./clt01-mcp http 192.168.1.10:8081 :8091
```

设备地址也可用环境变量 `CLT01_ADDR` 指定；stdio 模式协议响应走 stdout、日志走 stderr。

### Cursor 接入（stdio）

项目 `.cursor/mcp.json` 或全局 `~/.cursor/mcp.json`：

```json
{
  "mcpServers": {
    "clt01": {
      "command": "/absolute/path/to/clt01-mcp",
      "args": ["stdio", "192.168.1.10:8081"]
    }
  }
}
```

保存后 Cursor 自动拉起子进程。HTTP 模式则添加 "url" 型 MCP，填 `http://127.0.0.1:8091/mcp`
（无状态实现，无需 session/token）。接入后在对话里即可说「让设备播报xx」「改 1 号角色
prompt」「查音色列表」等，模型自动选工具调用。

## 原子语义操作（推荐入口）

```go
c.SetModel(slot, cloth, filename, data, nil)          // 设模型（gzip 分块上传）
c.ApplyModel(slot, cloth, filename, nil)              // 仅应用已有模型
c.SetInfo(slot, &clt01.CharacterSettings{Name: "…"})  // 设角色信息
c.SetCloneVoice(audio, "a.wav", &clt01.CloneVoiceOptions{GroupKey: "g"}) // 设克隆音色
res, _ := c.PlayChat(text, &clt01.PlayChatOptions{    // 播报
    Slot: 1, Language: "zh", VoiceID: "v", GroupKey: "g"})
// res.Channel: "chat-turns"（/data/user/chat/turns）
```

## 固件版本（只支持 1.5.3+）

本 SDK 只支持 1.5.3 及以上固件，**接口默认全部走最新版本**：
上传走 gzip 分块通道（job[transport=gzip] → chunk/:seq → complete，8MiB 流式硬限）、
应用走 outfit/apply（早期 1.5.3 构建 404 时回退三件套 model-apply）、
落盘校验以 uploaded-models 为权威。更早固件（1.5.2 / legacy）的兼容通道已移除。
探测失败时也按 1.5.3 处理；`Firmware()` 仅用于日志与联调：

```go
fw, _ := c.Firmware()                  // 探测并缓存（版本 / drafts / uploaded-models）
c.ForceFirmware(clt01.VerChunkedUpload) // 联调时强制指定版本
c.UploadVRM(slot, cloth, filename, data, nil)   // 上传+应用+落盘校验
c.VerifyVRMPersisted(slot, cloth)               // 防假 SUCCESS
```

## 签名算法（与设备端一致）

`sig = hex(HMAC-SHA256(secret, "METHOD|path|timestamp|nonce|payloadMode|bodyHash|userID"))`

头：`X-Local-Sig` / `X-Local-Nonce` / `X-Local-Timestamp` / `X-Local-Payload-Mode` / `X-Local-Body-Hash` / `X-Local-User-Id` / `X-Local-Session-Id`（session 模式）。SDK 已全部自动处理。
