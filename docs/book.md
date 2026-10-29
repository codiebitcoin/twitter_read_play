# Book：上传图书 → 拆章节 → 点章 / 点段 / 随机朗读

角色详情 → **Book**（与 Feed / Stream 同级）。与社媒 Feed、直播 Stream 相互独立，
不依赖 twitter 上游（没配 socialdata key、没设用户名也能用）。

**书归属角色，角色之间不共享。** 一本上传的书只出现在上传它的角色的 Book 页；
程序不附带任何内置书，空数据目录下书库就是空的。

## 能力

| 需求 | 实现 |
|---|---|
| 上传图书（epub、txt 等主流格式） | `POST /api/roles/{id}/books`（multipart `file`）。支持 `.txt/.text`、`.md/.markdown`、`.epub`；编码自动识别 UTF-8(BOM)、UTF-16(LE/BE，含无 BOM)、GB18030/GBK（GB2312 子集包含在内）、Big5(HKSCS)，乱码还能按指定编码重新解码 |
| 自动拆分章节 | `internal/book/split.go`：英文 `Chapter 1 / CHAPTER IV / Part 2 / Book Three`、中文 `第X章 / 第X回 / 卷X / 一、`、`1.`、`§12`、罗马数字行、Markdown `#` 标题；识别不到就按 ~3000 字机械分章（不切断句子）；目录重复标题会被剔除；超长章再切成「（续 N）」保证单次下发不超设备等待上限 |
| 点章节播放 / 选章节 / 点段落播放 | 章节列表（可筛选、分页渲染，1900 章的书也不卡）→ 章尾/行内 ▶ 播放整章；右侧正文逐段点播（▶ 或点正文） |
| 随机播放 | `POST /api/roles/{id}/book/random`：章少段多（小说）按「章 × 未播段」随机且一轮不重复；章多段少（圣经）按章加权随机（没随机播过的章 3 倍权重） |
| （顺带）断点续读 | 进度按「角色 × 书」存 SQLite（`book_progress`），重启/换页不丢；章内按段推进，中途停止重播从断点继续 |

## 朗读方式

- **按文字念（默认）**：≥1.5.3 固件走 `tts/utterances`（设备直接读文字，不过 LLM），
  <1.5.3 走 `tts_audition`。适合长篇逐字朗读。
- **经设备 LLM**：走 `chat/turns`（角色 LLM + TTS），会按角色 prompt 的口吻念；
  适合短篇/台词。设备受理后没真正起播会报错并提示改用上一种（实测设备偶尔接受后立即闲置）。
- 可选「先念章节标题」：英文 `Genesis.`、中文 `《第一章 风起》。`
- 段落按句末标点切成 ≤240 字符的朗读段，段与段之间等设备真正播完再下发（同一台设备串行）。
- 若角色在 Persona 配了播报模板（`SpeakTemplate`），书的正文会代入 `{{TWEET}}`/`{PARA}` 位置作包装。

## API

所有图书操作都挂在角色下，服务端校验 `Book.RoleID == {id}`；越权访问返回 404。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/roles/{id}/books` | 该角色的书库（不含正文） |
| POST | `/api/roles/{id}/books` | 上传（multipart `file`，可选 `title`）；自动归属该角色 |
| POST | `/api/roles/{id}/books/claim` | `{book_id}`：认领一本未归属的旧书 |
| GET | `/api/roles/{id}/books/{book_id}` | 单本详情（章节索引：标题/字数/段数） |
| DELETE | `/api/roles/{id}/books/{book_id}` | 删除书（索引 + 文件 + 进度） |
| POST | `/api/roles/{id}/books/{book_id}/redecode` | `{encoding}`：`auto/utf-8/gb18030/big5/utf-16le/utf-16be` 重新解码 + 重新拆章 |
| GET | `/api/roles/{id}/books/{book_id}/chapters/{idx}` | 章节正文（段落数组；`?paragraph=N` 附该段全文） |
| GET | `/api/books/state?role_id=` | 该角色书库 + 进度 + **本角色**播放状态 + 未归属旧书 |
| POST | `/api/roles/{id}/book/play` | `{book_id, chapter, paragraph?, mode?, preface?}`：整章 / 单段 / 单段正文 |
| POST | `/api/roles/{id}/book/random` | `{book_id, mode?, preface?}`：随机一段 |
| POST | `/api/roles/{id}/book/stop` | `{machine_id?}`：停止本角色在该机器的朗读 |
| POST | `/api/roles/{id}/book/progress` | `{book_id, chapter, paragraph, reset?}`：设置断点 / 从头 |

播放状态（`PlayRun`）带 `role_id`：Book 页只展示、停止本角色发起的朗读，别的角色在同一台设备上播书不会串到当前页面。

## 存储

```
data/books/{book_id}/original.txt|md|epub   原件（重新解码/改进拆分用）
data/books/{book_id}/ch-0001.txt …          每章正文（段落间空行）
data/trp.db  blobs.books        书索引（role_id/标题/作者/格式/编码/章节列表）
data/trp.db  blobs.book_progress 角色 × 书 进度（断点、随机去重表）
```

正文不进 JSON blob：朗读时按章读文件，内存与书大小无关。

## 升级迁移（旧数据）

旧版本的书库是全局的，`Book` 没有 `role_id`。启动时 `book.Library.MigrateOwners` 给这些书补归属：

1. 存在 twitter 账号 `elonmusk` 的角色时，旧书统一归给它；
2. 否则按朗读进度归属（多个角色有进度时取最近播放的）；
3. 都没有进度且只有一个角色时归它；多角色则保持未归属。

迁移**只写归属，不删除任何书**。未归属的书会在各角色的 Book 页下方显示「未归属的旧书」，
点一下即可认领到当前角色；认领后其他角色不再可见。删除角色会级联删除它名下的书与进度。

## 预置角色（哈梅内伊 / 弥勒佛）

`resources/import-roles.json` + `cmd/importrole` 一次性导入「形象 + 声音样本 + prompt」，
导入后按常规流程在 Status 页 sync / voice_clone。详见 [resources/README-roles.md](../resources/README-roles.md)。

- 伊朗教法学家形象：Sketchfab `Seyyed Ali Khamenei`（CC BY）；波斯语声音样本取自哈梅内伊 2026-01-09 讲话。
- 弥勒形象：Sketchfab `Laughing Buddha`（truekit）→ 资源服务自动封装 VRM；中文声音样本取自体佛法师讲经（20–60 秒）。
- 角色模板用 `{TITLE}`（书目 · 第 N 章）/ `{PARA}`（正文）——书朗读时会自动替换。

## 克莱恩「愚者」角色（parody 模式）

`resources/import-role-klein.json`：形象借弥勒佛的 VRM、声音借弥勒的克隆（voice 24，slot 4），
prompt / 朗读模板 / parody prompt 按「愚者」的人设写。导入：

```bash
./importrole -data ./data -spec ./resources/import-role-klein.json
```

parody prompt（`parody_prompt` 字段）人设要点（据百度百科「克莱恩·莫雷蒂」「愚者」词条整理）：

- **来历**：周明瑞穿越附身廷根市霍伊大学历史系毕业生克莱恩·莫雷蒂，值夜者出身，误入灰雾之上后建立塔罗会，自称「愚者」；途径：占卜家→小丑→魔术师→无面人→秘偶大师→诡法师→古代学者→奇迹师→诡秘侍者→愚者（序列0）。
- **性格**：外表高深莫测，内里谨慎（「从心之人」「克怂」）、爱吐槽、腹黑；「多做主动的表演，但不做无准备的表演」；解决不了的危险就举报给官方教会；乐善好施、同情弱小；精打细算（便士、金镑、报销）；美食与猫。
- **口吻**：「这是命运的愚弄」「祝你好运」「善良的人总是会得到回报的」「唯一的好消息是……」。

给设备朗读时的效果（金刚经第一分）：
「《法会因由分第一》。以「愚者」的口吻、用中文诵读下面的内容：语气平静从容，带一点古老神灵的庄重，像在灰雾之上对塔罗会的成员说话……如是我闻：……」

注意：示例里把 `parody_interval` 设成了 600 秒（测试用，避免自动播报刷设备）；演示完记得调回 0（只手动）或关掉 parody 开关。

## 线上导入（远程模式，不碰数据库）

服务跑在 K8s 里、数据在 PVC 上，进程外改库要 exec 进 Pod；`importrole -remote` 走 API 更安全：

```bash
./importrole -remote https://<站点> -user <用户名> -pass <密码> \
  -spec ./resources/import-roles.json          # 默认不应用 bindings（绑定另做）；-bind 才应用
```

- 角色：GET 快照 → 覆盖 spec 字段 → PUT（没有则 POST）
- 形象 / 语音样本：`POST /api/roles/{id}/vrm`、`POST /api/roles/{id}/clone?upload_only=1`（只留样本，不触发克隆）
- 大文件（> ~20 MB 的 VRM）：公网 nginx（ingress）限制请求体大小会 413，改走
  `kubectl port-forward` 直连 Pod 再导入，例如
  `kubectl -n <namespace> port-forward pod/<pod> 18080:8080` 后 `-remote http://127.0.0.1:18080`。

## 已知边界 / 后续

- 只抽文字：EPUB 里的图片、脚注、表格样式不处理；DRM 加密的 EPUB 解析会报「no readable text」。
- `mobi/azw3/pdf` 未支持（需要额外解析器），可先转成 EPUB/TXT 再上传。
- 随机播放起始只播一段/一章开头；连续听整本请用章节列表的 ▶ 或「从第 N 章 ▶」（断点续读）。
- 角色 Persona 里的 `SpeakRegex` 替换不作用于书正文（书不适合套推文的正则规则），只套模板。
- 书不跨角色共享：同一本书想在两个角色上用，需要各上传一次（各自独立进度）。
