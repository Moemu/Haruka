package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 暂停标记文件：`haruka-agent pause 2h` 写入，`resume` 删除，
// run 进程每轮 flush 前检查。首次检测到暂停时清空队列与 spool（设计案 6.4 节）。

const pauseMarkerFile = "paused.json"

func PauseMarkerPath(stateDir string) string {
	return filepath.Join(stateDir, pauseMarkerFile)
}

func WritePauseMarker(stateDir string, until time.Time) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(struct {
		Until string `json:"until"`
	}{until.UTC().Format(time.RFC3339)})
	tmp := PauseMarkerPath(stateDir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, PauseMarkerPath(stateDir))
}

// ReadPauseMarker 返回 (until, active)。过期标记视为不存在。
func ReadPauseMarker(stateDir string) (time.Time, bool, error) {
	raw, err := os.ReadFile(PauseMarkerPath(stateDir))
	if os.IsNotExist(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, true, err
	}
	var st struct {
		Until string `json:"until"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return time.Time{}, true, fmt.Errorf("暂停标记损坏，保持暂停；修复后执行 resume: %w", err)
	}
	until, err := time.Parse(time.RFC3339, st.Until)
	if err != nil {
		return time.Time{}, true, fmt.Errorf("暂停标记时间非法，保持暂停: %w", err)
	}
	return until, time.Now().Before(until), nil
}

func ClearPauseMarker(stateDir string) error {
	err := os.Remove(PauseMarkerPath(stateDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
