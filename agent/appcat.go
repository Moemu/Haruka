package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// AppMeta 描述一个进程名对应的展示身份。IDE 非空表示该应用支持标题解析。
type AppMeta struct {
	App      string `yaml:"app"`
	Category string `yaml:"category"`
	IDE      string `yaml:"ide"` // jetbrains | vscode | ""
}

// builtinApps 是内置进程映射表（键为小写进程名），用户配置 app_map 可覆盖或追加。
var builtinApps = map[string]AppMeta{
	"idea64.exe":          {App: "IntelliJ IDEA", Category: "coding", IDE: "jetbrains"},
	"idea.exe":            {App: "IntelliJ IDEA", Category: "coding", IDE: "jetbrains"},
	"pycharm64.exe":       {App: "PyCharm", Category: "coding", IDE: "jetbrains"},
	"goland64.exe":        {App: "GoLand", Category: "coding", IDE: "jetbrains"},
	"rider64.exe":         {App: "JetBrains Rider", Category: "coding", IDE: "jetbrains"},
	"webstorm64.exe":      {App: "WebStorm", Category: "coding", IDE: "jetbrains"},
	"clion64.exe":         {App: "CLion", Category: "coding", IDE: "jetbrains"},
	"datagrip64.exe":      {App: "DataGrip", Category: "coding", IDE: "jetbrains"},
	"phpstorm64.exe":      {App: "PhpStorm", Category: "coding", IDE: "jetbrains"},
	"rubymine64.exe":      {App: "RubyMine", Category: "coding", IDE: "jetbrains"},
	"code.exe":            {App: "Visual Studio Code", Category: "coding", IDE: "vscode"},
	"code - insiders.exe": {App: "Visual Studio Code - Insiders", Category: "coding", IDE: "vscode"},
	"vscodium.exe":        {App: "VSCodium", Category: "coding", IDE: "vscode"},
	"devenv.exe":          {App: "Visual Studio", Category: "coding"},
	"chrome.exe":          {App: "Google Chrome", Category: "browsing"},
	"msedge.exe":          {App: "Microsoft Edge", Category: "browsing"},
	"firefox.exe":         {App: "Firefox", Category: "browsing"},
	"brave.exe":           {App: "Brave", Category: "browsing"},
	"opera.exe":           {App: "Opera", Category: "browsing"},
	"winword.exe":         {App: "Microsoft Word", Category: "writing"},
	"typora.exe":          {App: "Typora", Category: "writing"},
	"obsidian.exe":        {App: "Obsidian", Category: "writing"},
	"figma.exe":           {App: "Figma", Category: "design"},
}

// defaultPrivateApps 是敏感应用默认清单（借鉴 live-dashboard 的 SECRET 级）：
// 命中即整级匿名化，应用名与项目都不上报。
var defaultPrivateApps = []string{
	"KeePass*",
	"1Password*",
	"Bitwarden*",
}

// Classifier 把「前台进程名 + 窗口标题」转成脱敏后的 activity 事件负载。
// 窗口标题只在这里使用，解析完成后即被丢弃，绝不出网。
type Classifier struct {
	apps     map[string]AppMeta
	private  []string
	aliases  map[string]string
	unmapped string
	fileType bool
}

func NewClassifier(rt *Runtime) *Classifier {
	apps := make(map[string]AppMeta, len(builtinApps)+len(rt.Cfg.Activity.AppMap))
	for k, v := range builtinApps {
		apps[strings.ToLower(k)] = v
	}
	for k, v := range rt.Cfg.Activity.AppMap {
		apps[strings.ToLower(k)] = v
	}
	private := append([]string(nil), defaultPrivateApps...)
	private = append(private, rt.Cfg.Activity.PrivateApps...)
	return &Classifier{
		apps:     apps,
		private:  private,
		aliases:  rt.Cfg.Activity.Aliases,
		unmapped: rt.Cfg.Activity.Unmapped,
		fileType: rt.Cfg.Privacy.FileType,
	}
}

// Classify 依据进程名与窗口标题产出 activity 负载。调用方负责在拿到返回值后丢弃标题。
func (cl *Classifier) Classify(processBase, windowTitle string) ActivityResult {
	lower := strings.ToLower(processBase)
	for _, pat := range cl.private {
		if ok, _ := filepath.Match(strings.ToLower(pat), lower); ok {
			return ActivityResult{Kind: "private", Category: "private"}
		}
	}
	meta, known := cl.apps[lower]
	if !known {
		meta = AppMeta{App: processBase, Category: "other"}
	}
	if meta.Category == "" {
		meta.Category = "other"
	}
	res := ActivityResult{Kind: "app", App: meta.App, Category: meta.Category}
	switch meta.Category {
	case "browsing":
		res.Kind = "browser"
	case "coding":
		if meta.IDE != "" {
			res.Kind = "ide"
			parsed := parseIDETitle(meta.IDE, windowTitle)
			res.Project = cl.resolveProject(meta.IDE, parsed.Project)
			if cl.fileType && parsed.FileExt != "" {
				res.FileType = parsed.FileExt
			}
		}
	}
	return res
}

// resolveProject 应用别名表；未命中走 unmapped 兜底（hidden / hash / null 都不外发原名）。
func (cl *Classifier) resolveProject(ideID, project string) string {
	if project == "" {
		return ""
	}
	if alias, ok := cl.aliases[ideID+":"+project]; ok {
		return alias
	}
	switch cl.unmapped {
	case "hash":
		sum := sha256.Sum256([]byte(ideID + ":" + project))
		return "p-" + hex.EncodeToString(sum[:2])
	default: // hidden / null
		return ""
	}
}

// ActivityResult 是 activity 事件的负载形态（与 protocol.ActivityData 字段一致）。
type ActivityResult struct {
	Kind     string
	App      string
	Category string
	Project  string
	FileType string
}
