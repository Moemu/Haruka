package agent

import (
	"path/filepath"
	"strings"
)

// VS Code 标题解析：只提取文件扩展名（标题形如「文件 — 目录 — Visual Studio Code」，
// U+2014 em dash），解析完丢弃标题原文。JetBrains 项目别名机制已移除，不再解析其标题。

// parseVSCodeFileExt 从 VS Code 窗口标题提取正在编辑的文件扩展名（如 ".go"）；
// 解析不出（新标签页、非编辑器窗口）返回空串。最长 8 字符防误报。
func parseVSCodeFileExt(title string) string {
	parts := strings.Split(title, " — ")
	if len(parts) < 3 {
		return ""
	}
	last := parts[len(parts)-1]
	for _, product := range []string{"Visual Studio Code", "VSCodium"} {
		if strings.HasPrefix(last, product) {
			ext := strings.ToLower(filepath.Ext(strings.TrimSpace(parts[0])))
			if ext != "" && len(ext) <= 8 {
				return ext
			}
			return ""
		}
	}
	return ""
}
