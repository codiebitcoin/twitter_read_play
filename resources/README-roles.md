# resources/import-roles.json —— 预置角色包

两个新角色（都是「试音通过后」再上线，试音本身会调设备，不要提交）：

| id | 角色 | 形象 | 声音样本 | 语言 | 绑定 |
|---|---|---|---|---|---|
| `khamenei` | Ayatollah Ali Khamenei（伊朗最高领袖 / 教法学家） | Sketchfab「Seyyed Ali Khamenei」(BakhtiarSource, CC BY)，资源服务还原的 VRM | bilibili 哈梅内伊 2026-01-09 讲话（波斯语原声）第 20–60 秒 | fa | codie-black-codiebitcoin-dev slot 2，克隆 voice 21 |
| `maitreya` | Maitreya 弥勒佛 | Sketchfab「Laughing Buddha」(truekit) → VRM | bilibili 体佛法师讲经（中文）第 20–60 秒 | zh | codie-black-codiebitcoin-dev slot 3，克隆 voice 22 |

导入（服务进程**停掉**时跑，避免两处同时写库）：

```bash
go build -o importrole ./cmd/importrole
./importrole -data ./data -spec ./resources/import-roles.json -dry-run   # 先看
./importrole -data ./data -spec ./resources/import-roles.json
```

角色朗读模板（SpeakTemplate）里用 `{TITLE}` 报「书目 · 第 N 章」、`{PARA}` 放正文——
书朗读（Feed → book）会自动把这两个占位换成实际内容。

## 音源延伸（NAS，尚未接进设备）

自建资源服务上的 mp3 库：

- `mp3/Ayatollah Khamenei/`：哈梅内伊讲话、德黑兰演讲、霍梅尼讲话（伊朗教法学家）
- `mp3/Maitreya/`：体佛法师《佛说阿弥陀经》讲解、南无弥勒佛圣号

设备只能放板载 TF 卡的音乐（`/api/data/user/music/*`，需先在设备上打开音乐页），需要单独做
「上传到设备 → 选曲播放」链路；见 docs/book.md 的「后续」一节。
