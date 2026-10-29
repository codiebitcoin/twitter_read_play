package watch

import (
	"context"
	"os"
	"path/filepath"

	clt01 "workshop_inner_sdk"
)

// 本文件原为版本分发的实现层。该逻辑已整体下沉 SDK（facade/api_*/version，见其 PLAN.md）。
// 这里只保留与既有调用方（internal/api、cmd/*）兼容的薄封装，统一走原子语义。

// vrCallContext 组装原子调用的固定上下文。
func vrCallContext(machineID string, slot, cloth int) *clt01.CallContext {
	return &clt01.CallContext{MachineID: machineID, Slot: slot, Cloth: cloth}
}

// UploadVRMData 把一份 VRM 上传到设备的 slot/cloth 并应用（SDK 内按固件版本分发）。
func UploadVRMData(c *clt01.Client, machineID string, slot, cloth int, filename string, data []byte, logf func(ok bool, detail, errMsg string)) error {
	_, err := c.SetVrm(context.Background(), vrCallContext(machineID, slot, cloth), filename, data, true, &clt01.VRMOptions{Apply: true, OnStep: logf})
	return err
}

// UploadVRMDataOpt apply=false 时只上传到设备草稿（Persona 页「仅上传」），
// 不 SelectCloth/不转正；apply=true 上传成功后立即应用并做分层落盘校验。
func UploadVRMDataOpt(c *clt01.Client, machineID string, slot, cloth int, filename string, data []byte, apply bool, logf func(ok bool, detail, errMsg string)) error {
	_, err := c.SetVrm(context.Background(), vrCallContext(machineID, slot, cloth), filename, data, true, &clt01.VRMOptions{Apply: apply, OnStep: logf})
	return err
}

// ApplyDraftVRM 设备草稿目录已有该 slot/cloth 的同名模型时跳过重传直接应用 + 校验。
// 返回 false 表示没有可复用草稿（或固件无 drafts 接口），调用方应走完整上传。
func ApplyDraftVRM(c *clt01.Client, machineID string, slot, cloth int, filename string, logf func(ok bool, detail, errMsg string)) (bool, error) {
	applied, _, err := c.ApplyDraftVRM(context.Background(), vrCallContext(machineID, slot, cloth), filename, &clt01.VRMOptions{Apply: true, OnStep: logf})
	return applied, err
}

// UploadLocalVRMFile 便捷封装：从本地路径读取并上传（供 API 层复用）。
func UploadLocalVRMFile(c *clt01.Client, machineID string, slot, cloth int, path string, logf func(ok bool, detail, errMsg string)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return UploadVRMData(c, machineID, slot, cloth, filepath.Base(path), data, logf)
}
