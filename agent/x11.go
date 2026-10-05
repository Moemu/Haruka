//go:build linux

package agent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// x11Conn 是一次性的 X11 连接，只实现采集所需的协议子集（设计案 8.2 节
// 能力矩阵的 X11 行）：连接握手、InternAtom、GetProperty、QueryExtension、
// MIT-SCREEN-SAVER 的 QueryInfo。窗口标题只在本地解析，原文不出进程。
//
// 为保持纯 Go 交叉编译，不链接 libX11/libXss，直接说 X 协议。
// 请求帧格式（基础协议）：[0]=opcode，[1]=子字段（或 0），[2:4]=CARD16
// 请求总长（4 字节单位，含头），其后为按规范排布的字段。

type x11Conn struct {
	conn    net.Conn
	root    uint32
	ridBase uint32
	seq     uint16
}

const (
	x11ProtoMajor = 11
	x11ProtoMinor = 0

	opInternAtom        = 16
	opGetProperty       = 20
	opQueryExtension    = 98
	opScreensaverQuery  = 1 // 扩展内的子操作码
	xscreensaverExtName = "MIT-SCREEN-SAVER"
)

var errX11IdleUnsupported = errors.New("MIT-SCREEN-SAVER 扩展不可用")

func newX11Conn() (*x11Conn, error) {
	display := os.Getenv("DISPLAY")
	if display == "" {
		return nil, fmt.Errorf("DISPLAY 未设置")
	}
	network, addr, displayNum, err := x11SocketAddr(display)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout(network, addr, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接 X server %s 失败: %w", addr, err)
	}
	c := &x11Conn{conn: conn}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	authName, authData := loadXauthCookie(displayNum)
	if err := c.handshake(authName, authData); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// x11SocketAddr 解析 DISPLAY：":0"/":0.0" → unix 套接字；"host:10" → tcp 6000+N。
func x11SocketAddr(display string) (network, addr string, displayNum int, err error) {
	host := display
	if i := strings.LastIndex(display, ":"); i >= 0 {
		host = display[:i]
		display = display[i+1:]
	} else {
		return "", "", 0, fmt.Errorf("DISPLAY 格式非法: %q", display)
	}
	numStr := display
	if i := strings.Index(display, "."); i >= 0 {
		numStr = display[:i]
	}
	var num int
	if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil {
		return "", "", 0, fmt.Errorf("DISPLAY 编号非法: %q", display)
	}
	if host == "" {
		return "unix", fmt.Sprintf("/tmp/.X11-unix/X%d", num), num, nil
	}
	return "tcp", fmt.Sprintf("%s:%d", host, 6000+num), num, nil
}

// handshake 完成 X11 连接 setup。auth 来自 Xauthority（MIT-MAGIC-COOKIE-1）；
// 本地 socket 通常免认证（auth 为空）。
func (c *x11Conn) handshake(authName string, authData []byte) error {
	buf := []byte{'l', 0}
	buf = binary.LittleEndian.AppendUint16(buf, x11ProtoMajor)
	buf = binary.LittleEndian.AppendUint16(buf, x11ProtoMinor)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(authName)))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(authData)))
	buf = binary.LittleEndian.AppendUint16(buf, 0) // 补齐
	buf = append(buf, authName...)
	buf = appendPad(buf)
	buf = append(buf, authData...)
	buf = appendPad(buf)
	if _, err := c.conn.Write(buf); err != nil {
		return err
	}

	prefix := make([]byte, 8)
	if err := readFull(c.conn, prefix); err != nil {
		return err
	}
	switch prefix[0] {
	case 0:
		reasonLen := int(prefix[1])
		extra := make([]byte, int(binary.LittleEndian.Uint16(prefix[6:8]))*4)
		if err := readFull(c.conn, extra); err != nil {
			return err
		}
		if reasonLen > len(extra) {
			return fmt.Errorf("X server 拒绝响应长度非法")
		}
		return fmt.Errorf("X server 拒绝连接: %s", string(extra[:reasonLen]))
	case 2:
		return fmt.Errorf("X server 要求认证，暂不支持")
	case 1:
	default:
		return fmt.Errorf("X server setup 类型非法")
	}

	bodyLen := int(binary.LittleEndian.Uint16(prefix[6:8])) * 4
	body := make([]byte, bodyLen)
	if err := readFull(c.conn, body); err != nil {
		return err
	}
	return c.parseSetup(body)
}

// parseSetup 提取首屏 root window 与资源 ID 基址。
func (c *x11Conn) parseSetup(b []byte) error {
	if len(b) < 32 {
		return fmt.Errorf("setup 响应过短")
	}
	vendorLen := int(binary.LittleEndian.Uint16(b[16:18]))
	screensLen := int(b[20])
	formatsLen := int(b[21])
	off := 32 + pad4(vendorLen) + formatsLen*8
	if screensLen < 1 || len(b) < off+4 {
		return fmt.Errorf("setup 中无 SCREEN")
	}
	c.ridBase = binary.LittleEndian.Uint32(b[4:8])
	c.root = binary.LittleEndian.Uint32(b[off : off+4])
	if c.root == 0 {
		return fmt.Errorf("root window 为 0")
	}
	return nil
}

// call 发送请求并等待同序号的 Reply，途中丢弃事件。
func (c *x11Conn) call(sub uint8, op uint8, extra []byte) ([]byte, error) {
	if err := c.send(sub, op, extra); err != nil {
		return nil, err
	}
	return c.readReply()
}

// send 发送请求：[0]=op，[1]=sub（子操作码/标志），[2:4]=CARD16 总长（4 字节单位，含头）。
// void 请求的协议层错误会以事件形式到达，被 readReply 丢弃。
func (c *x11Conn) send(sub, op uint8, extra []byte) error {
	units := (1 + len(extra) + 3) / 4
	req := make([]byte, units*4)
	req[0] = op
	req[1] = sub
	binary.LittleEndian.PutUint16(req[2:4], uint16(units))
	copy(req[4:], extra)
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(req)
	return err
}

func (c *x11Conn) readReply() ([]byte, error) {
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		hdr := make([]byte, 32)
		if err := readFull(c.conn, hdr); err != nil {
			return nil, err
		}
		switch hdr[0] {
		case 0: // Error
			return nil, fmt.Errorf("X 错误 code=%d seq=%d", hdr[1], binary.LittleEndian.Uint16(hdr[2:4]))
		case 1: // Reply
			extraLen := uint64(binary.LittleEndian.Uint32(hdr[4:8])) * 4
			if extraLen > 1<<20 {
				return nil, fmt.Errorf("X 应答过大")
			}
			extra := make([]byte, extraLen)
			if err := readFull(c.conn, extra); err != nil {
				return nil, err
			}
			return append(hdr, extra...), nil
		default: // Event：本采集器不订阅任何事件，直接丢弃
			if hdr[0]&0x7f == 35 { // X Generic Event 才包含额外长度字段
				extraLen := uint64(binary.LittleEndian.Uint32(hdr[4:8])) * 4
				if extraLen > 1<<20 {
					return nil, fmt.Errorf("X 事件过大")
				}
				skip := make([]byte, extraLen)
				if err := readFull(c.conn, skip); err != nil {
					return nil, err
				}
			}
		}
	}
}

// internAtom：extra 为 [0:2]=name 长度，[2:4]=unused，name+pad；
// only-if-exists 标志在请求头 [1]。
func (c *x11Conn) internAtom(name string, onlyIfExists bool) (uint32, error) {
	var sub uint8
	if onlyIfExists {
		sub = 1
	}
	extra := make([]byte, 4, 4+len(name)+4)
	binary.LittleEndian.PutUint16(extra[0:2], uint16(len(name)))
	extra = append(extra, name...)
	extra = appendPad(extra)
	reply, err := c.call(sub, opInternAtom, extra)
	if err != nil {
		return 0, err
	}
	atom := binary.LittleEndian.Uint32(reply[8:12])
	if atom == 0 {
		return 0, fmt.Errorf("atom %q 不存在", name)
	}
	return atom, nil
}

// queryExtension：extra 与 InternAtom 同构——[0:2]=name 长度，[2:4]=unused，name+pad
// （名字从偏移 8 起）。
// Reply 布局：[0]=type，[8]=present，[9]=major-opcode（注意 present 不在字节 1）。
func (c *x11Conn) queryExtension(name string) (present bool, majorOp uint8, err error) {
	extra := make([]byte, 4, 4+len(name)+4)
	binary.LittleEndian.PutUint16(extra[0:2], uint16(len(name)))
	extra = append(extra, name...)
	extra = appendPad(extra)
	reply, err := c.call(0, opQueryExtension, extra)
	if err != nil {
		return false, 0, err
	}
	return reply[8] != 0, reply[9], nil
}

// getProperty 以 AnyPropertyType 读取属性；属性不存在时返回 (0, nil, nil)。
// extra：[0:4]=window，[4:8]=property，[8:12]=type，[12:16]=long-offset，[16:20]=long-length。
func (c *x11Conn) getProperty(window, propAtom uint32) (format uint8, data []byte, err error) {
	extra := make([]byte, 20)
	putU32(extra[0:4], window)
	putU32(extra[4:8], propAtom)
	putU32(extra[8:12], 0)   // AnyPropertyType
	putU32(extra[12:16], 0)  // long-offset
	putU32(extra[16:20], 64) // long-length
	reply, err := c.call(0, opGetProperty, extra)
	if err != nil {
		return 0, nil, err
	}
	format = reply[1]
	n := int(binary.LittleEndian.Uint32(reply[16:20]))
	if format == 0 || n == 0 {
		return 0, nil, nil
	}
	itemSize := int(format) / 8
	if len(reply) < 32+n*itemSize {
		return 0, nil, fmt.Errorf("GetProperty 响应不完整")
	}
	return format, reply[32 : 32+n*itemSize], nil
}

// screensaverIdleMs 返回 MIT-SCREEN-SAVER 扩展的空闲毫秒数。
// extra：[0:4]=drawable；QueryInfo 子操作码经请求头 [1] 传递。
func (c *x11Conn) screensaverIdleMs() (uint32, error) {
	present, majorOp, err := c.queryExtension(xscreensaverExtName)
	if err != nil {
		return 0, err
	}
	if !present || majorOp == 0 {
		return 0, errX11IdleUnsupported
	}
	extra := make([]byte, 4)
	putU32(extra[0:4], c.root)
	reply, err := c.call(opScreensaverQuery, majorOp, extra)
	if err != nil {
		return 0, err
	}
	return parseScreensaverIdle(reply)
}

// parseScreensaverIdle 从 QueryInfo 应答中取 MsSinceUserInput。
// 布局（与 xgb 生成代码一致）：[1]=state，[8:12]=saver window，
// [12:16]=MsUntilServer（距屏保启动），[16:20]=MsSinceUserInput（输入空闲）。
// 二者不是同一个值：屏保未启用时 MsUntilServer 可能是倒计时或 0。
func parseScreensaverIdle(reply []byte) (uint32, error) {
	if len(reply) < 32 {
		return 0, fmt.Errorf("QueryInfo 应答过短: %d 字节", len(reply))
	}
	return binary.LittleEndian.Uint32(reply[16:20]), nil
}

// activeWindow 返回 EWMH _NET_ACTIVE_WINDOW；无 WM 或无前台窗口时返回 0。
func (c *x11Conn) activeWindow() (uint32, error) {
	atom, err := c.internAtom("_NET_ACTIVE_WINDOW", true)
	if err != nil {
		return 0, nil // 无 EWMH 窗口管理器：视为无前台窗口
	}
	format, data, err := c.getProperty(c.root, atom)
	if err != nil {
		return 0, err
	}
	if format != 32 || len(data) < 4 {
		return 0, nil
	}
	w := binary.LittleEndian.Uint32(data[:4])
	if w == 0 || w == 1 { // None / PointerRoot
		return 0, nil
	}
	return w, nil
}

// windowInfo 读取窗口的 WM_CLASS（类名）、_NET_WM_PID（进程名）与标题。
func (c *x11Conn) windowInfo(w uint32) (class, procName, title string, err error) {
	if atom, err2 := c.internAtom("WM_CLASS", true); err2 == nil {
		if format, data, err3 := c.getProperty(w, atom); err3 == nil && format == 8 {
			// WM_CLASS 为两个 NUL 分隔串：instance\0class\0
			parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
			if len(parts) >= 2 {
				class = parts[1]
			}
		}
	}
	if atom, err2 := c.internAtom("_NET_WM_PID", true); err2 == nil {
		if format, data, err3 := c.getProperty(w, atom); err3 == nil && format == 32 && len(data) >= 4 {
			procName = procComm(int(binary.LittleEndian.Uint32(data[:4])))
		}
	}
	if atom, err2 := c.internAtom("_NET_WM_NAME", true); err2 == nil {
		if format, data, err3 := c.getProperty(w, atom); err3 == nil && format == 8 {
			title = string(data)
		}
	}
	return class, procName, title, nil
}

func (c *x11Conn) Close() error { return c.conn.Close() }

// procComm 读取 /proc/<pid>/comm 作为进程名兜底。
func procComm(pid int) string {
	if pid <= 0 {
		return ""
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// --- 字节序与填充辅助 ---

func putU32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

func appendPad(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func pad4(n int) int { return (n + 3) / 4 * 4 }

func readFull(conn net.Conn, buf []byte) error {
	got := 0
	for got < len(buf) {
		n, err := conn.Read(buf[got:])
		if err != nil {
			return err
		}
		got += n
	}
	return nil
}
