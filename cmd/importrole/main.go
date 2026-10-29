// importrole 把一个「现成的角色包」导入到服务数据目录（在线服务停着的时候跑，或跑完再重启服务）：
//
//	importrole -data ./data -spec ./resources/roles.json
//
// 为什么要有这个工具：角色包 = prompt / VRM / 语音样本 / 语言 / 复播设置，一次在设备上试音时
// 会连续改好几项；用页面逐项保存，中间任何一次并发读写的旧快照都可能把刚存的字段冲掉
// （见 internal/role/manager.go Get 的注释）。这个工具在服务进程外一次性写库 + 落资源文件，原子得多。
//
// spec 是 JSON 数组，每个元素：
//
//	{
//	  "id": "khamenei",                        // 数据目录下的角色目录名（roles/<id>/），也是角色 id
//	  "name": "Ayatollah Khamenei", "enabled": true,
//	  "prompt": "...", "speak_template": "...", "speak_mode": "tts",
//	  "language": "fa", "sex": 1, "icon": "/path/to/icon.png",   // icon 可选
//	  "vrm":    "/path/to/model.vrm",          // 可选：复制到 roles/<id>/model.vrm 并登记为激活
//	  "voice":  "/path/to/voice.mp3",          // 可选：复制到 roles/<id>/voice.mp3 并登记为激活
//	  "bindings": [{"machine_id": "m-…", "slot": 1, "enabled": true}]
//	}
//
// 已存在的角色按 id 更新（保持原有绑定/voice_id 等运行态字段，除非 spec 里显式给了）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/store"
)

type specRole struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       *bool  `json:"enabled"`
	Prompt        string `json:"prompt"`
	SpeakTemplate string `json:"speak_template"`
	SpeakMode     string `json:"speak_mode"`
	Language      string `json:"language"`
	Sex           int    `json:"sex"`
	Animate       int    `json:"animation_intensity"`
	Icon          string `json:"icon"`
	VRM           string `json:"vrm"`
	Voice         string `json:"voice"`
	Description   string `json:"description"`
	Bindings      []struct {
		MachineID string `json:"machine_id"`
		Slot      int    `json:"slot"`
		Enabled   *bool  `json:"enabled"`
	} `json:"bindings"`
	// parody 模式（Simulate the persona）
	ParodyEnabled  *bool  `json:"parody_enabled"`
	ParodyInterval int    `json:"parody_interval"`
	ParodyPrompt   string `json:"parody_prompt"`
}

func main() {
	dataDir := flag.String("data", "data", "数据目录（与服务的 -data 相同）")
	specPath := flag.String("spec", "", "角色包 JSON（数组）")
	dryRun := flag.Bool("dry-run", false, "只打印将要做的事，不写库")
	remoteBase := flag.String("remote", "", "远程导入：服务地址（如 https://your-host.example），此时不碰本地数据库")
	user := flag.String("user", envOr("TRP_USER", ""), "远程导入登录用户名")
	pass := flag.String("pass", envOr("TRP_PASS", ""), "远程导入登录密码")
	bindFlag := flag.Bool("bind", false, "是否应用 spec 里的 bindings（远程导入默认不绑，绑定另说）")
	flag.Parse()
	if *specPath == "" {
		log.Fatal("-spec required")
	}
	if *remoteBase != "" {
		runRemote(*remoteBase, *user, *pass, *specPath, *dryRun, *bindFlag)
		return
	}
	raw, err := os.ReadFile(*specPath)
	if err != nil {
		log.Fatal(err)
	}
	var specs []specRole
	if err := json.Unmarshal(raw, &specs); err != nil {
		log.Fatalf("spec: %v", err)
	}
	st, err := store.New(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	rm, err := role.NewManager(st)
	if err != nil {
		log.Fatal(err)
	}
	for _, sp := range specs {
		if sp.ID == "" || sp.Name == "" {
			log.Fatalf("spec entry needs id and name: %+v", sp)
		}
		// 新角色或已有角色：已有角色上保留运行态（绑定 voice_id/group_key、last_* 等），
		// 只更新 spec 明确给的字段。
		r, gerr := rm.Get(sp.ID)
		if gerr != nil {
			r = &role.Role{ID: sp.ID, Enabled: true}
		}
		r.Name = sp.Name
		r.Prompt = sp.Prompt
		r.Description = sp.Description
		r.Language = sp.Language
		if sp.SpeakTemplate != "" {
			r.SpeakTemplate = sp.SpeakTemplate
		}
		if sp.SpeakMode != "" {
			r.SpeakMode = sp.SpeakMode
		}
		if sp.Sex > 0 {
			r.Sex = sp.Sex
		}
		if sp.Animate > 0 {
			r.AnimationIntensity = sp.Animate
		}
		if sp.Enabled != nil {
			r.Enabled = *sp.Enabled
		}
		if sp.ParodyEnabled != nil {
			r.ParodyEnabled = *sp.ParodyEnabled
		}
		if sp.ParodyInterval > 0 {
			r.ParodyInterval = sp.ParodyInterval
		}
		if sp.ParodyPrompt != "" {
			r.ParodyPrompt = sp.ParodyPrompt
		}
		if len(sp.Bindings) > 0 && *bindFlag {
			var bs []model.Binding
			for _, b := range sp.Bindings {
				en := true
				if b.Enabled != nil {
					en = *b.Enabled
				}
				bs = append(bs, model.Binding{MachineID: b.MachineID, Slot: b.Slot, Enabled: en})
			}
			r.Bindings = bs
		}
		// 资源文件：复制到 roles/<id>/ 再登记（相对路径以 data/ 为根）
		copyIn := func(src, base, typ string) (string, error) {
			if src == "" {
				return "", nil
			}
			ext := filepath.Ext(src)
			if ext == "" {
				return "", fmt.Errorf("%s %q has no extension", typ, src)
			}
			rel := "roles/" + sp.ID + "/" + base + ext
			dst := filepath.Join(*dataDir, rel)
			if *dryRun {
				fmt.Printf("[dry-run] copy %s -> %s\n", src, dst)
				return rel, nil
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return "", err
			}
			b, err := os.ReadFile(src)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return "", err
			}
			return rel, nil
		}
		vrmRel, err := copyIn(sp.VRM, "model", "vrm")
		if err != nil {
			log.Fatalf("%s: vrm: %v", sp.ID, err)
		}
		if vrmRel != "" {
			r.LocalVrm = vrmRel
			if r.ClothID <= 0 {
				r.ClothID = 2
			}
			found := false
			for _, v := range r.LocalVrms {
				if v.File == vrmRel {
					found = true
				}
			}
			if !found {
				r.LocalVrms = append(r.LocalVrms, model.VrmAsset{File: vrmRel, ClothID: r.ClothID, AddedAt: time.Now()})
			}
		}
		voiceRel, err := copyIn(sp.Voice, "voice", "mp3")
		if err != nil {
			log.Fatalf("%s: voice: %v", sp.ID, err)
		}
		if voiceRel != "" {
			r.LocalVoice = voiceRel
			found := false
			for _, v := range r.LocalVoices {
				if v.File == voiceRel {
					found = true
				}
			}
			if !found {
				r.LocalVoices = append(r.LocalVoices, model.VoiceAsset{File: voiceRel, AddedAt: time.Now()})
			}
		}
		if sp.Icon != "" {
			// 头像保留原扩展名（页面按 Content-Type 返回，png/jpg 都行）
			iconRel, err := copyIn(sp.Icon, "icon", "icon")
			if err != nil {
				log.Fatalf("%s: icon: %v", sp.ID, err)
			}
			r.Icon = iconRel
		}
		if *dryRun {
			fmt.Printf("[dry-run] upsert role %s (%s) vrm=%s voice=%s bindings=%v\n", r.ID, r.Name, r.LocalVrm, r.LocalVoice, r.Bindings)
			continue
		}
		if err := rm.Upsert(r); err != nil {
			log.Fatalf("%s: upsert: %v", sp.ID, err)
		}
		saved, _ := rm.Get(r.ID)
		fmt.Printf("role %s (%s): vrm=%s voice=%s language=%s enabled=%v bindings=%d\n",
			saved.ID, saved.Name, saved.LocalVrm, saved.LocalVoice, saved.Language, saved.Enabled, len(saved.Bindings))
	}
	fmt.Println("done")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// runRemote 走 HTTP 导入到正在运行的服务（线上/本机均可），不碰数据库文件。
// 顺序：建/更新角色 -> 上传 VRM -> 上传语音样本；（可选）绑定。
func runRemote(base, user, pass, specPath string, dryRun, bind bool) {
	if !dryRun && (user == "" || pass == "") {
		log.Fatal("远程导入需要 -user / -pass（或 TRP_USER / TRP_PASS）")
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		log.Fatal(err)
	}
	var specs []specRole
	if err := json.Unmarshal(raw, &specs); err != nil {
		log.Fatalf("spec: %v", err)
	}
	var rc *remote
	if !dryRun {
		if rc, err = newRemote(base, user, pass); err != nil {
			log.Fatal(err)
		}
	}
	for _, sp := range specs {
		if sp.ID == "" || sp.Name == "" {
			log.Fatalf("spec entry needs id and name: %+v", sp)
		}
		if dryRun {
			fmt.Printf("[dry-run] PUT %s/%s  vrm=%s voice=%s icon=%s bind=%v\n",
				base, sp.ID, sp.VRM, sp.Voice, sp.Icon, bind)
			continue
		}
		if !bind && len(sp.Bindings) > 0 {
			sp.Bindings = nil
		}
		if err := rc.putRole(sp.ID, sp); err != nil {
			log.Fatalf("%s: %v", sp.ID, err)
		}
		for _, a := range []struct{ kind, path string }{
			{"vrm", sp.VRM}, {"voice", sp.Voice}, {"icon", sp.Icon},
		} {
			if a.path == "" {
				continue
			}
			if err := rc.uploadAsset(sp.ID, a.kind, a.path); err != nil {
				log.Fatalf("%s: %v", sp.ID, err)
			}
			fmt.Printf("  uploaded %s: %s\n", a.kind, filepath.Base(a.path))
		}
		if cur, err := rc.getRole(sp.ID); err == nil && cur != nil {
			fmt.Printf("role %s (%v): vrm=%v voice=%v language=%v bindings=%v\n",
				sp.ID, cur["name"], cur["local_vrm"], cur["local_voice"], cur["language"], cur["bindings"])
		}
	}
	fmt.Println("remote done")
}
