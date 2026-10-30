//go:build windows

package agent

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"haruka/protocol"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	procGetLastInputInfo           = user32.NewProc("GetLastInputInfo")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextLengthW       = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procOpenInputDesktop           = user32.NewProc("OpenInputDesktop")
	procCloseDesktop               = user32.NewProc("CloseDesktop")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetTickCount               = kernel32.NewProc("GetTickCount")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procGetSystemPowerStatus       = kernel32.NewProc("GetSystemPowerStatus")
)

const processQueryLimitedInformation = 0x1000

// sessionLocked 报告控制台是否处于锁屏/安全桌面（锁屏、UAC 提权确认、
// Ctrl+Alt+Del）。锁屏时输入桌面是 winsta0\Winlogon，普通用户进程打不开，
// 以此判定——不依赖锁屏进程名（winlogon.exe 之类会随系统变化）。
func sessionLocked() bool {
	h, _, _ := procOpenInputDesktop.Call(0, 0, 0x0001 /*DESKTOP_READOBJECTS*/)
	if h == 0 {
		return true
	}
	procCloseDesktop.Call(h)
	return false
}

// lastInputInfo 与 winuser.LASTINPUTINFO 对应；dwTime 是 GetTickCount 时域。
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// PresenceCollector 读取调用会话的最后输入时间（必须运行在已登录用户的会话内，
// Session 0 服务进程取不到用户输入——这是两级进程架构的由来）。
type PresenceCollector struct {
	deps      Deps
	awayAfter time.Duration
}

func NewPresenceCollector(deps Deps, awayAfter time.Duration) *PresenceCollector {
	return &PresenceCollector{deps: deps, awayAfter: awayAfter}
}

func (c *PresenceCollector) Name() string           { return "presence" }
func (c *PresenceCollector) Capability() Capability { return CapOK }

func (c *PresenceCollector) Tick(now time.Time) ([]protocol.Event, error) {
	var li lastInputInfo
	li.cbSize = uint32(unsafe.Sizeof(li))
	r1, _, callErr := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r1 == 0 {
		return nil, fmt.Errorf("GetLastInputInfo: %v", callErr)
	}
	// uint32 减法天然处理 GetTickCount 的约 49.7 天回绕。
	tick, _, _ := procGetTickCount.Call()
	idle := time.Duration(uint32(tick)-li.dwTime) * time.Millisecond
	if idle < 0 {
		idle = 0
	}
	state := protocol.StateActive
	if idle >= c.awayAfter {
		state = protocol.StateAway
	}
	ev, err := protocol.NewEvent(protocol.TypePresence, c.deps.Seq.Next(), now,
		protocol.PresenceData{State: state, IdleMinutes: int(idle / time.Minute)})
	if err != nil {
		return nil, err
	}
	return []protocol.Event{ev}, nil
}
