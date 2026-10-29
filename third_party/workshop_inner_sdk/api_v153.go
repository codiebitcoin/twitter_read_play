package clt01

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// api_v153.go — 固件 ≥1.5.3 实现（对齐 clt01_embed_backend 1.5.3 分支，见 PLAN.md §8）：
//   - 上传：gzip 压缩分块通道（job[transport=gzip] → chunk/:seq → complete，均为
//     1.5.3 新增路由）。chunk 正文 MaxBytesReader 8MiB；签名中间件对 gzip-stream/
//     zstd-stream 模式 LimitReader 8MiB，超限 413 `MODEL_TOO_LARGE`（非 stream 模式
//     无 8MiB 限制，job/async 上限 200MB）。
//   - 应用：PUT /character 三件套 model-apply（设备固件无 outfit/apply 端点；
//     1.5.3 在 commit 时增加 heavy-gate，内存不足报 MODEL_TOO_COMPLEX）。
//   - 落盘判据：uploaded-models（硬编码 root = CharacterAssetsRoot，与
//     clothCacheBasePath 一致，两版本同）权威校验；SUCCESS 但列表没有该模型
//     = 固件 VRM 管线损坏。
//
// 本层不做跨版本静默回退：版本已由 Facade 判定，失败就报失败，
// 避免向草稿目录写入半截文件污染 1.5.2 流程。

// vrmChunkWireTarget 每个压缩分块（独立 gzip frame）压出后的目标线上大小，
// 对 8MiB 硬限留足余量。
const vrmChunkWireTarget = 4 << 20

type v153Impl struct{ c *Client }

func (v *v153Impl) Version() FirmwareVersion { return VerChunkedUpload }

// UploadVRM gzip 分块全链路。任何一步失败即 Cancel 悬挂任务。
func (v *v153Impl) UploadVRM(ctx context.Context, cc *CallContext, filename string, data []byte) (string, error) {
	jobID, err := v.c.ClothUploadJobCompressedV153(cc.Slot, cc.Cloth, filename, int64(len(data)), UploadTransportGzip)
	if err != nil {
		return "", fmt.Errorf("clt01: 1.5.3 job: %w", err)
	}
	if err := sendGzipChunks(ctx, v.c, jobID, data); err != nil {
		_ = v.c.ClothUploadJobCancel(jobID)
		return "", err
	}
	if err := v.c.ClothUploadCompleteCompressedV153(jobID, UploadTransportGzip); err != nil {
		_ = v.c.ClothUploadJobCancel(jobID)
		return "", fmt.Errorf("clt01: 1.5.3 complete: %w", err)
	}
	return jobID, nil
}

func (v *v153Impl) PollUpload(ctx context.Context, jobID string, timeout time.Duration) error {
	return pollUploadJob(ctx, v.c, jobID, timeout)
}

func (v *v153Impl) SelectCloth(ctx context.Context, slot, cloth int) error {
	return selectClothForV153(ctx, v.c, slot, cloth)
}

// ApplyVRM 三件套 model-apply + uploaded-models 权威校验。
// 设备端 1.5.3 commit 阶段内存不足会返回 500 "failed to commit draft model"
// （MODEL_TOO_COMPLEX 语义），错误消息原样透出。
func (v *v153Impl) ApplyVRM(ctx context.Context, cc *CallContext, filename string) (*VerifyResult, error) {
	if err := v.SelectCloth(ctx, cc.Slot, cc.Cloth); err != nil {
		return nil, fmt.Errorf("clt01: select cloth: %w", err)
	}
	// 1.5.3 complete 返回 SUCCESS 只代表分块重组完成，VRM 优化/落盘是异步的
	// —— 轮询 uploaded-models 等待落盘（最长 30s）
	var last json.RawMessage
	for i := 0; i < 6; i++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("clt01: canceled: %w", ctx.Err())
		case <-time.After(5 * time.Second):
		}
		models, err := v.c.GetRawCtx(ctx, "/v1/api/data/user/cloth/uploaded-models")
		if err != nil {
			return &VerifyResult{Persisted: false, Evidence: "uploaded-models unavailable"}, fmt.Errorf("clt01: uploaded-models: %w", err)
		}
		last = models
		if uploadedModelPresentRaw(models, cc.Slot, cc.Cloth) {
			return &VerifyResult{Persisted: true, Evidence: "uploaded-models"}, nil
		}
	}
	return &VerifyResult{Persisted: false},
		fmt.Errorf("clt01: VRM not persisted by device within 30s (uploaded-models=%s) — firmware VRM pipeline likely broken, model will NOT change on screen (slot %d cloth %d)", truncStr(string(last)), cc.Slot, cc.Cloth)
}

func (v *v153Impl) CancelUpload(jobID string) error { return v.c.ClothUploadJobCancel(jobID) }

// sendGzipChunks 把 data 压成若干独立 gzip frame 逐块上传（sequence 严格从 0 递增）。
func sendGzipChunks(ctx context.Context, c *Client, jobID string, data []byte) error {
	seq := 0
	for len(data) > 0 {
		n := len(data)
		if n > vrmChunkWireTarget {
			n = vrmChunkWireTarget
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data[:n]); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		if err := c.ClothUploadChunkCompressedV153Ctx(ctx, jobID, seq, buf.Bytes(), UploadTransportGzip); err != nil {
			return fmt.Errorf("clt01: chunk %d (%dB wire): %w", seq, buf.Len(), err)
		}
		seq++
		data = data[n:]
	}
	return nil
}

// truncStr 截断长响应用于错误消息。
func truncStr(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
