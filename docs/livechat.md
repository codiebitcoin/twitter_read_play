# 直播弹幕（living-model）

拉取 Twitch / YouTube / Bilibili 直播间弹幕，**来源可配置切换**，每路直播 = 一条配置。

> **2026-10-26 起**：设置页的「旧版直播源」卡片已删除，逐路 kind 表单与全局 Bilibili Cookie UI 不再提供。
> 本文记录的后端 `livechat` API 仅用于角色 Stream 的自动桥接与旧数据兼容。
>
> **Live 渠道去重后（docs/PLAN-live-channel-dedupe.md），新渠道模型只保留三个入口：**
> `twitch_browser`（内置浏览器 popout 聊天页轮询）、`bilibili_blivedm`（blivedm 公开 WebSocket，
> Cookie 可选）、`youtube_browser`（内置浏览器轮询 live_chat）。
> `twitch_eventsub` / `youtube_live_api` / `chatdl_go` / `bilibili_browser` 适配器已从注册表移除。

## 来源（kind）

| kind | 说明 | 凭证 |
|---|---|---|
| `twitch_eventsub` | Twitch 官方 EventSub WebSocket，订阅 `channel.chat.message` 推送弹幕，低延迟（旧配置兼容；新链路走 `twitch_browser`） | Twitch App 的 `client_id` + 一个 user access token（scope `user:read:chat`，可用 [twitchtokengenerator](https://twitchtokengenerator.com) 之类生成） |
| `youtube_api` | YouTube Live Streaming API：`videos.list` 取 `activeLiveChatId` → `liveChatMessages.list` 轮询（自动遵守 `pollingIntervalMillis`） | Google API Key + 开通 YouTube Data API v3 |
| `chat_downloader` | 调本机 `python3` + [chat-downloader](https://github.com/xenova/chat-downloader) 库，stdout JSON 行协议；支持 YouTube 直播/回放/首播、Twitch 直播/VOD/剪辑 | 无（免认证兜底；`pip install chat-downloader`） |
| `youtube_noauth` | 集成 [youtube-live-chat-downloader](https://github.com/abhinavxd/youtube-live-chat-downloader)（innertube continuation 循环） | 无。国内需 `HTTPS_PROXY` |
| ~~`twitch_irc`~~ | 已移除：IRC 6697 被常见代理节点黑洞（`irc read: i/o timeout`），Twitch 改走 `twitch_browser` 页面轮询 | — |
| `bilibili_blivedm` | B 站公开直播弹幕，社区 SDK [blivedm-go](https://github.com/Akegarasu/blivedm-go)（WebSocket）。后台实现在 `third_party/livechat` | Cookie（`SESSDATA` + `bili_jct`）可选；游客可读，填了昵称/礼物信息更完整 |

四者归一成同一个 `Message` 形状：`{seq, id, channel, user, text, at, kind(text/cheer/superchat/sub), amount}`。

原先的 `/live` 配置页（后并入设置页「直播」标签）已下线；下方 API 仍保留给角色 Stream 的自动桥接。
申请步骤教程接口（`GET /api/providers/{kind}/guide`，索引表 `provider_guides`）继续可用，书面记录见 [livechat-oauth.md](livechat-oauth.md)。

角色详情里 **Stream** 与 Feed / Book 同级、单独配置：绑定一路源、默认 1s 间隔、独立关键字、仅 SC/打赏开关。不复用 Feed/Watch 的 `interval` / `filter_words`。

## 配置（存 SQLite blobs `livechat.json`，页面/接口皆可改）

字段（POST /api/livechat 的 body）：

```jsonc
{
  "id": "",                       // 空=新建；非空=更新
  "title": "备注",
  "kind": "twitch_eventsub",      // twitch_eventsub | youtube_api | chat_downloader | youtube_noauth | bilibili_blivedm（twitch_irc 已移除）
  "channel": "somechannel",       // twitch login / yt 视频 URL 或 11 位 id / B 站房间号或 live.bilibili.com URL / chat_downloader 任意 URL
  "enabled": true,                // 服务启动即自动开拉；改成 false 自动停
  "twitch_client_id": "...",      // twitch_eventsub 必填
  "twitch_token": "...",          // twitch_eventsub 必填（user token, user:read:chat）
  "twitch_user_id": "",           // 可空：自动走 /validate 反解
  "youtube_api_key": "...",       // youtube_api 必填
  "python_path": "python3",       // chat_downloader 可选
  "extra_args": "--start_time 0", // chat_downloader 可选，透传 CLI 参数
  "bilibili_cookie": "",          // bilibili_blivedm 可选（SESSDATA + bili_jct）
  "base_url": ""                  // 可选 API 反代（twitch helix / googleapis）
}
```

## REST 接口

```
GET    /api/livechat                     配置列表 + 运行状态（running/last_error/count）
POST   /api/livechat                     保存（Enabled=true 且没在跑 → 立即启动；配置变更 → 重启生效）
POST   /api/livechat/random              随机挑一路正在直播的（body: {kind}；YouTube/Twitch 需已登录，B 站公开）
DELETE /api/livechat/{id}                删除并停止
POST   /api/livechat/{id}/start          手动启动
POST   /api/livechat/{id}/stop           手动停止
GET    /api/livechat/{id}/messages?after=<seq>&limit=200
                                         增量拉弹幕：返回 (after, 最新]，响应带 latest_seq 供下次游标
```

弹幕只存内存（每路最近 1000 条），重启即丢 —— 流式数据不落盘；要接播报管线时直接订阅 Messages 即可。

## 运行语义

- 断线自动重连（指数退避，2s → 60s 封顶）：Twitch WS 断开/心跳超时（90s 无消息）内建重连；
  YouTube 直播未开播 / liveChatId 失效会一直重试到开播。
- Twitch 收到 `session_reconnect` 会按官方迁移流程处理；订阅 409 冲突自动清旧订阅重建。
- `chat_downloader` 子进程随 ctx 取消被杀；直播结束后 Manager 会重启子进程重拉。

## 验收

- [x] 三种来源归一化解析与 Manager 生命周期（含 race 检测）全绿：`go test ./internal/livechat/`
- [x] Twitch EventSub 全链路（假 Helix REST + 假 EventSub WS）测试
- [x] YouTube videos→liveChatId→messages 轮询全链路测试
- [x] 配置持久化跨重启自动拉起