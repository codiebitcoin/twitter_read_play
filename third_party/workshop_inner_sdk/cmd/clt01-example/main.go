// 命令 main 演示 SDK 的两种用法：
//
//  1. 函数式配对：输入设备 IP → 请求配对 → 终端输入 6 位配对码 → 拿到凭据并保存
//  2. 内嵌设备授权页面：起一个本地反向代理，浏览器/WebView 打开 /config/pair
//
// 用法：
//
//	go run ./cmd/clt01-example pair 192.168.1.10:8081            # 完整配对流程
//	go run ./cmd/clt01-example info 192.168.1.10:8081           # 用已存凭据查系统信息
//	go run ./cmd/clt01-example console 192.168.1.10:8081 :8090  # 内嵌控制台代理
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	clt01 "workshop_inner_sdk"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "用法: clt01-example pair|info|console <device_addr> [listen_addr]")
		os.Exit(2)
	}
	cmd, addr := os.Args[1], os.Args[2]

	switch cmd {
	case "pairwait":
		codeFile := os.Args[3]
		timeout := 5 * time.Minute
		if len(os.Args) > 4 {
			if n, err := time.ParseDuration(os.Args[4]); err == nil {
				timeout = n
			}
		}
		if err := runPairwait(addr, codeFile, timeout); err != nil {
			log.Fatal(err)
		}
	case "pair":
		c, err := clt01.New(addr)
		if err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		creds, err := c.Pair(ctx, clt01.StdinCodeProvider{}, clt01.AuthScopePermanent)
		if err != nil {
			log.Fatal(err)
		}
		store, _ := clt01.NewStore()
		if err := store.Save(creds); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("配对成功: device_id=%s user=%s\n凭据已保存到 %s\n", creds.DeviceID, creds.UserID, store.Path)

	case "info":
		c, err := clt01.ClientFromStore(addr)
		if err != nil {
			log.Fatal(err)
		}
		info, err := c.SystemInfo()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("设备 %s 用户 %s 时区 %s，能力项 %d 个\n", info.LocalIP, info.Username, info.Timezone, len(info.Capabilities))

	case "console":
		listen := ":8090"
		if len(os.Args) > 3 {
			listen = os.Args[3]
		}
		c, err := clt01.New(addr)
		if err != nil {
			log.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle("/", c.ConsoleProxy())
		srv := &http.Server{Addr: listen, Handler: mux}
		go func() {
			fmt.Printf("控制台已内嵌: http://127.0.0.1%s/config/pair  (源: %s)\n", listen, c.ConsoleURL("/config/pair"))
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal(err)
			}
		}()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		_ = srv.Close()

	default:
		log.Fatalf("未知命令: %s", cmd)
	}
}
