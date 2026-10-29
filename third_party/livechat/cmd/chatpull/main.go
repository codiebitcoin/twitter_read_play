// chatpull：多源直播弹幕采集 CLI。
// 用法：
//
//	chatpull                          # 按 config.yaml 运行
//	chatpull -provider twitch_irc -target somechannel
//	chatpull -provider youtube_noauth -target "https://www.youtube.com/watch?v=XXX"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"livechat/pkg/provider"
	"livechat/pkg/provider/bilibili"
	"livechat/pkg/provider/twitcheventsub"
	"livechat/pkg/provider/youtubenoauth"
	"livechat/pkg/provider/youtubeofficial"
)

type Config struct {
	Provider       string `yaml:"provider"`
	Target         string `yaml:"target"`
	Proxy          string `yaml:"proxy"`
	TwitchEventSub struct {
		ClientID      string `yaml:"client_id"`
		AccessToken   string `yaml:"access_token"`
		BroadcasterID string `yaml:"broadcaster_id"`
		UserID        string `yaml:"user_id"`
	} `yaml:"twitch_eventsub"`
	YoutubeOfficial struct {
		AccessToken string `yaml:"access_token"`
	} `yaml:"youtube_official"`
	Bilibili struct {
		Cookie string `yaml:"cookie"`
	} `yaml:"bilibili"`
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func buildRegistry(cfg *Config) provider.Registry {
	return provider.Registry{
		"youtube_noauth": youtubenoauth.New(youtubenoauth.Options{Proxy: cfg.Proxy}),
		"youtube_official": youtubeofficial.New(youtubeofficial.Options{
			AccessToken: cfg.YoutubeOfficial.AccessToken,
		}),
		"twitch_eventsub": twitcheventsub.New(twitcheventsub.Options{
			ClientID:      cfg.TwitchEventSub.ClientID,
			AccessToken:   cfg.TwitchEventSub.AccessToken,
			BroadcasterID: cfg.TwitchEventSub.BroadcasterID,
			UserID:        cfg.TwitchEventSub.UserID,
		}),
		"bilibili_blivedm": bilibili.New(bilibili.Options{Cookie: cfg.Bilibili.Cookie}),
	}
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	provName := flag.String("provider", "", "覆盖配置中的 provider")
	target := flag.String("target", "", "覆盖配置中的 target（URL 或频道名）")
	duration := flag.Duration("duration", 30*time.Second, "运行时长（0=一直运行直到 Ctrl+C）")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	if *provName != "" {
		cfg.Provider = *provName
	}
	if *target != "" {
		cfg.Target = *target
	}
	if cfg.Provider == "" || cfg.Target == "" {
		log.Fatal("需要 provider 与 target（命令行或 config.yaml 中提供）")
	}

	reg := buildRegistry(cfg)
	p, ok := reg.Get(cfg.Provider)
	if !ok {
		log.Fatalf("未知 provider %q，可选: youtube_noauth | youtube_official | twitch_eventsub | bilibili_blivedm", cfg.Provider)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("收到退出信号，正在停止…")
		cancel()
	}()
	if *duration > 0 {
		timer := time.AfterFunc(*duration, cancel)
		defer timer.Stop()
	}

	log.Printf("provider=%s target=%s 开始拉取弹幕…", p.Name(), cfg.Target)
	msgCh, errCh, err := p.Stream(ctx, cfg.Target)
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}

	count := 0
	for {
		select {
		case m, ok := <-msgCh:
			if !ok {
				log.Printf("流结束，共 %d 条弹幕", count)
				return
			}
			count++
			fmt.Printf("[%s] %s %s | %s: %s\n",
				m.Platform, m.TS.Format("15:04:05"), m.TS.Format("2006-01-02"), m.Author, m.Text)
		case e := <-errCh:
			if e != nil {
				log.Printf("provider 错误: %v", e)
			}
			log.Printf("结束，共 %d 条弹幕", count)
			return
		case <-ctx.Done():
			log.Printf("超时/退出，共 %d 条弹幕", count)
			return
		}
	}
}
