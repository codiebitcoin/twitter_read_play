// Package api 日志模块：设置页「日志」卡片一键下载 zip。
// 桌面模式日志在 <数据目录>/logs/desktop.log（含轮转的 .1）；服务模式日志走
// stdout，没有文件可打包 —— zip 里放一份说明，别让用户下到空包一脸懵。
package api

import (
	"archive/zip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// logSecretRe 日志里出现过的敏感参数：desktop-start 凭据 t=、OAuth 回调 code/token=。
// 日志 zip 是发给开发者的，下载时统一打码（行内替换，不改变行数）。
var logSecretRe = regexp.MustCompile(`([?&](?:t|code|token)=)[0-9A-Za-z._~/+-]{8,}`)

// maxZipLogBytes 单个日志文件打包上限：超过就截头（防异常膨胀拖垮下载）。
const maxZipLogBytes = 32 << 20

func (s *Server) downloadLogs(w http.ResponseWriter, r *http.Request) {
	// 日志模块是桌面端功能（服务模式日志走 stdout，没有文件可打包）
	if !s.Desktop {
		http.NotFound(w, r)
		return
	}
	name := "trp-logs-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	dir := ""
	if s.LogPath != "" {
		dir = filepath.Dir(s.LogPath)
	}
	type logFile struct{ name, path string }
	var files []logFile
	if dir != "" {
		if es, err := os.ReadDir(dir); err == nil {
			for _, e := range es {
				if e.IsDir() || !strings.Contains(e.Name(), ".log") {
					continue
				}
				files = append(files, logFile{e.Name(), filepath.Join(dir, e.Name())})
			}
		}
	}
	if len(files) == 0 {
		tw, _ := zw.Create("README.txt")
		_, _ = io.WriteString(tw, strings.Join([]string{
			"twitter_read_play 日志包",
			"",
			"没有找到日志文件：服务模式（Docker/裸进程）的日志走 stdout/stderr，",
			"用 `docker logs` / `journalctl` 查看。桌面模式的日志在数据目录 logs/desktop.log，",
			"从这个入口下载时应该包含它。",
		}, "\n"))
		return
	}
	for _, f := range files {
		data, err := os.ReadFile(f.path)
		if err != nil || len(data) > maxZipLogBytes {
			continue
		}
		ow, err := zw.Create(f.name)
		if err != nil {
			return
		}
		_, _ = ow.Write(logSecretRe.ReplaceAll(data, []byte("$1***")))
	}
}
