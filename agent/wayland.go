//go:build linux

package agent

// Wayland 空闲检测（设计案 8.2 节 Wayland 行）：
// XWayland 不实现 MIT-SCREEN-SAVER，原生 Wayland 会话需要合成器侧的
// ext-idle-notify-v1（KWin / sway / wlroots 系实现；GNOME 的 Mutter、
// Weston 未实现 → 如实 unsupported，不猜测）。
//
// 与 XScreenSaver 的查询型模型不同，ext-idle-notify-v1 是通知型：注册
// get_idle_notification 后由合成器推送 idled / resumed 事件。采集端因此
// 把「tick 时查询空闲毫秒」适配为「事件流维护空闲状态 + tick 读数」：
// 非 idled（含持续操作、无任何事件）空闲为 0；idled 后从 idleStart 起算，
// timeout 决定 idled 触发粒度：
//
//	idled 期间: now - idleStart + timeout
//	活跃期间:   0（合成器认定 timeout 内有输入）

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---- 线上格式基元 ----
// 消息头 8 字节：object id (u32) + (size<<16 | opcode) (u32)，size 含头；
// 整数为主机字节序（协议要求两端同端序，实际环境均为小端）；
// 字符串为 u32 长度（含结尾 NUL）+ 内容 + NUL + 补齐到 4 字节。

const wlDisplayID uint32 = 1

func wlU32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func wlString(s string) []byte {
	// 长度字段 = 内容 + NUL（不含补齐）；补齐字节附在末尾。
	// 教训同 X11 QueryExtension：长度语义差一个 pad 就会让接口名匹配失败，
	// 且恰好 4 字节对齐的名字完全测不出来——布局测试必须用不对齐的名字。
	raw := append([]byte(s), 0)
	out := append(wlU32(uint32(len(raw))), raw...)
	return appendPad(out)
}

func align4(n int) int { return (n + 3) &^ 3 }

type wlMessage struct {
	obj    uint32
	opcode uint16
	body   []byte
}

// ---- 连接与握手 ----

type waylandConn struct {
	conn       net.Conn
	nextID     uint32
	registryID uint32 // enumerateGlobals 后有效；bind 请求发给它
	notifyID   uint32 // idle notification 对象，pump 按它过滤事件
}

func dialWayland() (net.Conn, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return nil, errors.New("XDG_RUNTIME_DIR 未设置（无 Wayland 会话）")
	}
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		display = "wayland-0"
	}
	path := display
	if !filepath.IsAbs(path) {
		path = filepath.Join(runtimeDir, display)
	}
	return net.DialTimeout("unix", path, 3*time.Second)
}

func newWaylandConn() (*waylandConn, error) {
	conn, err := dialWayland()
	if err != nil {
		return nil, err
	}
	return &waylandConn{conn: conn, nextID: 1}, nil // 1 = wl_display
}

func (c *waylandConn) allocID() uint32 {
	c.nextID++
	return c.nextID
}

func (c *waylandConn) send(m wlMessage) error {
	size := 8 + len(m.body)
	if size > 0xffff {
		return errors.New("wayland 消息超长")
	}
	hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(hdr[0:4], m.obj)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(size)<<16|uint32(m.opcode))
	_, err := c.conn.Write(append(hdr, m.body...))
	return err
}

func (c *waylandConn) read(deadline time.Time) (wlMessage, error) {
	if deadline.IsZero() {
		c.conn.SetReadDeadline(time.Time{})
	} else {
		c.conn.SetReadDeadline(deadline)
	}
	var hdr [8]byte
	if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
		return wlMessage{}, err
	}
	word := binary.LittleEndian.Uint32(hdr[4:8])
	size := int(word >> 16)
	opcode := uint16(word & 0xffff)
	if size < 8 || size > 4096 {
		return wlMessage{}, fmt.Errorf("wayland 消息长度非法: %d", size)
	}
	body := make([]byte, size-8)
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return wlMessage{}, err
	}
	return wlMessage{obj: binary.LittleEndian.Uint32(hdr[0:4]), opcode: opcode, body: body}, nil
}

type waylandGlobal struct {
	name    uint32
	iface   string
	version uint32
}

// parseGlobal 解析 wl_registry.global 事件体：
// name (u32) + interface (string) + version (u32)。
func parseGlobal(b []byte) (waylandGlobal, error) {
	if len(b) < 8 {
		return waylandGlobal{}, errors.New("global 事件体过短")
	}
	name := binary.LittleEndian.Uint32(b[0:4])
	slen := int(binary.LittleEndian.Uint32(b[4:8]))
	if slen < 1 || 8+slen > len(b) {
		return waylandGlobal{}, errors.New("global 接口名长度非法")
	}
	iface := string(b[8 : 8+slen-1]) // 去结尾 NUL
	pos := 8 + align4(slen)
	if pos+4 > len(b) {
		return waylandGlobal{}, errors.New("global 版本字段缺失")
	}
	return waylandGlobal{name: name, iface: iface, version: binary.LittleEndian.Uint32(b[pos : pos+4])}, nil
}

func displayErrorMessage(b []byte) string {
	// wl_display.error 事件体：object_id (u32) + code (u32) + message (string)
	if len(b) >= 12 {
		if slen := int(binary.LittleEndian.Uint32(b[8:12])); slen >= 1 && 12+slen <= len(b) {
			return string(b[12 : 12+slen-1])
		}
	}
	return "未知错误"
}

// enumerateGlobals 发送 get_registry + sync，读事件直到 sync 回调 done，
// 返回合成器通告的全部 global。不认识的合成器私有事件直接跳过。
func (c *waylandConn) enumerateGlobals() ([]waylandGlobal, error) {
	c.registryID = c.allocID()
	registryID := c.registryID
	if err := c.send(wlMessage{obj: wlDisplayID, opcode: 1, body: wlU32(registryID)}); err != nil { // get_registry
		return nil, err
	}
	syncID := c.allocID()
	if err := c.send(wlMessage{obj: wlDisplayID, opcode: 0, body: wlU32(syncID)}); err != nil { // sync
		return nil, err
	}
	var globals []waylandGlobal
	deadline := time.Now().Add(3 * time.Second)
	for {
		m, err := c.read(deadline)
		if err != nil {
			return nil, fmt.Errorf("registry 枚举失败: %w", err)
		}
		switch {
		case m.obj == wlDisplayID && m.opcode == 0:
			return nil, fmt.Errorf("合成器报错: %s", displayErrorMessage(m.body))
		case m.obj == registryID && m.opcode == 0:
			g, err := parseGlobal(m.body)
			if err != nil {
				return nil, err
			}
			globals = append(globals, g)
		case m.obj == registryID && m.opcode == 1:
			// global_remove：枚举期间忽略
		case m.obj == syncID && m.opcode == 0:
			return globals, nil
		}
	}
}

// roundtrip 发一个 sync 并等它 done，确保此前的请求已被合成器处理。
func (c *waylandConn) roundtrip() error {
	syncID := c.allocID()
	if err := c.send(wlMessage{obj: wlDisplayID, opcode: 0, body: wlU32(syncID)}); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		m, err := c.read(deadline)
		if err != nil {
			return fmt.Errorf("roundtrip 失败: %w", err)
		}
		if m.obj == wlDisplayID && m.opcode == 0 {
			return fmt.Errorf("合成器报错: %s", displayErrorMessage(m.body))
		}
		if m.obj == syncID && m.opcode == 0 {
			return nil
		}
	}
}

// ---- 空闲状态源 ----

// wlIdleTimeout 是 idled 通知的触发粒度：合成器在用户静默该时长后推送
// idled。取 30 秒——空闲分钟数换算最多低估一个 timeout，且 resumed 后
// 能在一个 tick 内回到 active。
const wlIdleTimeout = 30 * time.Second

// 临时故障的重试退避：合成器重启、会话短暂切换等场景按此重试；
// 「确认不支持」不在此列（进程内不重试）。
const (
	wlRetryBase = 5 * time.Second
	wlRetryMax  = 60 * time.Second
)

// errIdleUnsupported 表示合成器已确认缺少 ext-idle-notify-v1（或无 seat）——
// 协议集在会话存续期间不变，探测到即进程内不再重试。
var errIdleUnsupported = errors.New("合成器未实现 ext-idle-notify-v1")

// waylandNow 可注入时钟（测试用）。
var waylandNow = time.Now

// waylandIdleSource 在进程内持有一条 Wayland 连接与一个 idle notification，
// 后台事件流维护 idled 状态，IdleMinutes 供 presence tick 读数。
//
// 失败分两类（不都永久降级）：
//   - 确认不支持（枚举到 globals 却没有 ext_idle_notifier_v1 / wl_seat）：
//     置 unsupported，进程内不重试；
//   - 临时故障（连不上、握手超时、合成器报错）：按退避重试（5s 起步、
//     60s 封顶），合成器重启后自动恢复。
//
// 暂停（设计案 6.4 节「暂停 = 停止全部采集」）通过 Pause 断开事件流——
// 后台 goroutine 也属于采集；Resume 后下次读数立即重连。
type waylandIdleSource struct {
	mu          sync.Mutex
	log         *slog.Logger
	conn        *waylandConn
	dead        bool
	unsupported bool // 已确认合成器不支持：进程内不重试
	paused      bool // 暂停中：连接已断开，恢复后重连
	nextRetry   time.Time
	retryDelay  time.Duration
	probeErr    error // 最近一次失败的具体原因（doctor 与测试诊断用）
	now         func() time.Time

	idled     bool
	idleStart time.Time
	timeout   time.Duration
}

func newWaylandIdleSource(log *slog.Logger) *waylandIdleSource {
	return &waylandIdleSource{log: log, timeout: wlIdleTimeout, now: waylandNow}
}

// ProbeError 返回最近一次失败的原因；nil 表示可用或尚未探测。
func (s *waylandIdleSource) ProbeError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probeErr
}

// IdleMinutes 返回当前空闲分钟数；false 表示 Wayland 侧不可用（无会话、
// 不支持或暂停中），presence 调用方继续走降级链。
//
// 通知型协议语义：idled/resumed 只在状态切换时推送，持续操作期间没有
// 任何事件——非 idled 即合成器认定用户活跃（timeout 内有输入），空闲为
// 0；只有收到 idled 后才从 idleStart 起算（+ timeout 补偿触发粒度）。
// 按 lastActive 累加会把「持续操作」误判为空闲增长（已修复的缺陷）。
func (s *waylandIdleSource) IdleMinutes(now time.Time) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ensureLocked(now) {
		return 0, false
	}
	if !s.idled {
		return 0, true
	}
	idle := now.Sub(s.idleStart) + s.timeout
	if idle < 0 {
		idle = 0
	}
	return int(idle / time.Minute), true
}

// ensureLocked 保证连接可用；返回 false 表示当前不可用（不支持 / 暂停 /
// 退避窗口内）。调用方必须已持有 mu。
func (s *waylandIdleSource) ensureLocked(now time.Time) bool {
	if s.unsupported || s.paused {
		return false
	}
	if s.conn != nil && !s.dead {
		return true
	}
	if s.conn != nil {
		s.conn.conn.Close() // 唤醒旧事件流退出
		s.conn = nil
	}
	if now.Before(s.nextRetry) {
		return false // 临时故障退避窗口内，不重试
	}
	conn, err := s.handshake()
	if err != nil {
		if s.log != nil {
			s.log.Debug("Wayland 空闲检测不可用", "error", err)
		}
		s.probeErr = err
		if errors.Is(err, errIdleUnsupported) {
			s.unsupported = true
		} else {
			// 临时故障：退避后重试（合成器重启、会话切换等会自愈）。
			if s.retryDelay == 0 {
				s.retryDelay = wlRetryBase
			} else {
				s.retryDelay *= 2
				if s.retryDelay > wlRetryMax {
					s.retryDelay = wlRetryMax
				}
			}
			s.nextRetry = now.Add(s.retryDelay)
		}
		return false
	}
	s.conn = conn
	s.dead = false
	s.probeErr = nil
	s.retryDelay = 0
	s.nextRetry = time.Time{}
	s.idled = false
	s.idleStart = time.Time{}
	if s.log != nil {
		s.log.Info("Wayland 空闲检测就绪", "timeout", s.timeout.String())
	}
	go s.pump(conn)
	return true
}

// Pause 暂停后台采集：断开事件流（pump 随之退出），不影响 unsupported 判定，
// Resume 后允许重连。设计案 6.4 节——暂停必须覆盖全部采集，含后台读事件。
func (s *waylandIdleSource) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	if s.conn != nil {
		s.conn.conn.Close()
		s.conn = nil
	}
}

// Resume 解除暂停：清退避，下次读数立即重建连接。
func (s *waylandIdleSource) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = false
	s.nextRetry = time.Time{}
	s.retryDelay = 0
}

// Close 释放连接并停机（agent 进程退出即回收，此处供显式清理）。
func (s *waylandIdleSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	if s.conn != nil {
		s.conn.conn.Close()
		s.conn = nil
	}
}

// handshake 完成连接、registry 枚举、seat 与 notifier 绑定、notification 创建。
// 协议缺失与临时连接故障分别返回，供调用方选择降级或重试。
func (s *waylandIdleSource) handshake() (*waylandConn, error) {
	conn, err := newWaylandConn()
	if err != nil {
		return nil, err
	}
	globals, err := conn.enumerateGlobals()
	if err != nil {
		conn.conn.Close()
		return nil, err
	}
	var seat, notifier *waylandGlobal
	for i := range globals {
		switch globals[i].iface {
		case "wl_seat":
			if seat == nil {
				seat = &globals[i]
			}
		case "ext_idle_notifier_v1":
			notifier = &globals[i]
		}
	}
	if notifier == nil || seat == nil {
		conn.conn.Close()
		return nil, fmt.Errorf("%w（seat=%v notifier=%v）", errIdleUnsupported, seat != nil, notifier != nil)
	}

	// wl_registry.bind 请求体：name + interface + version + new_id。
	// wl_seat 绑定 v1（capabilities 事件在 v1 下只有 u32，无需解析）。
	seatID := conn.allocID()
	seatBody := append(append(wlU32(seat.name), wlString("wl_seat")...), wlU32(1)...)
	seatBody = append(seatBody, wlU32(seatID)...)
	if err := conn.send(wlMessage{obj: conn.registryID, opcode: 0, body: seatBody}); err != nil {
		conn.conn.Close()
		return nil, err
	}

	notifierID := conn.allocID()
	nBody := append(append(wlU32(notifier.name), wlString("ext_idle_notifier_v1")...), wlU32(1)...)
	nBody = append(nBody, wlU32(notifierID)...)
	if err := conn.send(wlMessage{obj: conn.registryID, opcode: 0, body: nBody}); err != nil {
		conn.conn.Close()
		return nil, err
	}
	if err := conn.roundtrip(); err != nil {
		conn.conn.Close()
		return nil, err
	}

	// ext_idle_notifier_v1.get_idle_notification：new_id + timeout(ms) + seat。
	notifyID := conn.allocID()
	body := append(wlU32(notifyID), wlU32(uint32(s.timeout/time.Millisecond))...)
	body = append(body, wlU32(seatID)...)
	if err := conn.send(wlMessage{obj: notifierID, opcode: 1, body: body}); err != nil {
		conn.conn.Close()
		return nil, err
	}
	if err := conn.roundtrip(); err != nil {
		conn.conn.Close()
		return nil, err
	}
	conn.notifyID = notifyID
	return conn, nil
}

// pump 是连接的常驻事件流：只处理 ext_idle_notification_v1 的 idled/resumed
// 与 wl_display.error，其余事件跳过；读失败即标记 dead（下次读数重连）。
func (s *waylandIdleSource) pump(conn *waylandConn) {
	for {
		m, err := conn.read(time.Time{})
		if err != nil {
			s.mu.Lock()
			if s.conn == conn {
				s.dead = true
			}
			s.mu.Unlock()
			return
		}
		if m.obj == wlDisplayID && m.opcode == 0 {
			s.mu.Lock()
			if s.conn == conn {
				s.dead = true
			}
			s.mu.Unlock()
			if s.log != nil {
				s.log.Warn("合成器报错，Wayland 空闲检测断开", "error", displayErrorMessage(m.body))
			}
			conn.conn.Close()
			return
		}
		if m.obj != conn.notifyID {
			continue
		}
		now := s.now()
		s.mu.Lock()
		if s.conn != conn {
			s.mu.Unlock()
			return
		}
		switch m.opcode {
		case 0: // idled
			if !s.idled {
				s.idled = true
				s.idleStart = now
			}
		case 1: // resumed
			s.idled = false
		}
		s.mu.Unlock()
	}
}
