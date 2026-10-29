package clt01

// mcp.go — MCP（Model Context Protocol）服务端模式，零外部依赖实现。
//
// 传输层（二选一，由启动命令决定）：
//   - stdio          ：每行一条 JSON-RPC 2.0 消息（stdin→stdout），供 Cursor / Claude Desktop
//                      等以子进程方式拉起（"command" 型 MCP 配置）。
//   - Streamable HTTP：POST 单端点、无状态（不维持 session），供远程/局域网复用
//                      （"url" 型 MCP 配置，或 mcp-proxy 转发）。
//
// 协议面只实现服务端必需的最小集：initialize / ping / tools/list / tools/call。
// 工具集见 mcp_tools.go（全部封装本 SDK 的配对与原子操作）。
//
// 用法：
//
//	srv := clt01.NewMCPServer("192.168.1.10:8081")
//	srv.ServeStdio(ctx)                 // stdio 模式
//	http.Handle("/mcp", srv.Handler())  // HTTP 模式

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// MCPProtocolVersion 服务端支持的协议版本（客户端请求其它版本时按规格回退）。
const MCPProtocolVersion = "2025-06-18"

// sdkVersion MCP serverInfo 上报的 SDK 版本。
const sdkVersion = "0.1.0"

// mcpSupportedProtocolVersions initialize 版本协商候选：客户端请求的版本在列表内
// 则原样回显，否则回退最新版（MCP spec: server replies a version it supports）。
var mcpSupportedProtocolVersions = []string{"2025-06-18", "2024-11-05"}

// MCPTool MCP 工具定义：Handler 返回任意可 JSON 序列化的结果（或 error）。
type MCPTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, args map[string]any) (any, error)
}

// MCPServer MCP 服务端。DefaultAddr 是未在参数里显式给 addr 时使用的设备地址
// （来自启动参数或 CLT01_ADDR 环境变量）。
type MCPServer struct {
	DefaultAddr string

	tools   map[string]MCPTool
	toolsMu sync.Mutex
}

// NewMCPServer 创建 MCP 服务端并注册内置工具（见 mcp_tools.go）。
func NewMCPServer(defaultAddr string) *MCPServer {
	s := &MCPServer{DefaultAddr: defaultAddr, tools: map[string]MCPTool{}}
	s.registerTools()
	return s
}

func (s *MCPServer) add(t MCPTool) {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	s.tools[t.Name] = t
}

// ---- JSON-RPC 编解码 ----

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // 通知（无 id）时为空
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	mcpErrParse     = -32700
	mcpErrInvalidRe = -32600
	mcpErrNoMethod  = -32601
	mcpErrBadParams = -32602
	mcpErrInternal  = -32603
)

// handle 处理一条 JSON-RPC 请求，返回要回发的响应字节；通知（无 id）返回 nil。
// 输入损坏返回 JSON-RPC parse error 响应（id 为 null）。
func (s *MCPServer) handle(ctx context.Context, line []byte) []byte {
	var req mcpRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil || req.Method == "" {
		return mcpResponseRaw(json.RawMessage("null"), nil, &mcpError{Code: mcpErrParse, Message: "parse error: invalid JSON-RPC message"})
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	if isNotification {
		// notifications/initialized 等通知：确认收到即可，不回包
		return nil
	}
	result, rpcErr := s.dispatch(ctx, req)
	return mcpResponseRaw(req.ID, result, rpcErr)
}

func (s *MCPServer) dispatch(ctx context.Context, req mcpRequest) (any, *mcpError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := MCPProtocolVersion
		for _, v := range mcpSupportedProtocolVersions {
			if p.ProtocolVersion == v {
				version = v
				break
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "clt01-mcp", "version": sdkVersion},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolList()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &mcpError{Code: mcpErrBadParams, Message: "bad params: " + err.Error()}
		}
		return s.callTool(ctx, p.Name, p.Arguments)
	default:
		return nil, &mcpError{Code: mcpErrNoMethod, Message: "method not found: " + req.Method}
	}
}

// callTool 执行工具。工具业务错误走 MCP 结果（isError=true），让模型看到错误文本；
// 协议层错误（未知工具）才用 JSON-RPC error。
func (s *MCPServer) callTool(ctx context.Context, name string, rawArgs json.RawMessage) (any, *mcpError) {
	s.toolsMu.Lock()
	t, ok := s.tools[name]
	s.toolsMu.Unlock()
	if !ok {
		return nil, &mcpError{Code: mcpErrBadParams, Message: "unknown tool: " + name}
	}
	args := map[string]any{}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return toolErrorResult(fmt.Sprintf("参数不是合法 JSON 对象: %v", err)), nil
		}
	}
	out, err := t.Handler(ctx, args)
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(b)}},
	}, nil
}

func toolErrorResult(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func (s *MCPServer) toolList() []map[string]any {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return out
}

func mcpResponseRaw(id json.RawMessage, result any, rpcErr *mcpError) []byte {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	b, _ := json.Marshal(resp)
	return b
}

// ---- stdio 传输 ----

// ServeStdio 以 stdio 方式服务 MCP（每行一条 JSON-RPC 消息；响应写 stdout，
// 日志一律走 stderr 以免污染协议通道）。stdin EOF 或 ctx 取消时返回。
func (s *MCPServer) ServeStdio(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := bufio.NewWriter(os.Stdout)
	done := make(chan struct{})
	go func() { // ctx 取消时关闭 stdin 解除 Read 阻塞
		select {
		case <-ctx.Done():
			_ = os.Stdin.Close()
		case <-done:
		}
	}()
	defer close(done)

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if resp := s.handle(ctx, line); resp != nil {
			if _, err := out.Write(append(resp, '\n')); err != nil {
				return err
			}
			if err := out.Flush(); err != nil {
				return err
			}
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return sc.Err()
	}
}

// ---- Streamable HTTP 传输 ----

// Handler 返回无状态的 Streamable HTTP 端点：POST 收 JSON-RPC、回 JSON
// （通知回 202 无体）。不维持 Mcp-Session-Id（无状态服务端允许省略）。
// GET/SSE 通道未实现（服务端无主动推送需求），按规格回 405。
func (s *MCPServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "MCP endpoint accepts POST only", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<24))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		resp := s.handle(r.Context(), body)
		if resp == nil { // 通知：无响应体
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	})
}