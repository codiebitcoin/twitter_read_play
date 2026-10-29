package clt01

import "fmt"

// CallContext 固定调用上下文：所有原子接口都必须携带，
// 保证每次调用可定位到机器与角色（日志前缀、目标槽位、追踪 ID）。
//
// 设计约束：
//   - Slot/Cloth 是调用目标（≥1 的合法值），不再作为散落参数传递；
//   - MachineID/MachineName/CharacterName 冗余进日志与错误消息，便于多机排查；
//   - RequestID 可选，透传给设备端可追踪字段（如 chat/turns 的 request_id）。
type CallContext struct {
	MachineID     string // 机器标识（宿主分配，如 m-1791894013761841525）
	MachineName   string // 机器名（日志用）
	Slot          int    // 目标角色槽（1-5）
	Cloth         int    // 目标 cloth（模型相关操作必填，其余可 0）
	CharacterName string // 角色名（日志用）
	RequestID     string // 可选追踪 ID
}

func (cc *CallContext) ensure() *CallContext {
	if cc == nil {
		return &CallContext{}
	}
	return cc
}

// prefix 日志前缀：[machine=… slot=… cloth=… char=…]
func (cc *CallContext) prefix() string {
	cc = cc.ensure()
	return fmt.Sprintf("[machine=%s slot=%d cloth=%d char=%s]",
		orDash(cc.MachineID, cc.MachineName), cc.Slot, cc.Cloth, orDash(cc.CharacterName, cc.MachineName))
}

// validate 检查槽位/cloth 合法性（设备端要求 slot 1-5、cloth 1-N）。
func (cc *CallContext) validate(needCloth bool) error {
	cc = cc.ensure()
	if cc.Slot < 1 || cc.Slot > 5 {
		return fmt.Errorf("clt01: callcontext slot=%d 非法（设备要求 1-5）", cc.Slot)
	}
	if needCloth && cc.Cloth < 1 {
		return fmt.Errorf("clt01: callcontext cloth=%d 非法（模型操作要求 ≥1）", cc.Cloth)
	}
	return nil
}

func orDash(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "-"
}
