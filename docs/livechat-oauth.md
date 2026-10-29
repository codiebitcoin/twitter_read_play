# 直播弹幕：Google / Twitch 弹窗登录拿 Token

页面路径：**直播**（`/live`）。三种来源都支持，可在页面填写并切换。

申请步骤的**正文不写在前端 JSX**：每种来源一份独立 HTML（`internal/guides/html/<kind>.<lang>.html`），
登记在 SQLite 表 `provider_guides`，由 `GET /api/providers/{kind}/guide` 提供。
直播页「配置教程」按钮打开页内弹层。加一篇教程 = 丢 HTML + 登记一行，不用改前端。

| 来源 | 登录 | 教程 kind |
|---|---|---|
| Twitch EventSub | **用 Twitch 登录** 弹窗 | `twitch` / `twitch_eventsub` |
| YouTube Live API | **用 Google 登录** 弹窗 | `google` / `youtube_api` |
| chat-downloader | 不需要 | `chat_downloader` |

**不要用账号密码让程序代登。** Twitch / Google 都只走官方 OAuth 授权码，弹窗里由你本人点允许。密码不会进本服务。

---

## 数据库怎么存（对齐 codie_life_control）

`data/trp.db`：

```sql
CREATE TABLE IF NOT EXISTS provider_guides (
  kind TEXT PRIMARY KEY,   -- google / twitch / chat_downloader / youtube_api / twitch_eventsub
  file TEXT NOT NULL,      -- html 基名，如 google → html/google.zh.html
  updated_at INTEGER NOT NULL
);
```

启动时 `guides.Seed` upsert 打包清单。OAuth 凭证和 token 另存在 blobs `livechat_oauth.json`（脱敏后才回给前端）。

`{{BASE}}` 由前端把当前 origin 传给 `?base=`，弹层里的回调地址跟地址栏一致。

---

## 一、Google 登录（YouTube）——本次实操记录

项目：自建 Google Cloud 项目（ID 请填你自己项目）。登录账号：你自己的 Google 账号。
应用名称 **Codie Live Chat**，用户类型外部，发布状态测试。

1. [Google Cloud Console](https://console.cloud.google.com/) 用你自己的 Google 账号登录（不要把密码交给本服务）。
2. 选中你自己的项目。
3. **API 和服务 → 库** → 启用 **YouTube Data API v3**（通知里会出现 `youtube.googleapis.com`）。
4. **Google Auth Platform → 品牌**：应用名 `Codie Live Chat`；外部；测试；联系邮箱用登录账号。
5. **用户端 → Web application**：
   - JavaScript 来源：`http://127.0.0.1:8088`（跟地址栏 origin 一致）
   - 重定向 URI：`http://127.0.0.1:8088/api/livechat/oauth/google/callback`
   - Client ID / Secret 只在创建弹窗出现一次，立刻填到直播页，不要提交 git。
6. **资料存取权** 勾选：
   - `https://www.googleapis.com/auth/youtube.readonly`
   - `https://www.googleapis.com/auth/userinfo.email`
   点更新再 Save。
7. **目标对象** 保持测试中；把要点「允许」的账号加进测试用户（品牌联系邮箱往往已经在列表里）。
8. 直播页 → **用 Google 登录** → 选账号 → 允许。成功显示 `✓ 邮箱`。
9. 之后新建 `youtube_api` 源可留空 token / API Key，自动带上这个账号。

新控制台路径是 Auth Platform 的 **品牌 / 资料存取权 / 目标对象 / 用户端**，不再走旧的「OAuth 同意屏幕」向导。

### 本机换 token 必须走代理

Go 进程访问 `oauth2.googleapis.com` / `youtube.googleapis.com` 不走 macOS 系统代理。
本机常见代理是 `127.0.0.1:7892`。启动时带：

```bash
HTTPS_PROXY=http://127.0.0.1:7892 HTTP_PROXY=http://127.0.0.1:7892 \
  AUTH_USER=admin AUTH_PASS=11111 ./trp -addr 127.0.0.1:8088 -data ./data
```

浏览器能打开 Google **不等于** 服务端能换 token。没带代理时回调会报 `context deadline exceeded`。

外部 + 测试：refresh token 大约 7 天过期，需要重新登录。Token 在 blobs `livechat_oauth.json`，不要把 data 目录提交到 git。

只读弹幕也可以只用 API Key（凭据 → API 密钥），不登录；配额默认 1 万单位/天。

---

## 二、Twitch 登录（EventSub）

1. 打开 [Twitch 开发者控制台](https://dev.twitch.tv/console/apps) → **Register Your Application**。
2. Name 随意；OAuth Redirect URLs 填页面上显示的地址，本地一般是：
   ```
   http://127.0.0.1:8088/api/livechat/oauth/twitch/callback
   ```
   Category 选 Application Integration。
3. 创建后打开应用，复制 **Client ID**，点 New Secret 生成 **Client Secret**。
4. 回到本站 **直播** 页：粘贴后点 **用 Twitch 登录**。弹窗申请 `user:read:chat`。
5. 成功后显示 Twitch login 名。新建 `twitch_eventsub` 源可把 Client ID / Token 留空。

EventSub 读别人频道的弹幕：登录账号必须能进那个频道的聊天（公开频道即可）。`channel` 填对方的 login 名（不是显示名）。

---

## 三、配置 Subscribe（Twitch EventSub）

新建 / 编辑 `twitch_eventsub` 源时，可勾选要订阅的事件（默认只勾弹幕）：

| 类型 | 含义 |
|---|---|
| `channel.chat.message` | 普通弹幕（必选建议） |
| `channel.chat.notification` | 订阅/raid/公告等通知 |
| `channel.cheer` | Bits / Cheer |
| `channel.subscribe` | 新订阅 |
| `channel.subscription.gift` | 礼物订阅 |
| `channel.subscription.message` | 订阅留言 |

保存后服务按勾选项分别创建 EventSub 订阅；改勾选会重启该路。

---

## 四、建一路弹幕源

1. 点 **新建弹幕源**，选来源（旁侧「配置教程」跟 kind 走）。
2. **频道 / 链接**：
   - Twitch：`somechannel`（login 名）
   - YouTube：直播 URL 或 11 位 video id
   - chat-downloader：任意支持的直播/回放 URL
3. Token / Key 留空则用顶部已登录账号；需要覆盖时再手填。
4. 勾选 **启用** → 保存。列表里可启动 / 停止，点卡片看实时弹幕。

---

## 五、本地联调注意

- 回调必须和控制台里的 Redirect URI **逐字一致**（含 `http`/`https`、端口、路径）。Web 客户端还要填 JavaScript 来源。
- 弹窗被拦：浏览器地址栏右侧允许本站弹出窗口。
- Google 测试应用未过审时，只有「测试用户」列表里的账号能登录。
- Token 存在本机 SQLite（`livechat_oauth.json` blob），不要把 data 目录提交到 git。
- 退出登录只清 token，不删 Client ID / Secret。
