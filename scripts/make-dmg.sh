#!/bin/sh
# 生成 macOS 磁盘镜像（.dmg），把 .app 与 Applications 快捷方式放进去。
# 用法：sh scripts/make-dmg.sh "<App.app 路径>" "<输出 .dmg 路径>"
#
#   macOS：hdiutil 生成压缩 UDZO 镜像（Apple 原生，可签名 / 公证）
#   Linux：genisoimage 生成带 Rock Ridge + Apple 扩展的镜像并命名 .dmg
#          macOS 可直接挂载拖拽（保留可执行位）；若要原生压缩 dmg，
#          请在 Mac 上执行 make desktop-mac。
set -ex

APP_DIR="${1:?用法: make-dmg.sh <App.app 路径> <输出 dmg 路径>}"
OUT="${2:?用法: make-dmg.sh <App.app 路径> <输出 dmg 路径>}"
VOLNAME="${VOLNAME:-Twitter Read Play}"
STAGE="$(dirname "$OUT")/dmg-stage"

rm -rf "$STAGE"
mkdir -p "$STAGE"
cp -R "$APP_DIR" "$STAGE/"
ln -sf /Applications "$STAGE/Applications"

if command -v hdiutil >/dev/null 2>&1; then
    hdiutil create -volname "$VOLNAME" -srcfolder "$STAGE" -ov -format UDZO "$OUT"
else
    command -v genisoimage >/dev/null 2>&1 || {
        echo "缺少 genisoimage（Debian/Ubuntu: apt install genisoimage）；" >&2
        echo "或改用 .app.tar.gz，或在 macOS 上执行 make desktop-mac。" >&2
        exit 1
    }
    genisoimage -quiet -R -apple -V "$VOLNAME" -o "$OUT" "$STAGE"
fi
rm -rf "$STAGE"
echo "dmg: $OUT"
