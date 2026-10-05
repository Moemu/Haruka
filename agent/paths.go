package agent

import (
	"os"
	"path/filepath"
)

// DefaultStateDir 返回当前用户的状态目录（spool、序号、暂停标记、日志）。
func DefaultStateDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "haruka")
	}
	return filepath.Join(os.TempDir(), "haruka")
}

// DefaultConfigPath 依次尝试当前目录与状态目录下的配置文件。
func DefaultConfigPath() string {
	if _, err := os.Stat("haruka-agent.yml"); err == nil {
		return "haruka-agent.yml"
	}
	return filepath.Join(DefaultStateDir(), "agent.yml")
}
