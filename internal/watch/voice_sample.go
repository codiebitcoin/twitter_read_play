package watch

// knownQwenCloneGroups 已知支持克隆的 qwen 组：设备未广播时也默认优先尝试。
// 依据：2026-10-12 e2e 实测 codie-black/neo.h 上 sg_tts_en_group_qwen_audio 克隆成功
// （voice 21/22）；固件升级后设备不再广播该组，但克隆接口按 group_key 直接转发云端、
// 不校验是否在广播列表里 → 显式按已知组名先试。
var knownQwenCloneGroups = []string{"sg_tts_en_group_qwen_audio"}

// maxVoiceSampleBytes 克隆样本上传上限（留足余量：设备端 10 MiB 上限，云端按秒计费）。
const maxVoiceSampleBytes = 6 << 20

// trimVoiceSample 从 MP3 中截出前 limit 字节（按 MPEG 帧边界找齐，避免半个帧造成杂音）。
// 不重编码、不依赖 ffmpeg：ID3 头之后逐帧跳，截到最后不超过 limit 的帧结束位置。
func trimVoiceSample(data []byte, limit int) []byte {
	if len(data) <= limit {
		return data
	}
	// 跳过 ID3v2 头（"ID3" + 版本 + flags + 4 字节 synchsafe 长度）
	i := 0
	if len(data) > 10 && string(data[:3]) == "ID3" {
		n := int(data[6])<<21 | int(data[7])<<14 | int(data[8])<<7 | int(data[9])
		i = 10 + n
	}
	// 找第一个帧同步
	for i+4 < len(data) && !(data[i] == 0xFF && data[i+1]&0xE0 == 0xE0) {
		i++
	}
	end := i
	for i+4 < len(data) {
		if data[i] != 0xFF || data[i+1]&0xE0 != 0xE0 {
			i++
			continue
		}
		// 帧长：MPEG1/2/2.5 Layer III 的常见位率表
		br := int(data[i+2]>>4) & 0x0F
		sr := int(data[i+2]>>2) & 0x03
		pad := int(data[i+2]>>1) & 0x01
		if br == 0 || br == 0x0F || sr == 3 {
			i++
			continue
		}
		mpeg1 := data[i+1]&0x18 == 0x18
		isLayer3 := data[i+1]&0x06 == 0x02
		var frame int
		if mpeg1 && isLayer3 {
			bitrates := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
			srates := [...]int{44100, 48000, 32000}
			frame = 144000*bitrates[br]/srates[sr] + pad
		} else if isLayer3 {
			bitrates := [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
			srates := [...]int{22050, 24000, 16000}
			frame = 72000*bitrates[br]/srates[sr] + pad
		}
		if frame <= 0 {
			i++
			continue
		}
		if i+frame > limit {
			break
		}
		i += frame
		end = i
	}
	if end == 0 {
		return data[:limit]
	}
	return data[:end]
}
