# workshop_inner_sdk (clt01)

Go SDK for LAN access to CLT01 devices (`clt01_embed_backend`): functional API calls, a fully wrapped pairing flow, credential management, and an embedded reverse proxy for the device web console (including the `/config/pair` authorization page).

> Module name: `workshop_inner_sdk` (the former `workshop_api` has been merged into this repo and is maintained as inlined source).
> **Supports firmware 1.5.3+ only.** All interfaces use the latest versions by default; tests live in the `test/` directory.

## Installation

The SDK is maintained as inlined source inside the main repo (`third_party/workshop_inner_sdk`) and referenced through the main repo's `go.mod`:

```
replace workshop_inner_sdk => ./third_party/workshop_inner_sdk
```

No separate installation is needed.

## Quick start

### 1. Functional pairing (enter IP → enter the 6-digit pairing code; the full flow runs automatically)

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

    // Runs in the background automatically: ping → bootstrap → pair/request
    //   (device pops up the pairing window) → StdinCodeProvider waits for the
    //   user to enter the 6-digit code → pair/confirm exchanges it for credentials
    creds, err := c.Pair(ctx, clt01.StdinCodeProvider{}, clt01.AuthScopePermanent)
    if err != nil {
        panic(err)
    }

    store, _ := clt01.NewStore() // defaults to ~/.clt01sdk/credentials.json
    _ = store.Save(creds)

    fmt.Println("paired:", creds.DeviceID, creds.UserID)
}
```

GUI programs can supply their own pairing-code collector:

```go
creds, err := c.Pair(ctx, clt01.CodeFunc(func(ctx context.Context, challengeID string) (string, error) {
    return myDialog.AskPairingCode(challengeID) // returns the user-entered 6-digit code
}), clt01.AuthScopeSession) // session = 12-hour short-lived credentials; permanent = long-lived
```

### 2. Calling an already-paired device (credential reuse)

```go
c, err := clt01.ClientFromStore("192.168.1.10:8081")
info, err := c.SystemInfo()
fmt.Println(info.Username, info.Capabilities)

// Generic signed request
data, err := c.Do("GET", "/v1/api/data/user/chat/history/dates", nil, "")
data, err = c.Do("POST", "/v1/api/data/user/chat/text", []byte(`{"text":"hello"}`), clt01.PayloadModeJSON)
```

For the full endpoint list see `clt01_embed_backend/docs/web/LOCAL_API_REFERENCE.md`.

### 3. Embedding the device authorization page (/config/pair)

```go
mux := http.NewServeMux()
mux.Handle("/", c.ConsoleProxy())          // reverse proxy for the device console
http.ListenAndServe("127.0.0.1:8090", mux)
// Open http://127.0.0.1:8090/config/pair in a browser / WebView
```

Or hand the page URL straight to a WebView: `c.ConsoleURL("/config/pair")`.
Pairing and signing inside the page are handled by the device's own frontend; credentials are stored in the browser's localStorage, independent of SDK credentials.

## API overview

| Symbol | Description |
|---|---|
| `New(addr) (*Client, error)` | Create a client (`addr` may or may not include `http://`) |
| `Client.Pair(ctx, CodeProvider, scope) (*Credentials, error)` | Full pairing flow (chains all background calls automatically) |
| `Client.RequestPairing / ConfirmPairing / CancelPairing` | The three pairing steps, usable individually for custom orchestration |
| `Client.Ping / Bootstrap` | Unauthenticated probe and bootstrap info |
| `Client.SystemInfo()` | Signed system-info query |
| `Client.Do / GetJSON / PostJSON / GetRaw` | Generic signed requests returning the unified envelope `data` |
| `Client.Apply(*Credentials)` | Apply credentials |
| `Client.ConsoleProxy() / ConsoleURL(path)` | Embedded console reverse proxy / page URL |
| `Store` (`NewStore / Save / Load / Delete`) | Credential persistence (0600, default `~/.clt01sdk/credentials.json`) |
| `ClientFromStore(addr)` | Build an authorized client directly from the credential store |
| `StdinCodeProvider / CodeFunc` | Pairing-code collector implementations |
| `MCPServer` (`NewMCPServer / ServeStdio / Handler`) | MCP server mode: stdio / Streamable HTTP (see "MCP mode") |
| `Sign(secret, method, path, ts, nonce, payloadMode, bodyHash, userID)` | The signing algorithm itself, exported |
| `Credentials.SessionExpired()` | Expiry check for session credentials |

Errors are uniformly `*APIError` (`StatusCode` / `Code` / `Message`); branch on `Code` (e.g. `LOCAL_SESSION_EXPIRED`, `PAIRING_REQUEST_RATE_LIMITED`) as needed.

## Tests

All tests live in the `test/` directory (external test package `clt01_test`):

```bash
# Unit tests (no device required)
go test ./test/

# Online smoke tests (against a real device, skipped by default)
CLT01_LIVE_ADDR=192.168.1.10:8081 \
CLT01_LIVE_SECRET=<local_secret> CLT01_LIVE_SESSION=<session_id> CLT01_LIVE_USER=<user_id> \
go test ./test/ -run TestLive -v
```

Online cases cover: ping/bootstrap, signed requests, bad-signature rejection (`LOCAL_SECRET_MISMATCH`), and the pair request→cancel chain.

## CLI example

```bash
go run ./cmd/clt01-example pair 192.168.1.10:8081             # interactive pairing, saves credentials
go run ./cmd/clt01-example info 192.168.1.10:8081             # query the device with stored credentials
go run ./cmd/clt01-example console 192.168.1.10:8081 :8090    # embedded console proxy
```

## MCP mode (Model Context Protocol server)

The SDK ships with a built-in MCP server (zero external dependencies, hand-written JSON-RPC 2.0) that exposes pairing / character config / playback as MCP tools to model clients such as Cursor or Claude Desktop — control the device through conversation.

### Tool list

| Tool | Description |
|---|---|
| `clt01_ping` | Check device reachability (unauthenticated ping + bootstrap) |
| `clt01_pair_start` | Ask the device to pop up the 6-digit pairing-code window (valid 5 minutes) |
| `clt01_pair_confirm` | Complete pairing with the code and save credentials (scope=session/permanent) |
| `clt01_pair_cancel` / `clt01_pair_status` | Cancel a pairing challenge / query local credential status |
| `clt01_system_info` | Read system info (requires pairing) |
| `clt01_character_get / set / list` | Read / atomically set (SetInfo semantics) / list character slot config |
| `clt01_voice_groups` | Voice groups and their voice_id lists |
| `clt01_play_chat` | Speak text (chat/turns channel, returns chat_id) |
| `clt01_play_state` / `clt01_play_cancel` | Playback status query / cancel playback |
| `clt01_tts_say` | Direct TTS (bypasses the character LLM pipeline) |
| `clt01_raw_request` | Generic signed-request escape hatch (any LOCAL API) |

Every tool accepts an optional `addr` parameter to override the default device address; signed calls reuse `~/.clt01sdk/credentials.json`.

### Running

```bash
# Build
go build -o ./clt01-mcp ./cmd/clt01-mcp

# Pair first (signed tools need credentials), or do pair_start → pair_confirm inside the MCP conversation
go run ./cmd/clt01-example pair 192.168.1.10:8081

# Option 1: stdio subprocess mode (Cursor / Claude Desktop, recommended)
./clt01-mcp stdio 192.168.1.10:8081

# Option 2: Streamable HTTP mode (shared across LAN/remote, endpoint /mcp, listens on :8091 by default)
./clt01-mcp http 192.168.1.10:8081 :8091
```

The device address can also come from the `CLT01_ADDR` environment variable; in stdio mode protocol responses go to stdout and logs to stderr.

### Cursor integration (stdio)

Project `.cursor/mcp.json` or global `~/.cursor/mcp.json`:

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

Cursor launches the subprocess automatically once saved. For HTTP mode, add a "url"-type MCP entry pointing at `http://127.0.0.1:8091/mcp` (stateless implementation, no session/token needed). Once connected you can say things like "have the device announce X", "change slot 1's prompt", or "list the voices", and the model picks the right tool.

## Atomic semantic operations (recommended entry points)

```go
c.SetModel(slot, cloth, filename, data, nil)          // set model (gzip chunked upload)
c.ApplyModel(slot, cloth, filename, nil)              // apply an already-uploaded model
c.SetInfo(slot, &clt01.CharacterSettings{Name: "…"})  // set character info
c.SetCloneVoice(audio, "a.wav", &clt01.CloneVoiceOptions{GroupKey: "g"}) // set cloned voice
res, _ := c.PlayChat(text, &clt01.PlayChatOptions{    // playback
    Slot: 1, Language: "zh", VoiceID: "v", GroupKey: "g"})
// res.Channel: "chat-turns" (/data/user/chat/turns)
```

## Firmware versions (1.5.3+ only)

This SDK supports firmware 1.5.3 and above only, and **all interfaces default to the latest version**:
uploads use the gzip chunked channel (job[transport=gzip] → chunk/:seq → complete, with an 8MiB streaming hard limit),
applying uses outfit/apply (falling back to the model-apply trio when early 1.5.3 builds return 404),
and disk verification treats uploaded-models as authoritative. Compatibility channels for older firmware (1.5.2 / legacy) have been removed.
If probing fails the SDK still assumes 1.5.3; `Firmware()` is for logs and debugging only:

```go
fw, _ := c.Firmware()                  // probe and cache (version / drafts / uploaded-models)
c.ForceFirmware(clt01.VerChunkedUpload) // force a version for debugging
c.UploadVRM(slot, cloth, filename, data, nil)   // upload + apply + disk verification
c.VerifyVRMPersisted(slot, cloth)               // guard against fake SUCCESS
```

## Signature algorithm (matches the device side)

`sig = hex(HMAC-SHA256(secret, "METHOD|path|timestamp|nonce|payloadMode|bodyHash|userID"))`

Headers: `X-Local-Sig` / `X-Local-Nonce` / `X-Local-Timestamp` / `X-Local-Payload-Mode` / `X-Local-Body-Hash` / `X-Local-User-Id` / `X-Local-Session-Id` (session mode). The SDK handles all of this automatically.
