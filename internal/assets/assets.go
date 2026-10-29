// Package assets 随镜像内置的初始角色资源：VRM 模型 + voice_clone 语音样本。
// 只在两处使用：空库初始化时拷到数据目录（role.Manager），以及首次接入机器时
// 随该角色下发到设备（cloth 2 + voice_clone 槽 21）。
// 资源属于一条普通角色，之后一切以用户数据为准。
package assets

import _ "embed"

//go:embed seed.vrm
var SeedVRM []byte

//go:embed voice.mp3
var SeedVoice []byte

// SeedRoleID 空库初始化时插入的那条角色的固定 ID。
// 它只是一条普通角色：没有特殊标记、没有删除保护、不参与任何自动清理。
const SeedRoleID = "musk"
