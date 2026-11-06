module twitter_read_play

go 1.27.1

require (
	github.com/egoist/mygo v0.4.5
	github.com/gorilla/websocket v1.5.3
	livechat v0.0.0
	modernc.org/sqlite v1.34.5
	workshop_inner_sdk v0.0.0
)

require (
	github.com/Akegarasu/blivedm-go v1.6.0 // indirect
	github.com/andybalholm/brotli v1.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/tidwall/gjson v1.17.3 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/exp v0.0.0-20230315142452-642cacee5cc0 // indirect
	golang.org/x/mod v0.21.0 // indirect
	golang.org/x/sync v0.10.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
	modernc.org/libc v1.61.13 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.8.2 // indirect
)

replace workshop_inner_sdk => ./third_party/workshop_inner_sdk

replace livechat => ./third_party/livechat

tool github.com/egoist/mygo/cmd/mygo
