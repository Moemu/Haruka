//go:build windows

package agent

import (
	"fmt"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"haruka/protocol"
)

// WindowCollector 读取前台窗口：进程名进内置映射表，窗口标题只在本地解析出
// 文件扩展名后立即丢弃，标题原文永不出进程。
type WindowCollector struct {
	deps       Deps
	classifier *Classifier
}

func NewWindowCollector(deps Deps, classifier *Classifier) *WindowCollector {
	return &WindowCollector{deps: deps, classifier: classifier}
}

func (c *WindowCollector) Name() string           { return "window" }
func (c *WindowCollector) Capability() Capability { return CapOK }

func (c *WindowCollector) Tick(now time.Time) ([]protocol.Event, error) {
	// 锁屏/安全桌面：前台窗口属于系统进程（winlogon.exe 之类），既无意义也不该
	// 上报进程名——改报 idle，让状态页立刻切换到「挂机中」而不是挂着旧应用。
	if sessionLocked() {
		ev, err := protocol.NewEvent(protocol.TypeActivity, c.deps.Seq.Next(), now,
			protocol.ActivityData{Kind: "idle"})
		if err != nil {
			return nil, err
		}
		return []protocol.Event{ev}, nil
	}

	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		// 会话锁定或无前台窗口：不产出活动事件，presence 负责表达该状态。
		return nil, nil
	}

	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return nil, nil
	}

	title := windowTitle(hwnd)
	exe, err := processImageName(pid)
	if err != nil {
		c.deps.Log.Debug("读取进程映像名失败", "pid", pid, "error", err)
		return nil, nil
	}

	res := c.classifier.Classify(filepath.Base(exe), title)
	// res 产生后标题即刻失活：title 不再被引用，也不进入任何日志。
	ev, err := protocol.NewEvent(protocol.TypeActivity, c.deps.Seq.Next(), now, protocol.ActivityData{
		Kind:     res.Kind,
		App:      res.App,
		Category: res.Category,
		FileType: res.FileType,
	})
	if err != nil {
		return nil, err
	}
	return []protocol.Event{ev}, nil
}

func windowTitle(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	if n > 4096 {
		n = 4096
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func processImageName(pid uint32) (string, error) {
	h, _, callErr := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return "", fmt.Errorf("OpenProcess(%d): %v", pid, callErr)
	}
	defer procCloseHandle.Call(h)

	var size uint32 = 1024
	buf := make([]uint16, size)
	r1, _, callErr := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r1 == 0 {
		return "", fmt.Errorf("QueryFullProcessImageNameW(%d): %v", pid, callErr)
	}
	return syscall.UTF16ToString(buf[:size]), nil
}
