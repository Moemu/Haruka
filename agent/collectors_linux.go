//go:build linux

package agent

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"haruka/protocol"
)

// Linux 采集器（设计案 8.2 节能力矩阵）：
//   - presence：X11 走 MIT-SCREEN-SAVER 扩展；XWayland / 原生 Wayland 会话
//     降级到 ext-idle-notify-v1（wayland.go），合成器不支持则如实 unsupported
//   - window：EWMH _NET_ACTIVE_WINDOW → WM_CLASS / _NET_WM_PID / _NET_WM_NAME
//     （原生 Wayland 的 toplevel 协议按合成器各异，仍走后续切片）
//
// 不可用时如实降级，不猜测、不少报为可用。

// x11Session 可用性探测结果按进程缓存：DISPLAY 变化属极端场景。
var linuxSessionProbed bool

// PresenceCollector 空闲检测：优先 XScreenSaver 扩展，降级 Wayland
// ext-idle-notify-v1。wayland 源惰性建立——X11 可用时完全不碰 Wayland。
type PresenceCollector struct {
	deps        Deps
	awayAfter   time.Duration
	wayland     *waylandIdleSource
	waylandOnce sync.Once
}

func NewPresenceCollector(deps Deps, awayAfter time.Duration) TickCollector {
	return &PresenceCollector{deps: deps, awayAfter: awayAfter}
}

func (c *PresenceCollector) Name() string { return "presence" }

func (c *PresenceCollector) waylandSource() *waylandIdleSource {
	c.waylandOnce.Do(func() { c.wayland = newWaylandIdleSource(c.deps.Log) })
	return c.wayland
}

// PauseCollector / ResumeCollector 实现调度器的 PauseAware：暂停时断开
// Wayland 事件流（后台 goroutine 也属于采集，设计案 6.4 节），恢复后重建。
func (c *PresenceCollector) PauseCollector() {
	if src := c.waylandSource(); src != nil {
		src.Pause()
	}
}

func (c *PresenceCollector) ResumeCollector() {
	if src := c.waylandSource(); src != nil {
		src.Resume()
	}
}

// x11IdleMinutes 区分正常读数、确认缺少扩展和临时采集故障。
func (c *PresenceCollector) x11IdleMinutes() (int, Capability) {
	conn, err := newX11Conn()
	if err != nil {
		return 0, CapDegraded
	}
	defer conn.Close()
	idleMs, err := conn.screensaverIdleMs()
	if err != nil {
		if errors.Is(err, errX11IdleUnsupported) {
			return 0, CapUnsupported
		}
		return 0, CapDegraded
	}
	return int(time.Duration(idleMs) * time.Millisecond / time.Minute), CapOK
}

func (c *PresenceCollector) idleMinutes(now time.Time) (int, Capability) {
	idle, capability := c.x11IdleMinutes()
	if capability == CapOK {
		return idle, capability
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("XDG_SESSION_TYPE") != "wayland" {
		return 0, capability
	}
	src := c.waylandSource()
	if idle, ok := src.IdleMinutes(now); ok {
		return idle, CapOK
	}
	if errors.Is(src.ProbeError(), errIdleUnsupported) {
		return 0, CapUnsupported
	}
	return 0, CapDegraded
}

func (c *PresenceCollector) Capability() Capability {
	_, capability := c.idleMinutes(waylandNow())
	return capability
}

func (c *PresenceCollector) Tick(now time.Time) ([]protocol.Event, error) {
	idleMinutes, capability := c.idleMinutes(now)
	if capability != CapOK {
		return nil, nil
	}
	idle := time.Duration(idleMinutes) * time.Minute
	state := protocol.StateActive
	if idle >= c.awayAfter {
		state = protocol.StateAway
	}
	ev, err := protocol.NewEvent(protocol.TypePresence, c.deps.Seq.Next(), now,
		protocol.PresenceData{State: state, IdleMinutes: idleMinutes})
	if err != nil {
		return nil, err
	}
	return []protocol.Event{ev}, nil
}

// WindowCollector 基于 EWMH 的前台窗口检测。
type WindowCollector struct {
	deps       Deps
	classifier *Classifier
}

func NewWindowCollector(deps Deps, classifier *Classifier) TickCollector {
	return &WindowCollector{deps: deps, classifier: classifier}
}

func (c *WindowCollector) Name() string { return "window" }

func (c *WindowCollector) Capability() Capability {
	conn, err := newX11Conn()
	if err != nil {
		return CapUnsupported
	}
	defer conn.Close()
	if aw, err := conn.activeWindow(); err == nil && aw != 0 {
		return CapOK
	}
	return CapDegraded // 有会话但无 EWMH 前台窗口（无窗口管理器或全部最小化）
}

func (c *WindowCollector) Tick(now time.Time) ([]protocol.Event, error) {
	conn, err := newX11Conn()
	if err != nil {
		return nil, nil
	}
	defer conn.Close()
	aw, err := conn.activeWindow()
	if err != nil || aw == 0 {
		return nil, nil
	}
	class, procName, title, err := conn.windowInfo(aw)
	if err != nil {
		return nil, err
	}
	appID := class
	if appID == "" {
		appID = procName
	}
	if appID == "" {
		return nil, nil
	}
	// 拿到分类结果后标题即刻失活：title 不再被引用，也不进入任何日志。
	res := c.classifier.Classify(appID, title)
	ev, err := protocol.NewEvent(protocol.TypeActivity, c.deps.Seq.Next(), now, protocol.ActivityData{
		Kind:     res.Kind,
		App:      res.App,
		Category: res.Category,
		Project:  res.Project,
		FileType: res.FileType,
	})
	if err != nil {
		return nil, err
	}
	return []protocol.Event{ev}, nil
}

// Linux 内置应用映射：WM_CLASS/comm 键（小写），与 Windows 表合并使用。
var linuxBuiltinApps = map[string]AppMeta{
	"code":               {App: "Visual Studio Code", Category: "coding", IDE: "vscode"},
	"code-oss":           {App: "Visual Studio Code", Category: "coding", IDE: "vscode"},
	"jetbrains-idea":     {App: "IntelliJ IDEA", Category: "coding", IDE: "jetbrains"},
	"idea":               {App: "IntelliJ IDEA", Category: "coding", IDE: "jetbrains"},
	"firefox":            {App: "Firefox", Category: "browsing"},
	"google-chrome":      {App: "Google Chrome", Category: "browsing"},
	"chromium":           {App: "Chromium", Category: "browsing"},
	"org.gnome.nautilus": {App: "Files", Category: "other"},
}

func init() {
	for k, v := range linuxBuiltinApps {
		if _, exists := builtinApps[k]; !exists {
			builtinApps[strings.ToLower(k)] = v
		}
	}
}
