package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// Deps 是采集器与上报器的公共依赖。
type Deps struct {
	DeviceID   string
	DeviceKind string
	Seq        *Seq
	Log        *slog.Logger
	// Policy 是定位策略同步器；location 采集器在策略确认前不发送本地事件。
	Policy *PolicySync
}

// Seq 分配设备内单调递增的事件序号，持久化到状态目录。
// 进程崩溃最多丢失一次持久化（重启后从上次落盘值继续，不会回退）。
type Seq struct {
	mu   chan struct{}
	n    uint64
	path string
}

func OpenSeq(stateDir string) (*Seq, error) {
	s := &Seq{mu: make(chan struct{}, 1), path: filepath.Join(stateDir, "seq.json")}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		var st struct {
			N uint64 `json:"seq"`
		}
		if json.Unmarshal(raw, &st) == nil {
			s.n = st.N
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Next 返回下一个序号并落盘。落盘失败不阻塞采集（记入日志），极端情况是
// 崩溃后重用序号——服务端还有 (ts, seq) 排序与事件 ID 幂等两道防线。
func (s *Seq) Next() uint64 {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	s.n++
	raw, _ := json.Marshal(struct {
		N uint64 `json:"seq"`
	}{s.n})
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		os.Rename(tmp, s.path)
	}
	return s.n
}

// Peek 返回当前序号（不递增），供 status 命令展示。
func (s *Seq) Peek() uint64 {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.n
}
