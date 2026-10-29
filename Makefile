MYGO_VERSION := v0.4.5
MYGO := go run github.com/egoist/mygo/cmd/mygo@$(MYGO_VERSION)

.PHONY: build front build-go run build-docker desktop-windows desktop-linux desktop-appimage desktop-mac desktop desktop-run

# 前端：React (front/) → Vite 构建产物 front/dist，由 go:embed all:front/dist 嵌入二进制
front:
	cd front && npm ci || npm ci --legacy-peer-deps
	cd front && npm run build

# 前端开发服务器（API 代理到 :8080，需另开 make run-go）
front-dev:
	cd front && npm run dev

# 完整构建：先构建前端，再编译 Go（embed front/dist）
build: front
	go build -o dist/twitter_read_play .

# 只编译 Go（要求 front/dist 已存在，前端无改动时快速迭代）
build-go:
	go build -o dist/twitter_read_play .

run: build
	./dist/twitter_read_play -addr :8080 -data ./data

build-docker:
	docker build -t twitter_read_play:local -f Dockerfile .

# ---- 桌面端（MyGo：go:embed 前端 + 系统 WebView 窗口，纯 Go 无 cgo）----
# 产物统一在 build/：windows-amd64/*.exe + Setup 安装器、linux-amd64/*.deb/*.tar.gz
# + *.AppImage、darwin-universal/*.app（+ macOS 上 mygo 自带的原生 dmg）
# AppImage 需要 appimagetool；已有副本时用 APPIMAGE_TOOL=/path/to/appimagetool
APPIMAGE ?= build/twitter-read-play-x86_64.AppImage

# Windows exe + NSIS 安装器（Windows runner 上就地构建）
desktop-windows: front
	$(MYGO) build -platform windows/amd64 -o build

# Linux 产物（含 .deb 与 tar.gz）+ AppImage
desktop-appimage: front
	$(MYGO) build -platform linux/amd64 -o build
	sh scripts/make-appimage.sh build/linux-amd64 "$(APPIMAGE)"
desktop-linux: desktop-appimage

# macOS：在 Mac 上执行，.app + hdiutil 压缩 dmg（签名 / 公证也走这条）
desktop-mac: front
	$(MYGO) build -platform darwin/universal -o build
	tar -czf "build/Twitter Read Play.app.tar.gz" -C build/darwin-universal "Twitter Read Play.app"
	@ls build/darwin-universal/*.dmg >/dev/null 2>&1 || \
	  sh scripts/make-dmg.sh "build/darwin-universal/Twitter Read Play.app" "build/Twitter Read Play.dmg"

# 本机当前平台直接跑桌面窗口（开发调试）
desktop-run: build-go
	./dist/twitter_read_play -desktop -data ./data

# 一次产出三平台（任意一台机器即可交叉编译；dmg 用 genisoimage 生成）
desktop: front
	$(MYGO) build -platform windows/amd64,linux/amd64,darwin/universal -o build
	sh scripts/make-appimage.sh build/linux-amd64 "$(APPIMAGE)"
	sh scripts/make-dmg.sh "build/darwin-universal/Twitter Read Play.app" "build/Twitter Read Play.dmg"
	tar -czf "build/Twitter Read Play.app.tar.gz" -C build/darwin-universal "Twitter Read Play.app"
	@echo "产物见 build/：windows-amd64/*.exe、linux-amd64/*、*.AppImage、Twitter Read Play.dmg、Twitter Read Play.app.tar.gz"
