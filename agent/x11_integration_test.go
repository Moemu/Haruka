//go:build linux

package agent

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"
	"time"

	"haruka/internal/geo"
	"haruka/protocol"
)

// X11 集成测试：需要 DISPLAY（WSLg 或 X 桌面），否则跳过。
// 自建一个带伪 VS Code 标题的测试窗口，验证「属性读取 → 标题解析 →
// 端侧脱敏 → activity 事件」全链路；标题原文全程不出进程。

const (
	opCreateWindow  = 1
	opDestroyWindow = 4
	opMapWindow     = 8
	opChangeProp    = 18
)

func (c *x11Conn) createWindow(w uint32) {
	extra := make([]byte, 28)
	putU32(extra[0:4], w)
	putU32(extra[4:8], c.root)
	binary.LittleEndian.PutUint16(extra[8:10], 10)   // x
	binary.LittleEndian.PutUint16(extra[10:12], 10)  // y
	binary.LittleEndian.PutUint16(extra[12:14], 240) // width
	binary.LittleEndian.PutUint16(extra[14:16], 120) // height
	binary.LittleEndian.PutUint16(extra[18:20], 1)   // class: InputOutput
	putU32(extra[20:24], 0)                          // visual: CopyFromParent
	putU32(extra[24:28], 0)                          // value-mask: 空
	c.send(0 /*depth: CopyFromParent*/, opCreateWindow, extra)
}

func (c *x11Conn) changeProperty(w, prop, typ uint32, format uint8, data []byte) {
	extra := make([]byte, 20, 20+len(data)+4)
	putU32(extra[0:4], w)
	putU32(extra[4:8], prop)
	putU32(extra[8:12], typ)
	extra[12] = format
	putU32(extra[16:20], uint32(len(data)))
	extra = append(extra, data...)
	extra = appendPad(extra)
	c.send(0 /*mode: replace*/, opChangeProp, extra)
}

func (c *x11Conn) mapWindow(w uint32) {
	extra := make([]byte, 4)
	putU32(extra[0:4], w)
	c.send(0, opMapWindow, extra)
}

func (c *x11Conn) destroyWindow(w uint32) {
	extra := make([]byte, 4)
	putU32(extra[0:4], w)
	c.send(0, opDestroyWindow, extra)
}

func readFullC(c *x11Conn, buf []byte) error {
	got := 0
	for got < len(buf) {
		n, err := c.conn.Read(buf[got:])
		if err != nil {
			return err
		}
		got += n
	}
	return nil
}

func TestX11Integration(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY 未设置，跳过 X11 集成测试")
	}
	conn, err := newX11Conn()
	if err != nil {
		t.Skipf("无法连接 X server: %v", err)
	}
	defer conn.Close()
	t.Logf("root window: 0x%x", conn.root)

	// 1. MIT-SCREEN-SAVER 空闲检测（数值本身不做断言，只验证协议可用）。
	//    附带对照扫描：已知 XWayland 支持 RANDR/SHM/XKEYBOARD、不支持
	//    MIT-SCREEN-SAVER（Wayland 下空闲检测走别的路径），据此校准解析。
	for _, name := range []string{"RANDR", "MIT-SHM", "XKEYBOARD", "MIT-SCREEN-SAVER"} {
		present, major, err := conn.queryExtension(name)
		if err != nil {
			t.Logf("QueryExtension %-24s 出错: %v", name, err)
			continue
		}
		t.Logf("QueryExtension %-24s present=%v major=%d", name, present, major)
	}
	idleMs, err := conn.screensaverIdleMs()
	if err != nil {
		t.Logf("ScreenSaver 不可用（能力应如实降级）: %v", err)
	} else {
		t.Logf("ScreenSaver 空闲: %d ms", idleMs)
	}

	// 2. 自建测试窗口：WM_CLASS 第二段为 "code"，标题伪造成 VS Code 格式。
	wid := conn.ridBase + 1
	conn.createWindow(wid)
	defer conn.destroyWindow(wid)
	stringAtom, _ := conn.internAtom("STRING", false)
	utf8Atom, _ := conn.internAtom("UTF8_STRING", false)
	classAtom, _ := conn.internAtom("WM_CLASS", false)
	nameAtom, _ := conn.internAtom("_NET_WM_NAME", false)
	conn.changeProperty(wid, classAtom, stringAtom, 8, []byte("test\x00code\x00"))
	conn.changeProperty(wid, nameAtom, utf8Atom, 8,
		[]byte("x11_test.go — haruka — Visual Studio Code"))
	conn.mapWindow(wid)
	time.Sleep(300 * time.Millisecond)

	class, procName, title, err := conn.windowInfo(wid)
	if err != nil {
		t.Fatalf("windowInfo: %v", err)
	}
	if class != "code" {
		t.Fatalf("WM_CLASS = %q, want %q（proc=%q title=%q）", class, "code", procName, title)
	}
	if title != "x11_test.go — haruka — Visual Studio Code" {
		t.Fatalf("标题读取不符: %q", title)
	}
	t.Logf("窗口属性读取正常: class=%q title=%q", class, title)

	// 3. 端到端：若环境维护 _NET_ACTIVE_WINDOW（XWayland/EWMH WM），
	//    采集器应产出脱敏后的 activity（项目走别名表，标题不出进程）。
	var aw uint32
	for i := 0; i < 20; i++ {
		aw, _ = conn.activeWindow()
		if aw == wid {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if aw != wid {
		t.Logf("环境未维护 _NET_ACTIVE_WINDOW，跳过采集器端到端断言")
		return
	}

	seq, err := OpenSeq(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rt := testRuntime(t, func(c *Config) {
		c.Activity.Aliases = map[string]string{"vscode:haruka": "aurora"}
	})
	coll := NewWindowCollector(Deps{DeviceID: "d_test", DeviceKind: protocol.KindDesktop, Seq: seq}, NewClassifier(rt))
	events, err := coll.Tick(time.Now())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(events) != 1 || events[0].Type != protocol.TypeActivity {
		t.Fatalf("应产出一条 activity 事件: %+v", events)
	}
	var ad protocol.ActivityData
	if err := json.Unmarshal(events[0].Data, &ad); err != nil {
		t.Fatalf("解码 activity: %v", err)
	}
	if ad.Kind != "ide" || ad.App != "Visual Studio Code" || ad.Project != "aurora" {
		t.Fatalf("activity 不符: %+v", ad)
	}
	if ad.FileType != "" {
		t.Fatalf("file_type 默认关闭: %+v", ad)
	}
	t.Logf("端到端 activity: %+v", ad)
}

// geo 集成测试放同文件以便共用跳过逻辑：xdb 未下载时跳过。
func TestGeoResolveIntegration(t *testing.T) {
	path := os.Getenv("HARUKA_XDB")
	if path == "" {
		path = "../../tmp-dev/ip2region.xdb"
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("ip2region.xdb 未下载，跳过（脚本见 README）")
	}
	s, err := geo.NewSearcher(path)
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}

	loc, ok := s.Resolve("223.5.5.5") // 阿里公共 DNS，杭州
	if !ok || loc.Country != "中国" || loc.Province != "浙江省" || loc.City != "杭州市" || loc.Precision != protocol.PrecisionCity {
		t.Fatalf("223.5.5.5 应解析为中国城市级: %+v ok=%v", loc, ok)
	}
	for _, ip := range []string{"223.5.5.5", "119.29.29.29", "8.8.8.8"} {
		if region, err := s.Region(ip); err != nil {
			t.Logf("Region(%s) err=%v", ip, err)
		} else {
			t.Logf("Region(%s) = %q", ip, region)
		}
	}
	if overseas, ok := s.Resolve("8.8.8.8"); !ok || overseas.Country != "United States" || overseas.Precision != protocol.PrecisionProvince {
		t.Fatalf("境外 IP 应解析到省级: %+v ok=%v", overseas, ok)
	}
	for _, ip := range []string{"127.0.0.1", "192.168.1.1", "10.0.0.1"} {
		if _, ok := s.Resolve(ip); ok {
			t.Fatalf("保留 IP %s 不应解析出位置", ip)
		}
	}
}
