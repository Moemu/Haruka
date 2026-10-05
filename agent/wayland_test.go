//go:build linux

package agent

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Mock 合成器回环测试：真实 unix socket 上跑完整 wire 链路
// （registry 枚举 → bind wl_seat/ext_idle_notifier_v1 → get_idle_notification
// → idled/resumed 事件注入），验证 waylandIdleSource 的状态机与降级判定。
// 真实合成器行为以 x11_integration_test 同款「实测为准」原则覆盖。

var errWaylandShort = errors.New("wayland 请求体过短")

// parseBindBody 解析 wl_registry.bind 请求体：
// name (u32) + interface (string) + version (u32) + new_id (u32)。
func parseBindBody(b []byte) (name uint32, iface string, newID uint32, err error) {
	if len(b) < 12 {
		return 0, "", 0, errWaylandShort
	}
	name = binary.LittleEndian.Uint32(b[0:4])
	slen := int(binary.LittleEndian.Uint32(b[4:8]))
	if slen < 1 || 8+slen > len(b) {
		return 0, "", 0, errWaylandShort
	}
	iface = string(b[8 : 8+slen-1])
	pos := 8 + align4(slen)
	if pos+8 > len(b) {
		return 0, "", 0, errWaylandShort
	}
	return name, iface, binary.LittleEndian.Uint32(b[pos+4 : pos+8]), nil
}

// mockCompositor 是极简合成器：支持多条客户端连接（重连测试需要），
// 暴露已 accept 的连接供测试模拟「合成器重启」。
type mockCompositor struct {
	t        *testing.T
	path     string
	withIdle bool
	ln       net.Listener
	cmdCh    chan uint16 // 事件注入：0=idled, 1=resumed
	ready    chan uint32 // 每条连接的 notification 就绪信号
	conns    chan net.Conn
	mu       sync.Mutex
	active   *mockIdlePeer
	clients  []net.Conn
	done     chan struct{}
}

type mockIdlePeer struct {
	c        *waylandConn
	notifyID uint32
	mu       sync.Mutex
}

func (p *mockIdlePeer) send(msg wlMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.c.send(msg)
}

func startMockCompositor(t *testing.T, withIdle bool) *mockCompositor {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "mock-0")
	m := &mockCompositor{
		t:        t,
		path:     filepath.Join(dir, "mock-0"),
		withIdle: withIdle,
		cmdCh:    make(chan uint16, 8),
		ready:    make(chan uint32, 4),
		conns:    make(chan net.Conn, 4),
		done:     make(chan struct{}),
	}
	m.listen()
	go m.injectLoop()
	t.Cleanup(func() {
		close(m.done)
		m.ln.Close()
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, conn := range m.clients {
			conn.Close()
		}
	})
	return m
}

func (m *mockCompositor) listen() {
	m.t.Helper()
	ln, err := net.Listen("unix", m.path)
	if err != nil {
		m.t.Fatalf("listen: %v", err)
	}
	m.ln = ln
	go m.acceptLoop(ln, m.withIdle)
}

func (m *mockCompositor) acceptLoop(ln net.Listener, withIdle bool) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		m.mu.Lock()
		m.clients = append(m.clients, conn)
		m.mu.Unlock()
		select {
		case m.conns <- conn:
		default: // 测试只关心最近的连接
		}
		go m.serveConn(conn, withIdle)
	}
}

func (m *mockCompositor) injectLoop() {
	for {
		select {
		case <-m.done:
			return
		case ev := <-m.cmdCh:
			m.mu.Lock()
			peer := m.active
			m.mu.Unlock()
			if peer != nil {
				_ = peer.send(wlMessage{obj: peer.notifyID, opcode: ev})
			}
		}
	}
}

// dropConn 关闭最近一条客户端连接（模拟合成器侧断开）。
func (m *mockCompositor) dropConn() {
	m.t.Helper()
	select {
	case conn := <-m.conns:
		conn.Close()
	case <-time.After(5 * time.Second):
		m.t.Fatal("mock 未收到客户端连接")
	}
}

func (m *mockCompositor) serveConn(conn net.Conn, withIdle bool) {
	defer conn.Close()
	c := &waylandConn{conn: conn, nextID: 1000} // 服务端对象号空间与客户端隔离
	peer := &mockIdlePeer{c: c}
	var regID, notifyID uint32
	var notifySeen bool
	for {
		msg, err := c.read(time.Time{})
		if err != nil {
			return
		}
		switch {
		case msg.obj == wlDisplayID && msg.opcode == 1: // get_registry
			regID = binary.LittleEndian.Uint32(msg.body)
			_ = peer.send(wlMessage{obj: regID, opcode: 0, body: append(wlU32(1), append(wlString("wl_seat"), wlU32(7)...)...)})
			if withIdle {
				_ = peer.send(wlMessage{obj: regID, opcode: 0, body: append(wlU32(2), append(wlString("ext_idle_notifier_v1"), wlU32(1)...)...)})
			}
		case msg.obj == wlDisplayID && msg.opcode == 0: // sync
			cb := binary.LittleEndian.Uint32(msg.body)
			_ = peer.send(wlMessage{obj: cb, opcode: 0, body: wlU32(0)})
		case msg.obj == regID && msg.opcode == 0: // bind
			_, iface, newID, err := parseBindBody(msg.body)
			if err != nil {
				return
			}
			if iface == "wl_seat" {
				_ = peer.send(wlMessage{obj: newID, opcode: 0, body: wlU32(3)}) // capabilities
			} else if iface == "ext_idle_notifier_v1" {
				notifySeen = true
			}
		case notifySeen && msg.opcode == 1: // get_idle_notification（发给 notifier）
			notifyID = binary.LittleEndian.Uint32(msg.body)
			peer.notifyID = notifyID
			m.mu.Lock()
			m.active = peer
			m.mu.Unlock()
			m.ready <- notifyID
		}
	}
}

// waitForIdleExact 等事件流处理到位（真实 socket + 后台 pump 有调度延迟，
// 用精确值重试区分「已处理」与「未处理」——范围断言会撞上未处理窗口）。
func waitForIdleExact(t *testing.T, src *waylandIdleSource, now time.Time, want int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if n, ok := src.IdleMinutes(now); ok && n == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	n, ok := src.IdleMinutes(now)
	t.Fatalf("空闲分钟数未收敛到 %d（got %d, ok=%v）", want, n, ok)
}

func TestWaylandIdleSourceTransitions(t *testing.T) {
	m := startMockCompositor(t, true)

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cur := base
	waylandNow = func() time.Time { return cur }
	t.Cleanup(func() { waylandNow = time.Now })

	src := newWaylandIdleSource(nil)
	t.Cleanup(src.Close)
	// 连接是惰性的：首次 IdleMinutes 触发握手，随后 mock 才会收到
	// get_idle_notification。
	if n, ok := src.IdleMinutes(cur); !ok || n != 0 {
		t.Fatalf("初始应 0 分钟（ok=%v n=%d probeErr=%v）", ok, n, src.ProbeError())
	}
	select {
	case <-m.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("合成器未收到 get_idle_notification")
	}

	// 用户静默：base+1m 推 idled。先用 base+2m 探测确认 pump 已处理
	// （已处理：60s + 30s = 90s → 1；未处理：非 idled → 0），
	// 确认后再推进读数时钟，否则 idleStart 会被定格到推进后的时刻。
	cur = base.Add(time.Minute)
	m.cmdCh <- 0
	waitForIdleExact(t, src, base.Add(2*time.Minute), 1)

	cur = base.Add(7 * time.Minute)
	waitForIdleExact(t, src, cur, 6)

	// 用户回活动：base+8m 推 resumed；非 idled → 空闲 0（无论过了多久）。
	cur = base.Add(8 * time.Minute)
	m.cmdCh <- 1
	waitForIdleExact(t, src, cur, 0)
	cur = cur.Add(10 * time.Second)
	waitForIdleExact(t, src, cur, 0)

	// 再次静默：idled 在 base+9m。cur 保持不动直到确认 pump 已处理
	// （idleStart 定格在 base+9m），用显式 now 参数探测：
	//   +31s → 31s+30s = 61s → 1（未处理则是 0，可区分）；
	//   +29s → 29s+30s = 59s → 0，验证分钟取整。
	cur = base.Add(9 * time.Minute)
	m.cmdCh <- 0
	waitForIdleExact(t, src, base.Add(9*time.Minute).Add(31*time.Second), 1)
	waitForIdleExact(t, src, base.Add(9*time.Minute).Add(29*time.Second), 0)
}

// 断言（切片 6 P1）：通知协议只在状态切换时推送事件，持续操作期间没有
// 任何事件——非 idled 必须恒为 0，不得按 lastActive 累加（否则连接
// 5 分钟后持续操作会被误判 away）。
func TestWaylandIdleContinuedOperation(t *testing.T) {
	m := startMockCompositor(t, true)

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cur := base
	waylandNow = func() time.Time { return cur }
	t.Cleanup(func() { waylandNow = time.Now })

	src := newWaylandIdleSource(nil)
	t.Cleanup(src.Close)
	if _, ok := src.IdleMinutes(cur); !ok {
		t.Fatalf("初始连接失败: %v", src.ProbeError())
	}
	<-m.ready

	// 连接保持、零事件，推进 10 分钟（远超 away 阈值 5 分钟）：仍应 0 分钟。
	cur = base.Add(10 * time.Minute)
	if n, ok := src.IdleMinutes(cur); !ok || n != 0 {
		t.Fatalf("持续操作（无状态切换事件）应恒为 0 分钟，got %d（ok=%v）", n, ok)
	}
}

// 断言（切片 6 P2）：临时连接故障按退避重试并自愈——合成器重启后恢复；
// 「确认不支持」则进程内不再重试。
func TestWaylandTransientFailureReconnects(t *testing.T) {
	m := startMockCompositor(t, true)

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	waylandNow = func() time.Time { return base }
	t.Cleanup(func() { waylandNow = time.Now })

	src := newWaylandIdleSource(nil)
	t.Cleanup(src.Close)
	if _, ok := src.IdleMinutes(base); !ok {
		t.Fatalf("初始连接失败: %v", src.ProbeError())
	}
	<-m.ready

	// 合成器重启：先关客户端连接，再关监听。
	m.dropConn()
	m.ln.Close()
	// pump 感知断开需要一点真实时间。
	deadline := time.Now().Add(5 * time.Second)
	for {
		src.mu.Lock()
		dead := src.dead
		src.mu.Unlock()
		if dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pump 未感知连接断开")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 断开后的首次读数：重连失败（监听已关）→ 临时故障，进入退避。
	if _, ok := src.IdleMinutes(base); ok {
		t.Fatal("重连应失败")
	}
	src.mu.Lock()
	delay := src.retryDelay
	unsupported := src.unsupported
	src.mu.Unlock()
	if unsupported {
		t.Fatal("临时故障不得标记为确认不支持")
	}
	if delay != wlRetryBase {
		t.Fatalf("首次退避应为 %s，got %s", wlRetryBase, delay)
	}

	// 合成器恢复（同路径重新监听），退避到期后自动重连成功。
	m.listen()
	src.mu.Lock()
	src.nextRetry = time.Time{} // 模拟退避时间流逝
	src.mu.Unlock()
	if n, ok := src.IdleMinutes(base); !ok || n != 0 {
		t.Fatalf("恢复后应重连成功（ok=%v n=%d probeErr=%v）", ok, n, src.ProbeError())
	}
	<-m.ready
}

// 断言（切片 6 P2）：确认不支持（协议缺失）在进程内不重试——即使之后
// 同路径出现支持该协议的合成器也不改变判定；协议集在会话存续期间不变。
func TestWaylandUnsupportedStaysPermanent(t *testing.T) {
	m := startMockCompositor(t, false)

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	waylandNow = func() time.Time { return base }
	t.Cleanup(func() { waylandNow = time.Now })

	src := newWaylandIdleSource(nil)
	t.Cleanup(src.Close)
	if _, ok := src.IdleMinutes(base); ok {
		t.Fatal("无 ext_idle_notifier_v1 的合成器应如实返回不可用")
	}
	if !errors.Is(src.ProbeError(), errIdleUnsupported) {
		t.Fatalf("探测失败应为「确认不支持」: %v", src.ProbeError())
	}

	// 同路径换上支持协议的合成器：进程内判定不变，不重试。
	m.ln.Close()
	m.withIdle = true
	m.listen()
	if _, ok := src.IdleMinutes(base); ok {
		t.Fatal("确认不支持后不得重试")
	}
}

// 断言（切片 6 P2）：暂停断开后台事件流（暂停 = 停止全部采集），恢复后
// 下次读数立即重建连接。
func TestWaylandPauseResume(t *testing.T) {
	m := startMockCompositor(t, true)

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	waylandNow = func() time.Time { return base }
	t.Cleanup(func() { waylandNow = time.Now })

	src := newWaylandIdleSource(nil)
	t.Cleanup(src.Close)
	if _, ok := src.IdleMinutes(base); !ok {
		t.Fatalf("初始连接失败: %v", src.ProbeError())
	}
	<-m.ready

	src.Pause()
	src.mu.Lock()
	connGone := src.conn == nil
	paused := src.paused
	src.mu.Unlock()
	if !connGone || !paused {
		t.Fatal("暂停应断开连接并置位暂停状态")
	}
	if _, ok := src.IdleMinutes(base); ok {
		t.Fatal("暂停期间不得重建连接")
	}

	src.Resume()
	if n, ok := src.IdleMinutes(base); !ok || n != 0 {
		t.Fatalf("恢复后应重连成功（ok=%v n=%d probeErr=%v）", ok, n, src.ProbeError())
	}
	<-m.ready
}

func TestWaylandWireStringLayout(t *testing.T) {
	// 线上格式固化测试（X11 QueryExtension 的教训：偏移量与长度语义必须用
	// 不对齐的名字钉死——"ab" 含 NUL 为 3 字节、补齐 1 字节，长度字段是 3 不是 4）。
	got := wlString("ab")
	if len(got) != 8 {
		t.Fatalf("wlString 长度应 8（4 头 + 3 体 + 1 补齐），got %d", len(got))
	}
	if n := binary.LittleEndian.Uint32(got[0:4]); n != 3 {
		t.Fatalf("长度字段应为 3（含 NUL、不含补齐），got %d", n)
	}
	want := []byte{'a', 'b', 0, 0}
	for i, w := range want {
		if got[4+i] != w {
			t.Fatalf("数据区第 %d 字节应 %d，got %d", i, w, got[4+i])
		}
	}
}

// TestWaylandLiveProbe 有真实 Wayland 会话时枚举 globals 并探测空闲能力，
// 用于记录合成器能力矩阵（无会话则跳过，CI 不受影响）。
func TestWaylandLiveProbe(t *testing.T) {
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		t.Skip("无 XDG_RUNTIME_DIR（非图形会话）")
	}
	conn, err := newWaylandConn()
	if err != nil {
		t.Skipf("Wayland 连接失败（如实跳过）: %v", err)
	}
	defer conn.conn.Close()
	globals, err := conn.enumerateGlobals()
	if err != nil {
		t.Skipf("registry 枚举失败: %v", err)
	}
	names := make([]string, 0, len(globals))
	hasIdle, hasSeat := false, false
	for _, g := range globals {
		names = append(names, g.iface)
		switch g.iface {
		case "ext_idle_notifier_v1":
			hasIdle = true
		case "wl_seat":
			hasSeat = true
		}
	}
	t.Logf("合成器 globals（%d 个）: %v", len(globals), names)
	if !hasSeat || !hasIdle {
		t.Logf("该合成器不支持 ext-idle-notify-v1（seat=%v notifier=%v），presence 如实降级", hasSeat, hasIdle)
	}
}
