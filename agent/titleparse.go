package agent

import (
	"path/filepath"
	"strings"
)

// IDE 标题解析：只提取项目名与文件扩展名，标题原文在调用处即被丢弃。
// JetBrains 与 Visual Studio Code 的分隔符不同（en dash 与 em dash），分别处理。

type ideTitle struct {
	Project string
	FileExt string
}

var jetBrainsProducts = []string{
	"IntelliJ IDEA", "PyCharm", "GoLand", "WebStorm", "Rider",
	"CLion", "DataGrip", "PhpStorm", "RubyMine", "Aqua", "Fleet",
}

var vscodeProducts = []string{"Visual Studio Code", "VSCodium"}

func parseIDETitle(ideID, title string) ideTitle {
	switch ideID {
	case "jetbrains":
		return parseJetBrainsTitle(title)
	case "vscode":
		return parseVSCodeTitle(title)
	}
	return ideTitle{}
}

// JetBrains 标题形如「项目名 – 文件 – 产品名」（U+2013 en dash，两侧空格）。
func parseJetBrainsTitle(title string) ideTitle {
	parts := strings.Split(title, " – ")
	if len(parts) < 3 {
		return ideTitle{}
	}
	last := parts[len(parts)-1]
	for _, product := range jetBrainsProducts {
		if strings.HasPrefix(last, product) {
			return ideTitle{Project: strings.TrimSpace(parts[0])}
		}
	}
	return ideTitle{}
}

// Visual Studio Code 标题形如「文件 — 目录 — Visual Studio Code」（U+2014 em dash）。
func parseVSCodeTitle(title string) ideTitle {
	parts := strings.Split(title, " — ")
	if len(parts) < 3 {
		return ideTitle{}
	}
	last := parts[len(parts)-1]
	for _, product := range vscodeProducts {
		if strings.HasPrefix(last, product) {
			out := ideTitle{Project: strings.TrimSpace(parts[len(parts)-2])}
			ext := strings.ToLower(filepath.Ext(strings.TrimSpace(parts[0])))
			if ext != "" && len(ext) <= 8 {
				out.FileExt = ext
			}
			return out
		}
	}
	return ideTitle{}
}
