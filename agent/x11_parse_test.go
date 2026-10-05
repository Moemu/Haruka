//go:build linux

package agent

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// 断言：QueryInfo 应答的 MsSinceUserInput 在 [16:20]，
// [12:16] 是 MsUntilServer——二者不同（审查 P1-6 的复现场景）。
func TestParseScreensaverIdleOffsets(t *testing.T) {
	reply := make([]byte, 32)
	reply[0] = 1
	binary.LittleEndian.PutUint32(reply[8:12], 0x438)    // saver window
	binary.LittleEndian.PutUint32(reply[12:16], 600_000) // MsUntilServer（10 分钟倒计时）
	binary.LittleEndian.PutUint32(reply[16:20], 1_000)   // MsSinceUserInput（真实空闲 1 秒）

	idle, err := parseScreensaverIdle(reply)
	if err != nil {
		t.Fatalf("parseScreensaverIdle: %v", err)
	}
	if idle != 1_000 {
		t.Fatalf("idle = %d ms, want 1_000（不得读取 MsUntilServer 字段）", idle)
	}

	if _, err := parseScreensaverIdle(make([]byte, 24)); err == nil {
		t.Fatal("过短应答应报错")
	}
}

// Xauthority 解析：按显示号匹配 FamilyLocal(256) 条目与 FamilyWild(65535) 条目。
func TestLoadXauthCookie(t *testing.T) {
	type entry struct {
		family uint16
		addr   string
		number string
		name   string
		data   []byte
	}
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
	build := func(entries []entry) []byte {
		var out []byte
		for _, e := range entries {
			out = append(out, be16(e.family)...)
			out = append(out, be16(uint16(len(e.addr)))...)
			out = append(out, e.addr...)
			out = append(out, be16(uint16(len(e.number)))...)
			out = append(out, e.number...)
			out = append(out, be16(uint16(len(e.name)))...)
			out = append(out, e.name...)
			out = append(out, be16(uint16(len(e.data)))...)
			out = append(out, e.data...)
		}
		return out
	}

	cookie := make([]byte, 16)
	for i := range cookie {
		cookie[i] = byte(i + 1)
	}
	wild := make([]byte, 16)
	for i := range wild {
		wild[i] = byte(0xAA + i)
	}
	name := "MIT-MAGIC-COOKIE-1"

	file := filepath.Join(t.TempDir(), ".Xauthority")
	raw := build([]entry{
		{255, "host", "0", "XDM-AUTHORIZATION-1", cookie}, // 非目标认证名
		{255, "host", "1", name, cookie},                  // 显示号不匹配
		{65535, "host", "0", name, cookie},                // FamilyWild 命中
	})
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAUTHORITY", file)
	gotName, gotData := loadXauthCookie(0)
	if gotName != name || len(gotData) != 16 {
		t.Fatalf("应命中 FamilyWild 条目: %q %v", gotName, gotData)
	}

	raw = build([]entry{
		{256, "myhost", "1", name, cookie}, // FamilyLocal 显示号 1
		{256, "myhost", "0", name, wild},   // FamilyLocal 显示号 0
	})
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	gotName, gotData = loadXauthCookie(0)
	if gotName != name || string(gotData) != string(wild) {
		t.Fatalf("应按显示号 0 命中精确条目: %q %v", gotName, gotData)
	}

	t.Setenv("XAUTHORITY", filepath.Join(t.TempDir(), "missing"))
	if n, d := loadXauthCookie(0); n != "" || d != nil {
		t.Fatalf("无 Xauthority 时应返回空认证: %q %v", n, d)
	}
}
