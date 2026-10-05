//go:build linux

// x11-fakewin 是开发测试工具：在 X11 会话里创建一个伪装成 VS Code 的窗口，
// 标题可从 stdin 动态切换，用于端到端验收（如「IDE 切项目 2 分钟内状态页更新」）。
// 仅在图形会话内运行；标题格式与 parseVSCodeTitle 对齐（em dash 分隔）。
//
//	./x11-fakewin 'aurora — main.go — Visual Studio Code'
//	echo 'haruka — status.go — Visual Studio Code' | ./x11-fakewin  # 持续读 stdin 换标题
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

var le = binary.LittleEndian

const (
	opCreateWindow   = 1
	opMapWindow      = 8
	opUnmapWindow    = 10
	opInternAtom     = 16
	opChangeProperty = 18
	opGetProperty    = 20
)

type conn struct {
	c         net.Conn
	root      uint32
	idBase    uint32
	idCounter uint32
}

func (c *conn) u32(v uint32) []byte { b := make([]byte, 4); le.PutUint32(b, v); return b }

func (c *conn) nextID() uint32 {
	c.idCounter++
	return c.idBase + c.idCounter
}

// request 发请求。X11 请求头 4 字节：[0]=opcode [1]=未用 [2:4]=整个请求的
// 长度（4 字节字，含头）。wantReply 为真时读回应答（32 字节头起）。
func (c *conn) request(op byte, extra []byte, wantReply bool) ([]byte, error) {
	sizeWords := (4 + len(extra) + 3) / 4
	full := make([]byte, sizeWords*4)
	full[0] = op
	le.PutUint16(full[2:4], uint16(sizeWords))
	copy(full[4:], extra)
	if _, err := c.c.Write(full); err != nil {
		return nil, err
	}
	if !wantReply {
		return nil, nil
	}
	rep := make([]byte, 32)
	for {
		if _, err := readFull(c.c, rep); err != nil {
			return nil, err
		}
		if rep[0] == 1 {
			break // 应答
		}
		if rep[0] == 0 {
			return nil, fmt.Errorf("X11 错误帧 code=%d", rep[1])
		}
		// 事件帧（MapWindow 后 XWM 会推事件）：32 字节，丢弃后继续等应答
	}
	additional := int(le.Uint32(rep[4:8])) * 4
	if additional > 0 {
		more := make([]byte, additional)
		if _, err := readFull(c.c, more); err != nil {
			return nil, err
		}
		rep = append(rep, more...)
	}
	return rep, nil
}

func readFull(nc net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := nc.Read(buf[n:])
		if err != nil {
			return n, err
		}
		n += k
	}
	return n, nil
}

func (c *conn) internAtom(name string) (uint32, error) {
	// [0:2]=名字长度，[2:4]=unused，name+pad（与 agent/x11.go 同构）。
	extra := make([]byte, 4, 4+len(name)+4)
	le.PutUint16(extra[0:2], uint16(len(name)))
	extra = append(extra, name...)
	for len(extra)%4 != 0 {
		extra = append(extra, 0)
	}
	rep, err := c.request(opInternAtom, extra, true)
	if err != nil {
		return 0, err
	}
	return le.Uint32(rep[8:12]), nil
}

func (c *conn) changeProperty(w, prop, typ uint32, data []byte) error {
	extra := make([]byte, 20, 20+len(data)+4)
	le.PutUint32(extra[0:4], w)
	le.PutUint32(extra[4:8], prop)
	le.PutUint32(extra[8:12], typ)
	extra[12] = 8 // format
	le.PutUint32(extra[16:20], uint32(len(data)))
	extra = append(extra, data...)
	for len(extra)%4 != 0 {
		extra = append(extra, 0)
	}
	_, err := c.request(opChangeProperty, extra, false)
	return err
}

func (c *conn) getProperty(w, prop uint32) ([]byte, error) {
	extra := make([]byte, 20)
	le.PutUint32(extra[0:4], w)
	le.PutUint32(extra[4:8], prop)
	le.PutUint32(extra[8:12], 0) // type Any
	le.PutUint32(extra[12:16], 0)
	le.PutUint32(extra[16:20], 64) // 最多 64 个 32 位长
	rep, err := c.request(opGetProperty, extra, true)
	if err != nil {
		return nil, err
	}
	if len(rep) < 32 {
		return nil, fmt.Errorf("GetProperty 应答过短")
	}
	n := int(le.Uint32(rep[8:12]))
	if n > len(rep)-32 {
		n = len(rep) - 32
	}
	return rep[32 : 32+n], nil
}

func main() {
	title := "aurora — main.go — Visual Studio Code"
	if len(os.Args) > 1 {
		title = os.Args[1]
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	sock := display
	if strings.HasPrefix(display, ":") {
		sock = "/tmp/.X11-unix/X" + display[1:]
	}
	xc, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "X11 连接失败:", err)
		os.Exit(1)
	}

	// 握手：little-endian 'l' + 版本 11.0 + 协议名/数据空（各 2 字节长度 + 补齐 8 字节）
	hs := make([]byte, 12)
	hs[0] = 'l'
	le.PutUint16(hs[2:4], 11)
	if _, err := xc.Write(hs); err != nil {
		fmt.Fprintln(os.Stderr, "握手发送失败:", err)
		os.Exit(1)
	}
	head := make([]byte, 8)
	if _, err := readFull(xc, head); err != nil {
		fmt.Fprintln(os.Stderr, "握手应答失败:", err)
		os.Exit(1)
	}
	if head[0] != 1 {
		fmt.Fprintln(os.Stderr, "握手失败: success=", head[0])
		os.Exit(1)
	}
	setup := make([]byte, int(le.Uint16(head[6:8]))*4)
	if _, err := readFull(xc, setup); err != nil {
		fmt.Fprintln(os.Stderr, "setup 读取失败:", err)
		os.Exit(1)
	}
	// setup 布局（与 agent/x11.go parseSetup 同构）：major(2) minor(2)
	// rid-base(4) rid-mask(4) motion(4) vendor-len(2) …screens-len@20
	// formats-len@21；root 在 32 + pad4(vendor) + formats*8 处。
	vendorLen := int(le.Uint16(setup[16:18]))
	formatsLen := int(setup[21])
	off := 32 + (vendorLen+3)&^3 + formatsLen*8
	if len(setup) < off+4 {
		fmt.Fprintln(os.Stderr, "setup 中无 SCREEN")
		os.Exit(1)
	}
	idBase := le.Uint32(setup[4:8])
	root := le.Uint32(setup[off : off+4])
	c := &conn{c: xc, idBase: idBase, root: root}

	wmClass, err := c.internAtom("WM_CLASS")
	if err != nil {
		fmt.Fprintln(os.Stderr, "intern WM_CLASS:", err)
		os.Exit(1)
	}
	wmName, err := c.internAtom("_NET_WM_NAME")
	if err != nil {
		fmt.Fprintln(os.Stderr, "intern _NET_WM_NAME:", err)
		os.Exit(1)
	}
	utf8String, err := c.internAtom("UTF8_STRING")
	if err != nil {
		fmt.Fprintln(os.Stderr, "intern UTF8_STRING:", err)
		os.Exit(1)
	}
	strAtom, err := c.internAtom("STRING")
	if err != nil {
		fmt.Fprintln(os.Stderr, "intern STRING:", err)
		os.Exit(1)
	}

	wid := c.nextID()
	// CreateWindow：depth 0（CopyFromParent），28 字节参数，值掩码为空
	extra := make([]byte, 28)
	le.PutUint32(extra[0:4], wid)
	le.PutUint32(extra[4:8], root)
	le.PutUint16(extra[8:10], 10)
	le.PutUint16(extra[10:12], 10)
	le.PutUint16(extra[12:14], 320)
	le.PutUint16(extra[14:16], 200)
	le.PutUint16(extra[18:20], 1) // InputOutput
	if _, err := c.request(opCreateWindow, extra, false); err != nil {
		fmt.Fprintln(os.Stderr, "CreateWindow:", err)
		os.Exit(1)
	}
	if err := c.changeProperty(wid, wmClass, strAtom, []byte("code\x00code\x00")); err != nil {
		fmt.Fprintln(os.Stderr, "WM_CLASS:", err)
		os.Exit(1)
	}
	setTitle := func(t string) error { return c.changeProperty(wid, wmName, utf8String, []byte(t)) }
	if err := setTitle(title); err != nil {
		fmt.Fprintln(os.Stderr, "_NET_WM_NAME:", err)
		os.Exit(1)
	}
	mapReq := c.u32(wid)
	if _, err := c.request(opMapWindow, mapReq, false); err != nil {
		fmt.Fprintln(os.Stderr, "MapWindow:", err)
		os.Exit(1)
	}
	fmt.Printf("窗口就绪 wid=%d 标题=%q；stdin 每行切换标题，quit 退出\n", wid, title)

	// 自诊断：周期检查 _NET_ACTIVE_WINDOW 是否指向本窗口。
	netActive, err := c.internAtom("_NET_ACTIVE_WINDOW")
	if err != nil {
		fmt.Printf("[diag] intern _NET_ACTIVE_WINDOW 失败: %v（不诊断）\n", err)
	} else {
		go func() {
			for {
				time.Sleep(2 * time.Second)
				v, err := c.getProperty(c.root, netActive)
				if err != nil {
					fmt.Printf("[diag] _NET_ACTIVE_WINDOW 读取失败: %v\n", err)
					continue
				}
				if len(v) == 4 {
					aw := le.Uint32(v)
					if aw == wid {
						fmt.Printf("[diag] 本窗口为前台 (aw=%d)\n", aw)
					} else {
						fmt.Printf("[diag] 前台是别的窗口 aw=%d (本窗口 %d)\n", aw, wid)
					}
				} else {
					fmt.Printf("[diag] _NET_ACTIVE_WINDOW 空/异常: %v\n", v)
				}
			}
		}()
	}

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "quit" {
			return
		}
		if line == "focus" {
			// 重新映射自己的测试窗口，让 WSLg 恢复前台窗口观测。
			if _, err := c.request(opUnmapWindow, c.u32(wid), false); err != nil {
				fmt.Fprintln(os.Stderr, "UnmapWindow:", err)
				return
			}
			if _, err := c.request(opMapWindow, c.u32(wid), false); err != nil {
				fmt.Fprintln(os.Stderr, "MapWindow:", err)
				return
			}
			continue
		}
		if err := setTitle(line); err != nil {
			fmt.Fprintln(os.Stderr, "改标题失败:", err)
			return
		}
		fmt.Printf("标题 → %q\n", line)
	}
	// stdin 关闭（后台运行常见）：窗口保持现状驻留，销毁窗口即退出进程。
	fmt.Println("stdin 关闭，窗口驻留中")
	select {}
}
