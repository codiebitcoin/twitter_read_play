#!/bin/sh
# 用 mygo build 的 Linux 产物打包 AppImage。
# 用法（在仓库根或任意目录执行）：
#   sh scripts/make-appimage.sh <mygo linux 产物目录> <输出 AppImage 路径>
# 依赖：mksquashfs（squashfs-tools）+ appimagetool；两者都可指定：
#   APPIMAGE_TOOL=/usr/bin/appimagetool   # 已有副本；默认到 tools 目录下载
#   APPIMAGE_TOOL_URL=...                 # 外网不可达时换成镜像地址
set -ex

SRC_DIR="${1:?用法: make-appimage.sh <产物目录> <输出路径>}"
OUT="${2:?用法: make-appimage.sh <产物目录> <输出路径>}"
TOOLS_DIR="${TOOLS_DIR:-build/tools}"
APPIMAGE_TOOL_NAME="appimagetool-x86_64.AppImage"
APPIMAGE_TOOL="${APPIMAGE_TOOL:-$TOOLS_DIR/appimagetool}"
APPIMAGE_TOOL_URL="${APPIMAGE_TOOL_URL:-https://github.com/AppImage/appimagetool/releases/download/continuous/$APPIMAGE_TOOL_NAME}"

# 1. 找到 mygo 产物里的可执行文件（应用名含空格，按 ELF magic 识别，不依赖 file）
EXE=""
for f in "$SRC_DIR"/*; do
    [ -f "$f" ] && [ -x "$f" ] || continue
    if [ "$(head -c 4 "$f" | od -An -tx1 | tr -d ' \n')" = "7f454c46" ]; then
        EXE="$f"; break
    fi
done
[ -n "$EXE" ] || { echo "未在 $SRC_DIR 找到可执行文件" >&2; exit 1; }

# 2. appimagetool（优先用已有副本 / 环境变量指定；否则尝试下载）
if [ ! -x "$APPIMAGE_TOOL" ]; then
    mkdir -p "$TOOLS_DIR"
    curl -fL --retry 2 --max-time 120 -o "$APPIMAGE_TOOL" "$APPIMAGE_TOOL_URL"
    chmod +x "$APPIMAGE_TOOL"
fi

# 3. 组装 AppDir
APPDIR="$(dirname "$OUT")/appimage-AppDir"
rm -rf "$APPDIR"
mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/icons/hicolor/1024x1024/apps"
cp "$EXE" "$APPDIR/usr/bin/twitter-read-play"

cat > "$APPDIR/AppRun" <<'EOF'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/twitter-read-play" -desktop "$@"
EOF
chmod +x "$APPDIR/AppRun"

cat > "$APPDIR/twitter-read-play.desktop" <<'EOF'
[Desktop Entry]
Type=Application
Name=Twitter Read Play
Comment=Twitter 新推文监控 → Codie 播报
Exec=AppRun
Icon=twitter-read-play
Categories=Network;Utility;
EOF

# 图标：优先用 mygo 产物里的，否则用仓库 resources/icon.png（调用方 cwd=仓库根）
ICON=""
for f in "$SRC_DIR"/*.png; do [ -f "$f" ] && ICON="$f" && break; done
[ -n "$ICON" ] || ICON="resources/icon.png"
cp "$ICON" "$APPDIR/twitter-read-play.png"
cp "$ICON" "$APPDIR/.DirIcon"
cp "$ICON" "$APPDIR/usr/share/icons/hicolor/1024x1024/apps/twitter-read-play.png"

# 4. 打包（解包版 appimagetool 无需 FUSE；AppImage 版加 --appimage-extract-and-run）
if head -c 11 "$APPIMAGE_TOOL" 2>/dev/null | grep -q AppImage; then
    "$APPIMAGE_TOOL" --appimage-extract-and-run "$APPDIR" "$OUT"
else
    "$APPIMAGE_TOOL" "$APPDIR" "$OUT"
fi
rm -rf "$APPDIR"
echo "AppImage: $OUT"
