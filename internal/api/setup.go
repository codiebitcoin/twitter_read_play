// Package api：首页「导航页」用的聚合状态接口。
//
// 首页需要同时知道「有没有设备 / 有没有角色 / 有没有绑定 / 能不能播报」，如果前端串行拉
// /api/machines、/api/roles、/api/settings 再自己算，首屏会多几个 RTT，而且判定逻辑会散在
// 两个地方。这里一次算完，前端只做展示。
package api

import (
	"net/http"
	"runtime"
	"strings"
)

// setupStatusResponse GET /api/setup/status 的响应。
type setupStatusResponse struct {
	Machines        int    `json:"machines"`
	PairedMachines  int    `json:"paired_machines"`
	Roles           int    `json:"roles"`
	BoundRoles      int    `json:"bound_roles"` // 至少有一个有效绑定的角色数
	Complete        bool   `json:"complete"`    // 有设备 && 有绑定到该设备的角色
	LLMReady        bool   `json:"llm_ready"`
	SocialdataReady bool   `json:"socialdata_ready"`
	// DataSourceReady 有没有任何可用的取数方式：socialdata key，或内置浏览器已登录平台。
	// 首页/向导的「数据源」徽标看这个；socialdata_ready 只表示 key 本身。
	DataSourceReady bool   `json:"data_source_ready"`
	BrowserReady    bool   `json:"browser_ready"`    // 内置浏览器可用（桌面模式）
	BrowserLoggedIn int    `json:"browser_logged_in"` // 已登录的平台数
	PCMode          bool   `json:"pc_mode"`          // PC 模式：无公网回调，前端隐藏 webhook 配置
	HostOS          string `json:"host_os"`          // 服务端所在系统（darwin/linux/windows）：macOS 上配对前要提示「本地网络」授权
}

// setupStatus GET /api/setup/status：配置是否配齐（一次配齐 = 设备 + 角色 + 绑定）。
func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	out := setupStatusResponse{}

	machines := s.Machines.List()
	out.Machines = len(machines)
	machineIDs := make(map[string]bool, len(machines))
	for _, m := range machines {
		machineIDs[m.ID] = true
		if m.Creds != nil {
			out.PairedMachines++
		}
	}

	roles := s.Roles.List()
	out.Roles = len(roles)
	for _, rl := range roles {
		for _, b := range rl.Bindings {
			// 绑定必须指向仍存在的设备、槽位有效且启用，才算「配齐」；
			// 删除设备后遗留的孤儿绑定不该让首页显示「已完成」。
			if b.Enabled && b.MachineID != "" && b.Slot > 0 && machineIDs[b.MachineID] {
				out.BoundRoles++
				break
			}
		}
	}

	// complete 表示「配置链路已存在」：设备 + 角色 + 绑定。配对/在线是设备状态，
	// 由首页单独用 warn 提示（未配对设备收不到角色包），不把刚走完向导的人打回"未配齐"。
	out.Complete = out.Machines > 0 && out.BoundRoles > 0
	out.LLMReady = s.mustSetting("llm_base_url") != "" && s.mustSetting("llm_api_key") != "" && s.mustSetting("llm_model") != ""
	out.SocialdataReady = strings.TrimSpace(s.mustSetting("socialdata_api_key")) != ""
	// 数据源可以来自 socialdata key，也可以来自内置浏览器的登录态（桌面端）。
	if s.browserAvailable() {
		out.BrowserReady = true
		for _, st := range s.Browser.Statuses() {
			if st.Connected {
				out.BrowserLoggedIn++
			}
		}
	}
	out.DataSourceReady = out.SocialdataReady || out.BrowserLoggedIn > 0
	out.PCMode = s.PCMode
	out.HostOS = runtime.GOOS

	s.json(w, out)
}
