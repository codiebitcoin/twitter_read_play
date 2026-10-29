# Codie 设备接口调用说明

> 当前播报用的是**组装模式（chat 模式）**：本服务把推文组装成一段完整的 `text` 发给设备，设备端走角色自带的人设（`character_prompt`，即下发角色包时写进去的 system prompt）+ LLM + TTS 管线朗读。没有单独的 system_prompt 接口 —— 人设靠 ① 的 `PUT character` 预先下发，内容靠 ② 的 `text` 一次性传入。

本服务通过 SDK（`third_party/workshop_inner_sdk/codie.go`，包 `clt01`）调用 Codie 设备的局域网 HTTP 接口（`http://设备IP:8081`）。

## 播报调用序列

对每个目标绑定（machine_id + slot），按顺序调用：

### ① 下发角色包（`ApplyRolePackage`，manager.go:570）

**读角色配置**

```
GET /v1/api/data/user/character?slot={n}
```

**写角色配置**（`PutCharacter`）

```
PUT /v1/api/data/user/character
```

```jsonc
{
  "slot_number": 1,              // 槽位号
  "name": "Elon",                // 角色名（Role.Name）
  "description": "...",          // 角色描述
  "character_prompt": "...",     // 人设 prompt（Role.Prompt）
  "voice_id": "xxx",             // 音色 ID
  "group_key": "xxx",            // 音色分组（克隆组）
  "tts_volume": 80               // TTS 音量
}
```

注意：请求体不允许带 cloth 相关字段（`selected_cloth` / `available_cloths` / `custom_cloths` / `link_other_original_cloth`），SDK 统一剥离，否则设备回 `OUTFIT_APPLY_REQUIRED`。

**完整示例**（假设角色 Elon Musk，槽位 1）：

```jsonc
PUT /v1/api/data/user/character
{
  "slot_number": 1,
  "name": "Elon",
  "description": "Tesla & SpaceX CEO",
  "character_prompt": "You are Elon Musk. Speak in first person, casual and witty.",  // ← 这就是设备端 LLM 的 system prompt
  "voice_id": "v_abc123",
  "group_key": "g_clone_01",
  "tts_volume": 80
}
```

**切换槽位**（`SwitchSlot`）

```
PUT /v1/api/data/user/slot
{ "slot_number": 1 }
```

### ② 播报（按角色 `SpeakMode` 二选一，默认 chat）

**chat 模式**（`ChatText`，走设备 LLM+TTS 管线）

```
POST /v1/api/data/user/chat/text
```

```jsonc
{
  "text": "...",        // 组装好的 prompt：SpeakPrompt + 最多 3 条上下文推文 + 目标推文
  "slot": 1,            // 目标槽位
  "language": "zh"      // 可选，角色语言
}
```

**完整示例**（播报推文 "Just landed 🚀 @ spacex https://t.co/xxx"，上下文还有 2 条推文）：

```jsonc
POST /v1/api/data/user/chat/text
{
  "slot": 1,
  "language": "en",
  "text": "Ignore all previous conversation context. \nHere are some of your recent posts for context:\n1. Starship test flight next week\n2. Mars city will happen\n\nNow randomly pick one of your posts (or read this one): Just landed 🚀 at SpaceX \nConvert it to natural spoken language (expand @mentions to spoken names, drop links), then read it aloud in your own voice, then add a short comment."
}
```

响应：`{ "chat_id": "..." }`（可用来取消）。

设备端实际朗读流程：设备把这段 `text` 交给当前槽位角色的 LLM（system prompt 就是 ① 里 `character_prompt`），LLM 生成口语化回复，再 TTS 用 `voice_id` 音色读出来。

`text` 的组装规则（manager.go:902-921）：

```
第 1 段  SpeakPrompt（角色设置了就用它；没设用固定句
         "Ignore all previous conversation context. "）
第 2 段  固定句 "Here are some of your recent posts for context:"
第 3 段  最多 3 条其他推文，编号列表 "1. ..."（排除目标推文本身，取 tw.Text 原文）
第 4 段  固定句 "Now randomly pick one of your posts (or read this one): {目标推文}
         Convert it to natural spoken language (expand @mentions to spoken names,
         drop links), then read it aloud in your own voice, then add a short comment."
```

- 目标推文用 `EffectiveText()`：用户编辑过用编辑文本，否则原文（超 300 字截断，`CleanText`）。
- 上下文推文用的是拉取接口返回的原始 `Text`。

**取消播报**（`CancelChat`）

```
POST /v1/api/data/user/chat/cancel
{ "chat_id": "..." }
```

**tts 模式**（`TTSAudition`，指定音色直读）

```
POST /v1/api/data/user/tts_audition
```

```jsonc
{
  "voice_id": "xxx",    // 角色绑定音色
  "text": "...",        // 清洗口语化后的推文文本（speakableText）
  "group_key": "xxx"    // 音色分组
}
```

### ③ 辅助：音色分组查询（`VoiceGroups`）

```
GET /v1/api/data/user/ability/group-keys
```

返回各分组的 `group_key`、可用 `voice_ids`、克隆 `voice_ids`，用于校验角色音色在目标设备上是否存在。

## 旧固件降级

- `chat/text` 返回 404（旧固件无此接口）→ 自动改用 `tts_audition` 直读（manager.go:923）。

## 播报接口速查表

| 方法 | 接口 | 关键参数 | 用途 |
|---|---|---|---|
| GET | `/v1/api/data/user/character?slot={n}` | slot | 读角色槽配置 |
| PUT | `/v1/api/data/user/character` | slot_number, name, character_prompt, voice_id, group_key, tts_volume | 下发角色包 |
| PUT | `/v1/api/data/user/slot` | slot_number | 切换激活槽位 |
| POST | `/v1/api/data/user/chat/text` | text, slot, language | chat 模式播报 |
| POST | `/v1/api/data/user/chat/cancel` | chat_id | 取消播报 |
| POST | `/v1/api/data/user/tts_audition` | voice_id, text, group_key | tts 模式播报 |
| GET | `/v1/api/data/user/ability/group-keys` | — | 音色分组 |
