// 命令 clt01-mcp 把 workshop_inner_sdk 以 MCP（Model Context Protocol）服务端方式暴露，
// 供 Cursor / Claude Desktop 等模型客户端调用设备能力。
//
// 两种传输模式：
//
//	clt01-mcp stdio 192.168.1.10:8081            # stdio 子进程模式（Cursor/Claude Desktop 配置 "command" 用）
//	clt01-mcp http 192.168.1.10:8081 :8091       # Streamable HTTP 模式（远程/局域网复用，配置 "url" 用）
//
// 默认设备地址也可用环境变量 CLT01_ADDR 指定；凭据复用 ~/.clt01sdk/credentials.json
// （先用 `go run ./cmd/clt01-example pair <addr>` 或 MCP 的 pair_start/pair_confirm 完成配对）。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	clt01 "workshop_inner_sdk"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	mode := os.Args[1]
	addr := os.Getenv("CLT01_ADDR")
	if len(os.Args) > 2 {
		addr = os.Args[2]
	}

	switch mode {
	case "stdio":
		if addr == "" {
			log.Fatal("stdio 模式需要设备地址：clt01-mcp stdio <host:port>（或环境变量 CLT01_ADDR）")
		}
		srv := clt01.NewMCPServer(addr)
		fmt.Fprintf(os.Stderr, "[clt01-mcp] stdio 模式已启动，默认设备 %s（协议日志走 stderr，勿写 stdout）\n", addr)
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := srv.ServeStdio(ctx); err != nil {
			log.Fatal(err)
		}

	case "http":
		listen := ":8091"
		if len(os.Args) > 3 {
			listen = os.Args[3]
		}
		if addr == "" {
			log.Fatal("http 模式需要设备地址：clt01-mcp http <host:port> [listen]（或环境变量 CLT01_ADDR）")
		}
		srv := clt01.NewMCPServer(addr)
		mux := http.NewServeMux()
		mux.Handle("/mcp", srv.Handler())
		s := &http.Server{Addr: listen, Handler: mux}
		go func() {
			fmt.Printf("[clt01-mcp] Streamable HTTP 已启动: http://127.0.0.1%s/mcp（设备 %s）\n", listen, addr)
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal(err)
			}
		}()
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		_ = s.Close()

	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `用法: clt01-mcp <mode> <device_addr> [listen_addr]
  stdio <host:port>           MCP stdio 模式（Cursor/Claude Desktop 以子进程拉起）
  http  <host:port> [listen]  MCP Streamable HTTP 模式（默认监听 :8091，端点 /mcp）

环境变量: CLT01_ADDR 默认设备地址；凭据库 ~/.clt01sdk/credentials.json`)
}