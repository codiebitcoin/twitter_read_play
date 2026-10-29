package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---- 远程导入（HTTP，不碰数据库）----
//
// 线上服务跑在 K8s 里、数据在 PVC 上，进程外改库要 exec 进 Pod；走 API 更安全：
//   - 角色：POST（新建）/ PUT（已有） /api/roles，用 GET 的快照做底再覆盖 spec 字段
//   - VRM / 语音样本：POST /api/roles/{id}/vrm、/voice（multipart，服务端落盘并登记）
//   - 头像：POST /api/roles/{id}/icon
// 注意：PUT 是「整包覆盖」，必须基于最新快照发送；PUT 之后再上传资源（上传接口自己会登记并保存）。

type remote struct {
	base  string
	token string
	hc    *http.Client
}

func newRemote(base, user, pass string) (*remote, error) {
	r := &remote{base: strings.TrimRight(base, "/"), hc: &http.Client{Timeout: 10 * time.Minute}}
	body, _ := json.Marshal(map[string]string{"user": user, "pass": pass})
	resp, err := r.hc.Post(r.base+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("login %s: %s", resp.Status, string(b))
	}
	for _, c := range resp.Cookies() {
		if c.Name == "trp_session" {
			r.token = c.Value
		}
	}
	if r.token == "" {
		return nil, fmt.Errorf("login ok but no trp_session cookie")
	}
	return r, nil
}

func (r *remote) do(method, path string, body io.Reader, contentType string) ([]byte, int, error) {
	req, err := http.NewRequest(method, r.base+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Cookie", "trp_session="+r.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

// getRole 角色快照（不存在返回 nil）。
func (r *remote) getRole(id string) (map[string]any, error) {
	b, code, err := r.do("GET", "/api/roles/"+id, nil, "")
	if err != nil {
		return nil, err
	}
	if code == 404 {
		return nil, nil
	}
	if code != 200 {
		return nil, fmt.Errorf("GET role %s: %d %s", id, code, string(b))
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// putRole 新建或更新（拿到的快照做底，spec 字段覆盖；spec 没给的资源字段保留原值）。
func (r *remote) putRole(id string, spec specRole) error {
	cur, err := r.getRole(id)
	if err != nil {
		return err
	}
	m := cur
	if m == nil {
		m = map[string]any{"id": id, "enabled": true}
	}
	m["id"] = id
	m["name"] = spec.Name
	if spec.Prompt != "" {
		m["prompt"] = spec.Prompt
	}
	if spec.Description != "" {
		m["description"] = spec.Description
	}
	if spec.Language != "" {
		m["language"] = spec.Language
	}
	if spec.SpeakTemplate != "" {
		m["speak_template"] = spec.SpeakTemplate
	}
	if spec.SpeakMode != "" {
		m["speak_mode"] = spec.SpeakMode
	}
	if spec.Sex > 0 {
		m["sex"] = spec.Sex
	}
	if spec.Animate > 0 {
		m["animation_intensity"] = spec.Animate
	}
	if spec.Enabled != nil {
		m["enabled"] = *spec.Enabled
	}
	if spec.ParodyEnabled != nil {
		m["parody_enabled"] = *spec.ParodyEnabled
	}
	if spec.ParodyInterval > 0 {
		m["parody_interval"] = spec.ParodyInterval
	}
	if spec.ParodyPrompt != "" {
		m["parody_prompt"] = spec.ParodyPrompt
	}
	if len(spec.Bindings) > 0 {
		var bs []map[string]any
		for _, b := range spec.Bindings {
			en := true
			if b.Enabled != nil {
				en = *b.Enabled
			}
			bs = append(bs, map[string]any{"machine_id": b.MachineID, "slot": b.Slot, "enabled": en})
		}
		m["bindings"] = bs
	}
	// 服务端只认 model.Role 的字段；prompts 等只读字段会被忽略
	body, _ := json.Marshal(m)
	method, path := "POST", "/api/roles"
	if cur != nil {
		method, path = "PUT", "/api/roles/"+id
	}
	b, code, err := r.do(method, path, bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	if code != 200 && code != 201 {
		return fmt.Errorf("%s role %s: %d %s", method, id, code, string(b))
	}
	return nil
}

// uploadAsset 上传角色资源：kind = vrm|voice|icon。
//   - vrm   : POST /vrm（服务端落盘 + 登记为激活，后台再下发设备）
//   - voice : POST /clone?upload_only=1（只留样本并激活，不触发克隆——克隆要绑定 + 在线设备，另做）
//   - icon  : POST /icon
func (r *remote) uploadAsset(id, kind, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	endpoint := "/api/roles/" + id + "/" + kind
	if kind == "voice" {
		// 与页面「上传样本」同一条路：只留样本，不克隆
		_ = mw.WriteField("upload_only", "1")
		endpoint = "/api/roles/" + id + "/clone"
	}
	if err := mw.Close(); err != nil {
		return err
	}
	b, code, err := r.do("POST", endpoint, &buf, mw.FormDataContentType())
	if err != nil {
		return err
	}
	if code != 200 && code != 201 && code != 202 {
		return fmt.Errorf("upload %s %s: %d %s", kind, id, code, string(b))
	}
	return nil
}
