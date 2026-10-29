package clt01

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// 本文件定义固件实现接口：
// Facade 只依赖 firmwareImpl；api_v153.go 是唯一实现（只支持 1.5.3+ 固件）。

// VerifyResult 应用后的落盘校验结果。
type VerifyResult struct {
	Persisted bool   // 设备端确实落盘/提交了该模型
	Evidence  string // 判据原文（uploaded-models 命中）
}

// firmwareImpl 固件的上传/应用/校验实现。
type firmwareImpl interface {
	Version() FirmwareVersion
	// UploadVRM 完整上传一份 VRM（含压缩/分块细节），失败时负责清理悬挂任务。
	UploadVRM(ctx context.Context, cc *CallContext, filename string, data []byte) (jobID string, err error)
	// PollUpload 轮询处理状态直到 SUCCESS / 终态 / 超时。
	PollUpload(ctx context.Context, jobID string, timeout time.Duration) error
	// ApplyVRM 选中并应用该 cloth，按本层判据做落盘校验。
	ApplyVRM(ctx context.Context, cc *CallContext, filename string) (*VerifyResult, error)
	// SelectCloth 仅换装（不含上传）。
	SelectCloth(ctx context.Context, slot, cloth int) error
	// CancelUpload 取消悬挂任务。
	CancelUpload(jobID string) error
}

// implFor 返回唯一实现（本 SDK 只支持 1.5.3+）。
func implFor(_ FirmwareVersion, c *Client) firmwareImpl {
	return &v153Impl{c: c}
}

// ---- 各实现共享的辅助 ----
// selectClothOutfitViaApply 1.5.3+（较新构建）：outfit/apply 是唯一应用通道
// （PUT 三件套会被 OUTFIT_APPLY_REQUIRED 拒）；CHARACTER_LOADING(409) 自动重试；
// 回读验证未生效重试，最多 5 次。
func selectClothOutfitViaApply(ctx context.Context, c *Client, slot, clothID int) error {
	for attempt := 0; ; attempt++ {
		err := c.OutfitApply(slot, clothID)
		if err == nil {
			select {
			case <-ctx.Done():
				return fmt.Errorf("clt01: canceled: %w", ctx.Err())
			case <-time.After(2 * time.Second):
			}
			if ch, gerr := c.GetCharacterCtx(ctx, slot); gerr == nil && ch.SelectedCloth == clothID {
				return nil
			}
		} else if ae, ok := err.(*APIError); ok {
			if attempt < 5 && ae.Code == "CHARACTER_LOADING" {
				time.Sleep(6 * time.Second)
				continue
			}
			return err
		} else {
			return err
		}
		if attempt >= 5 {
			return fmt.Errorf("clt01: outfit/apply accepted but slot %d still not on cloth %d", slot, clothID)
		}
	}
}

// selectClothForV153 1.5.3+ 的应用通道：outfit/apply 优先（较新构建唯一合法通道，
// 404 说明是早期 1.5.3 构建，回退三件套）。
func selectClothForV153(ctx context.Context, c *Client, slot, clothID int) error {
	err := selectClothOutfitViaApply(ctx, c, slot, clothID)
	if ae, ok := err.(*APIError); ok && (ae.StatusCode == 404 || ae.StatusCode == 405) {
		return selectClothThreePieceCtx(ctx, c, slot, clothID)
	}
	return err
}

// pollUploadJob 轮询 /cloth/upload/status：2s 间隔，timeout 上限（0 = 默认 120s）。
func pollUploadJob(ctx context.Context, c *Client, jobID string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	for i := 0; time.Duration(i)*2*time.Second < timeout; i++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("clt01: canceled: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
		status, code, err := c.ClothUploadStatus(jobID)
		if err != nil {
			return fmt.Errorf("clt01: upload status: %w", err)
		}
		switch status {
		case "SUCCESS":
			return nil
		case "FAILED", "CANCELED", "":
			msg := fmt.Sprintf("clt01: VRM processing status=%s", status)
			if code != "" {
				msg += " code=" + code
			}
			return fmt.Errorf("%s", msg)
		}
	}
	return fmt.Errorf("clt01: VRM processing timeout (%s)", timeout)
}

// selectClothThreePiece 各固件层统一的三件套 model-apply：PUT /character 显式携带
// selected_cloth + available_cloths + link_other_original_cloth（空串也要带），
// 设备端（1.5.2 与 1.5.3 同）判定 isModelApply 后走 commitDraftClothCache 提交草稿。
// 设备固件没有 /character/outfit/apply 端点（clt01_embed_backend 两版本路由均已核实）。
// CHARACTER_LOADING（VRM 处理中，409）自动重试；回读验证未生效则重试，最多 5 次。
func selectClothThreePieceCtx(ctx context.Context, c *Client, slot, clothID int) error {
	for attempt := 0; ; attempt++ {
		_, err := c.SelectClothV15x(slot, clothID)
		if err == nil {
			select {
			case <-ctx.Done():
				return fmt.Errorf("clt01: canceled: %w", ctx.Err())
			case <-time.After(2 * time.Second):
			}
			if ch, gerr := c.GetCharacter(slot); gerr == nil && ch.SelectedCloth == clothID {
				return nil
			}
		} else if ae, ok := err.(*APIError); ok {
			if attempt < 5 && ae.Code == "CHARACTER_LOADING" {
				time.Sleep(6 * time.Second)
				continue
			}
			return err
		} else {
			return err
		}
		if attempt >= 5 {
			return fmt.Errorf("clt01: model-apply accepted but slot %d still not on cloth %d", slot, clothID)
		}
	}
}

// uploadedModelPresentRaw 检查 uploaded-models 列表里是否有该 slot/cloth 的模型。
// 设备端响应（1.5.2 与 1.5.3 同，clt01_embed_backend cloth_uploaded_models_gin.go）：
//
//	{"success":true,"data":{"root":"…","total":N,"models":[
//	  {"filename","relative_path","size_bytes","slot_number","cloth_id","state"}]}}
//
// state: "applied"（正式）/ "uploaded_draft"（temp/ 草稿，未 commit）。
// 判落盘只认 applied（草稿态在 Facade 的 apply 流程 commit 之后才会出现）。
func uploadedModelPresentRaw(raw json.RawMessage, slot, cloth int) bool {
	// 兼容两种形态：信封未解包 {"data":{"models":[…]}} 与已解包 {"models":[…]}
	var wrapper struct {
		Data *struct {
			Models []struct {
				SlotNumber int    `json:"slot_number"`
				ClothID    int    `json:"cloth_id"`
				Filename   string `json:"filename"`
				State      string `json:"state"`
			} `json:"models"`
		} `json:"data"`
		Models []struct {
			SlotNumber int    `json:"slot_number"`
			ClothID    int    `json:"cloth_id"`
			Filename   string `json:"filename"`
			State      string `json:"state"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return false
	}
	entries := wrapper.Models
	if wrapper.Data != nil {
		entries = wrapper.Data.Models
	}
	for _, e := range entries {
		if e.ClothID != cloth {
			continue
		}
		if e.SlotNumber != 0 && e.SlotNumber != slot {
			continue
		}
		// 解析失败/未知 state 的旧格式兜底算命中；明确的 draft 态不算已落盘
		if e.State == "uploaded_draft" {
			continue
		}
		return true
	}
	return false
}
