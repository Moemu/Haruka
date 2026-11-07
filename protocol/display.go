package protocol

import "haruka/internal/display"

// CheckDisplayString 是 display.Check 的协议侧别名：三端契约历史 API，
// 实现已收敛到 internal/display 单一真源（核心与插件共用同一口径）。
func CheckDisplayString(field, v string, maxRunes int) error {
	return display.Check(field, v, maxRunes)
}
