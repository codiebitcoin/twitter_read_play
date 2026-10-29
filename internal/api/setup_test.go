package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"twitter_read_play/internal/model"
)

// 首页「配齐」判定：设备 + 角色 + 有效绑定。删除设备后遗留的孤儿绑定不能算数，
// 否则首页会显示「已完成」但实际播不出去。
func TestSetupStatusComplete(t *testing.T) {
	s := newQueueTestServer(t)

	status := func() setupStatusResponse {
		rec := httptest.NewRecorder()
		s.setupStatus(rec, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var out setupStatusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("非 JSON: %v (%s)", err, rec.Body.String())
		}
		return out
	}

	if st := status(); st.Complete {
		t.Fatalf("空库不该是 complete: %+v", st)
	}

	mc, err := s.Machines.Add("living", "127.0.0.1", "permanent")
	if err != nil {
		t.Fatal(err)
	}
	// 未配对：有设备也不算配齐。
	if st := status(); st.Machines != 1 || st.Complete {
		t.Fatalf("只有未配对设备: %+v", st)
	}
	mc.Creds = &model.Creds{DeviceID: "dev", UserID: "user", DeviceAddr: mc.Addr, PairedAt: 1}
	if err := s.Machines.Update(mc); err != nil {
		t.Fatal(err)
	}
	if st := status(); st.PairedMachines != 1 {
		t.Fatalf("配对设备数 = %d", st.PairedMachines)
	}

	// 给初始角色加一个有效绑定 → 配齐。
	roles := s.Roles.List()
	if len(roles) == 0 {
		t.Fatal("空库应有一条初始角色")
	}
	rl := *roles[0]
	rl.Bindings = []model.Binding{{MachineID: mc.ID, Slot: 1, Enabled: true}}
	if err := s.Roles.Upsert(&rl); err != nil {
		t.Fatal(err)
	}
	if st := status(); !st.Complete || st.BoundRoles != 1 {
		t.Fatalf("有效绑定后应 complete: %+v", st)
	}

	// 停用绑定 → 回到未配齐。
	rl.Bindings[0].Enabled = false
	if err := s.Roles.Upsert(&rl); err != nil {
		t.Fatal(err)
	}
	if st := status(); st.Complete {
		t.Fatalf("停用绑定不该 complete: %+v", st)
	}

	// 绑定有效但设备未配对：配置链路仍然存在（complete=true），配对状态单独计数，
	// 首页据此给出"未配对"警告，而不是把刚走完向导的人打回"未配齐"。
	rl.Bindings[0].Enabled = true
	if err := s.Roles.Upsert(&rl); err != nil {
		t.Fatal(err)
	}
	mc.Creds = nil
	if err := s.Machines.Update(mc); err != nil {
		t.Fatal(err)
	}
	if st := status(); !st.Complete || st.PairedMachines != 0 {
		t.Fatalf("未配对设备：complete 应为 true 且 paired=0，实际 %+v", st)
	}
	mc.Creds = &model.Creds{DeviceID: "dev", UserID: "user", DeviceAddr: mc.Addr, PairedAt: 1}
	if err := s.Machines.Update(mc); err != nil {
		t.Fatal(err)
	}

	// 指向不存在设备的孤儿绑定 → 也不算。
	rl.Bindings = []model.Binding{{MachineID: "m-missing", Slot: 1, Enabled: true}}
	if err := s.Roles.Upsert(&rl); err != nil {
		t.Fatal(err)
	}
	if st := status(); st.Complete || st.BoundRoles != 0 {
		t.Fatalf("孤儿绑定不该 complete: %+v", st)
	}

	// 设置里配好 LLM / socialdata → 首页能提示「可以播报」。
	_ = s.Store.SetSetting("llm_base_url", "https://api.example.com")
	_ = s.Store.SetSetting("llm_api_key", "sk-test")
	_ = s.Store.SetSetting("llm_model", "test-model")
	_ = s.Store.SetSetting("socialdata_api_key", "sd-test")
	if st := status(); !st.LLMReady || !st.SocialdataReady {
		t.Fatalf("LLM/Key 状态未反映: %+v", st)
	}
}
