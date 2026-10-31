package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 日志轮转：写满 max 后当前文件改名为 .1（旧轮转覆盖），新内容写进新建的当前文件。

func TestRotateWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haruka.log")
	w, err := openRotateWriter(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 60) + "\n" // 61 字节
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(line)); err != nil { // 122 > 100 → 轮转
		t.Fatal(err)
	}
	first, err := os.ReadFile(path + ".1")
	if err != nil || string(first) != line {
		t.Fatalf("旧内容应在 .1: %v %q", err, first)
	}
	if _, err := w.Write([]byte(line)); err != nil { // 再次轮转，.1 被覆盖
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path + ".1")
	current, _ := os.ReadFile(path)
	if string(second) != line || string(current) != line {
		t.Fatalf("轮转后 .1 应为上一份、当前文件应为最新一份: %q / %q", second, current)
	}

	// 重启场景：追加打开时已有内容计入 size，不因 61 < 100 而漏轮转。
	w2, err := openRotateWriter(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Write([]byte(line)); err != nil { // 61+61 > 100 → 轮转
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(len(line)) {
		t.Fatalf("重启后轮转应清空当前文件: %v %d", err, info.Size())
	}
	w.f.Close()
	w2.f.Close()
}
