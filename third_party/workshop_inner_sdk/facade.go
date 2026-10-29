package clt01

import (
	"context"
	"fmt"
	"time"
)

// facade.go — 版本分发的上传/应用流程（SetVrm 的实现内核 + 草稿复用/落盘校验入口）。
// 宿主统一走 atomic.go 的原子语义；本文件的 uploadVRM 是 SetVrm 的执行体。

// VRMOptions 上传选项。
type VRMOptions struct {
	Apply   bool                                 // true（默认）= 上传后立即应用并校验；false = 仅入草稿
	Timeout time.Duration                        // 处理状态轮询上限（0 = 120s）
	OnStep  func(ok bool, detail, errMsg string) // 步骤日志回调（可 nil）
}

func (o *VRMOptions) logf(ok bool, detail, errMsg string) {
	if o != nil && o.OnStep != nil {
		o.OnStep(ok, detail, errMsg)
	}
}

// VRMResult 上传/应用结果。
type VRMResult struct {
	JobID   string
	Version FirmwareVersion // 实际执行层（含首次上传试探后的升格结果）
	Verify  *VerifyResult   // Apply=false 时为 nil
}

// uploadVRM 完整上传流程（SetVrm 的执行体），只支持 1.5.3+：
// gzip 分块 → 轮询 → outfit/apply 应用 → uploaded-models 权威校验。
func (c *Client) uploadVRM(ctx context.Context, cc *CallContext, filename string, data []byte, opt *VRMOptions) (*VRMResult, error) {
	if opt == nil {
		opt = &VRMOptions{Apply: true}
	}
	// 设备接收大文件远慢于默认超时，上传链路放宽；
	// 其余短请求不受影响（超时只是上限）。
	c.SetHTTPTimeout(15 * time.Minute)

	version := VerChunkedUpload
	opt.logf(true, fmt.Sprintf("%s VRM upload started (%d bytes, firmware %s)", cc.prefix(), len(data), version), "")

	impl := implFor(version, c)
	jobID, err := impl.UploadVRM(ctx, cc, filename, data)
	if err != nil {
		opt.logf(false, "", err.Error())
		return nil, err
	}

	if err := impl.PollUpload(ctx, jobID, opt.Timeout); err != nil {
		opt.logf(false, "", err.Error())
		return nil, err
	}

	res := &VRMResult{JobID: jobID, Version: version}
	if !opt.Apply {
		// 仅上传：模型在设备草稿目录，不应用不换屏（apply 由 Status 页/Check&fix 触发）
		opt.logf(true, fmt.Sprintf("%s VRM uploaded (drafts, upload-only, via %s) — apply from Status page", cc.prefix(), version), "")
		return res, nil
	}

	verify, err := impl.ApplyVRM(ctx, cc, filename)
	res.Verify = verify
	if err != nil {
		opt.logf(false, "", err.Error())
		return res, err
	}
	opt.logf(true, fmt.Sprintf("%s VRM synced (via %s, evidence: %s)", cc.prefix(), version, verify.Evidence), "")
	return res, nil
}

// ApplyDraftVRM 设备草稿目录已有该 slot 的同名模型（「仅上传」留下的）时
// 跳过整份重传，直接按本固件层语义应用 + 校验。返回 false 表示没有可复用草稿
// （或固件无 drafts 接口），调用方应走完整上传。
func (c *Client) ApplyDraftVRM(ctx context.Context, cc *CallContext, filename string, opt *VRMOptions) (bool, *VRMResult, error) {
	if opt == nil {
		opt = &VRMOptions{Apply: true}
	}
	c.SetHTTPTimeout(15 * time.Minute)
	drafts, err := c.ClothDrafts(cc.Slot)
	if err != nil {
		return false, nil, nil // 接口不可用 → 按无草稿处理
	}
	base := basename(filename)
	found := false
	for _, d := range drafts {
		if d.ClothID == cc.Cloth && d.Filename == base {
			found = true
			break
		}
	}
	if !found {
		return false, nil, nil
	}
	version := c.detectVersion()
	opt.logf(true, fmt.Sprintf("%s VRM draft already present (%s) — applying without re-upload", cc.prefix(), base), "")
	impl := implFor(version, c)
	verify, err := impl.ApplyVRM(ctx, cc, filename)
	res := &VRMResult{Version: version, Verify: verify}
	if err != nil {
		opt.logf(false, "", err.Error())
		return true, res, err
	}
	opt.logf(true, fmt.Sprintf("%s VRM synced (draft reuse, evidence: %s)", cc.prefix(), verify.Evidence), "")
	return true, res, nil
}

// VerifyVRMPersisted 按固件层校验模型是否真的落盘（防假 SUCCESS）：
//   - ≥1.5.3：uploaded-models 权威，恒空即管线损坏 → false；
//   - 1.5.x / legacy：部分构建列表恒空是已知显示问题 → 宽松返回 true。
func (c *Client) VerifyVRMPersisted(ctx context.Context, cc *CallContext) (bool, error) {
	version := c.detectVersion()
	models, err := c.GetRawCtx(ctx, "/v1/api/data/user/cloth/uploaded-models")
	if err != nil {
		if version.AtLeast(VerChunkedUpload) {
			return false, err // 权威层：列表接口不可用即失败
		}
		return true, nil // 低层固件列表接口不可用 ≠ 未落盘
	}
	if uploadedModelPresentRaw(models, cc.Slot, cc.Cloth) {
		return true, nil
	}
	if version.AtLeast(VerChunkedUpload) {
		return false, nil
	}
	return true, nil // 宽松层：恒空是已知显示问题
}

func basename(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
