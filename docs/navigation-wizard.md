# 首页导航页 + 添加向导演示

> 对应实现：`front/src/components/HomePage.jsx`、`front/src/components/wizard/SetupWizard.jsx`、
> `internal/api/setup.go`、`internal/machine/discover.go`。

## 1. 首页（导航页）

- 路由：`/`（顶部导航第一项「首页」）。未知路径回落到首页；`/roles`、`/machines` 等原页面不变。
- 首屏一次拉全：`GET /api/setup/status`、`GET /api/machines`、`GET /api/roles`、
  `GET /api/upstreams`、`GET /api/logs`、`GET /api/settings`。
- 内容：
  - 配置状态徽标 + 「＋ 添加」；
  - 未配齐时的大卡「开始使用：把设备和角色连起来」；
  - 统计条：设备 / 角色 / 有效绑定；
  - 快速导航：机器、角色、设置、日志；
  - 设备列表（配对状态、地址、固件）与角色列表（平台/用户名、音色/VRM、绑定槽位、一键同步）；
  - 最近动态（播报 / 同步 / 校验日志，最近 5 条）；
  - 有未配对设备时显示黄色警告条并直达该设备的配对入口。

### `GET /api/setup/status`

```json
{
  "machines": 1,
  "paired_machines": 0,
  "roles": 2,
  "bound_roles": 1,
  "complete": true,
  "llm_ready": false,
  "socialdata_ready": false
}
```

- `complete` = 有设备 + 有至少一个绑定到现存设备的启用绑定（只表示「配置链路存在」；
  配对/在线状态由首页警告条单独提示）。
- 判定放在后端，避免首页串行请求后自己算一套口径。
- 所有 API 响应统一带 `Cache-Control: no-store`，避免向导保存后首页读到浏览器缓存。

## 2. 添加向导（弹窗）

左侧步骤条 + 一步一件事 + 可回退；顺序与参考项目 `codie_life_control` 的
`SetupWizard` 一致，只是把「账号（Account）」换成「设置角色（Role）」：

```
选择设备 → 设置角色 → 绑定槽位 → 同步角色信息 → 完成
```

| 步骤 | 做什么 | 复用的接口 |
|---|---|---|
| 1 选择设备 | 选已有机器 / 新增（名称、地址、授权类型）；**扫描局域网**自动发现地址；内联配对（start → 设备弹码 → confirm） | `GET/POST /api/machines`、`POST /api/machines/{id}/pair/*`、`GET /api/machines/discover` |
| 2 设置角色 | 选已有角色（重新拉详情「同步角色信息」）或新建；编辑名称/描述/平台/用户名 | `GET /api/roles`、`GET /api/roles/{id}`、`POST /api/roles`、`PUT /api/roles/{id}` |
| 3 绑定槽位 | 选 slot 1–5；已占用槽位禁用（同机同槽位只能一个角色） | `PUT /api/roles/{id}`（`bindings`） |
| 4 同步角色信息 | 基础信息下发（名称/描述/人设/音量/形象性别/动作强度）；可选完整同步（音色克隆 + VRM） | `POST /api/roles/{id}/apply`、`POST /api/roles/{id}/sync` |
| 5 完成 | 汇总设备 × 角色 × 槽位 × 同步状态；可「再添加一个」或打开角色页 | — |

### 同步语义

- **同步角色信息（默认）**：`/apply` 只 PATCH 基础角色配置，秒级返回；向导随后轮询
  `GET /api/logs?role_id=…&kind=sync` 等设备回执（最多 20s），把 `config patch applied`
  或失败原因展示出来。设备未配对/离线会明确报错，可「稍后再同步」。
- **完整同步（音色/形象）**：`/sync` 会先确保音色可用（没有音色样本时按钮禁用；克隆是分钟级）
  再上传 VRM。同步请求可能较久，向导会提示不要关闭窗口。
- 绑定写入机器时，若该机器的「本机启用角色」白名单非空，会自动把角色加进白名单；
  白名单为空（= 全部启用）时不做写入，避免把白名单收窄成只剩当前角色。

## 3. 局域网扫描

### 接口

```
GET /api/machines/discover[?port=8081]
```

响应：本机 IPv4、实际扫描的 CIDR、响应设备（`ip/addr/rtt_ms`）、扫描地址数、耗时。

### 范围与限制

- 对每个非 loopback IPv4 网卡：
  - `/24`：扫 1–254；
  - `/22`–`/23`：保留真实前缀（办公 VLAN 常见 /23，一刀切 /24 会漏设备）；
  - 比 `/22` 更宽：裁到接口所在的 `/24`（避免 /16 产生 6 万次拨号）；
  - `/31`、`/32`：跳过。
- 仅探测 TCP 端口（默认 `machine.DefaultPort` = 8081），因为固件不广播 mDNS 服务记录。
- 并发 128，单地址连接超时 300ms，整个请求 8s 截止；空网段也会在几秒内返回。
- 扫描的是**服务进程所在主机**的网段：桌面模式（本机窗口）最准；Docker/K8s 里看到的是
  容器/Pod 网段，通常扫不到物理局域网设备。

## 4. i18n

新增文案全部在 `front/src/i18n.js`（en / ja / zh），前缀 `home*`、`wiz*`、`pair*`。
默认语言沿用现有逻辑：localStorage `lang` > 浏览器语言（中文 → zh，其余 → en），可在右上角切换。

## 5. 测试

```bash
# Go（需要 go.mod 要求的 1.27.1；本机装了 1.25 时用 GOTOOLCHAIN=go1.27.1）
go test ./internal/api/... ./internal/machine/...

# 前端
cd front && npm run build
```

覆盖：`internal/machine/discover_test.go`（网段裁剪、网络号/广播地址、IP 排序）、
`internal/api/setup_test.go`（空库 / 仅设备 / 绑定 / 停用绑定 / 孤儿绑定 / LLM 与 Key 状态）。
