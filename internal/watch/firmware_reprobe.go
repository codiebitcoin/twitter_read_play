package watch

import (
	"log"
	"sync"
	"time"
)

// 播报失败后的固件版本自愈：
// 设备刷机（尤其降级 1.5.3 → 1.5.2）后，DB 里记录的旧版本会让 SDK 继续走
// chat/turns 通道，打到 1.5.2 固件上必然 ERROR。播放一旦没确认成功，就后台
// 重探一次固件版本并落库 —— 下一条播报（Client 每次新建并注入版本）自动换对通道。
// 同一台机器 1 分钟内只刷一次，避免队列连续失败时打爆设备。

const fwReprobeCooldown = time.Minute

var (
	fwKickMu   sync.Mutex
	fwKickLast = map[string]time.Time{}
)

// kickFirmwareReprobe 后台重探机器固件版本（带冷却；尽力而为，不报错）。
func (m *Manager) kickFirmwareReprobe(machineID string) {
	fwKickMu.Lock()
	if time.Since(fwKickLast[machineID]) < fwReprobeCooldown {
		fwKickMu.Unlock()
		return
	}
	fwKickLast[machineID] = time.Now()
	fwKickMu.Unlock()

	go func() {
		prev := ""
		if mc, err := m.machines.Get(machineID); err == nil {
			prev = mc.Firmware
		}
		v, err := m.machines.ProbeFirmware(machineID)
		if err != nil {
			log.Printf("[playback] firmware reprobe failed machine=%s: %v", machineID, err)
			return
		}
		if prev != "" && prev != v {
			log.Printf("[playback] firmware changed machine=%s: %s -> %s (channel selection updated)", machineID, prev, v)
		} else {
			log.Printf("[playback] firmware reconfirmed machine=%s: %s", machineID, v)
		}
	}()
}
