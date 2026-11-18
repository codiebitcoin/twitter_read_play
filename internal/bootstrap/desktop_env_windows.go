//go:build windows

package bootstrap

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32DLL              = syscall.NewLazyDLL("user32.dll")
	procGetSystemMetrics   = user32DLL.NewProc("GetSystemMetrics")
	procEnumDisplayDevices = user32DLL.NewProc("EnumDisplayDevicesW")
)

const smRemoteSession = 0x1000 // GetSystemMetrics(SM_REMOTESESSION)：会话来自 RDP

// displayDeviceW 对应 winuser.h 的 DISPLAY_DEVICEW。
type displayDeviceW struct {
	cb           uint32
	deviceName   [32]uint16
	deviceString [128]uint16
	stateFlags   uint32
	deviceID     [128]uint16
	deviceKey    [128]uint16
}

// windowsSoftwareRenderingPreferred Windows 上是否建议 WebView2 走软件渲染：
// RDP 会话，或所有活动显示适配器都是虚拟显卡（无 GPU 虚拟机）——这类环境
// 硬件合成会白屏（DOM 正常、画面全白）。探不到适配器时保守返回 false。
func windowsSoftwareRenderingPreferred() bool {
	if rdpSession() {
		return true
	}
	return onlyVirtualDisplayAdapters()
}

func rdpSession() bool {
	v, _, _ := procGetSystemMetrics.Call(smRemoteSession)
	return v != 0
}

func onlyVirtualDisplayAdapters() bool {
	// 常见虚拟/基础显卡名（小写子串匹配）；真实 GPU（Intel/NVIDIA/AMD 等）不命中。
	markers := []string{
		"basic render", "hyper-v", "vmware", "qxl", "virtio",
		"cirrus", "bochs", "standard vga", "virtual", "microsoft remote",
	}
	active := false
	for i := uint32(0); ; i++ {
		var d displayDeviceW
		d.cb = uint32(unsafe.Sizeof(d))
		r, _, _ := procEnumDisplayDevices.Call(0, uintptr(i), uintptr(unsafe.Pointer(&d)), 0)
		if r == 0 {
			break
		}
		if d.stateFlags&0x1 == 0 { // DISPLAY_DEVICE_ACTIVE
			continue
		}
		active = true
		name := strings.ToLower(syscall.UTF16ToString(d.deviceString[:]))
		virtual := false
		for _, m := range markers {
			if strings.Contains(name, m) {
				virtual = true
				break
			}
		}
		if !virtual {
			return false // 有真 GPU，保持硬件渲染
		}
	}
	return active
}
