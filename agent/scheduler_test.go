package agent

import (
	"testing"
	"time"

	"haruka/protocol"
)

// 断言（审查 P1-7）：暂停期间调度器完全停止采集，恢复后重新采样。
type tickCounter struct {
	n int
}

func (c *tickCounter) Name() string           { return "counter" }
func (c *tickCounter) Capability() Capability { return CapOK }
func (c *tickCounter) Tick(now time.Time) ([]protocol.Event, error) {
	c.n++
	return nil, nil
}

// pauseAwareRecorder 记录暂停/恢复钩子调用（P2：暂停必须覆盖后台常驻采集）。
type pauseAwareRecorder struct {
	tickCounter
	pauses, resumes int
}

func (c *pauseAwareRecorder) PauseCollector()  { c.pauses++ }
func (c *pauseAwareRecorder) ResumeCollector() { c.resumes++ }

func TestSchedulerPausesCollection(t *testing.T) {
	paused := false
	s := NewScheduler(nil, discardLogger(), func() bool { return paused })
	c := &tickCounter{}
	task := &schedulerTask{collector: c}
	now := time.Now()

	for i := 0; i < 3; i++ {
		s.tick(nil, task, now)
	}
	if c.n != 3 {
		t.Fatalf("未暂停时应正常采样: %d", c.n)
	}

	paused = true
	for i := 0; i < 3; i++ {
		s.tick(nil, task, now)
	}
	if c.n != 3 {
		t.Fatalf("暂停期间不得采集（窗口标题、输入状态、whoami-ip 都属于采集）: %d", c.n)
	}

	paused = false
	s.tick(nil, task, now)
	if c.n != 4 {
		t.Fatalf("恢复后应重新采样: %d", c.n)
	}
}

// 断言（切片 6 P2）：暂停/恢复时通知持有后台资源的采集器——进入暂停只
// 关闭一次，恢复只重建一次，且暂停期间反复 tick 不重复触发。
func TestSchedulerPauseHooksFireOnce(t *testing.T) {
	paused := false
	s := NewScheduler(nil, discardLogger(), func() bool { return paused })
	c := &pauseAwareRecorder{}
	task := &schedulerTask{collector: c}
	now := time.Now()

	paused = true
	for i := 0; i < 3; i++ {
		s.tick(nil, task, now)
	}
	if c.pauses != 1 || c.resumes != 0 {
		t.Fatalf("进入暂停应恰好 Pause 一次（pauses=%d resumes=%d）", c.pauses, c.resumes)
	}

	paused = false
	s.tick(nil, task, now)
	s.tick(nil, task, now)
	if c.pauses != 1 || c.resumes != 1 {
		t.Fatalf("恢复应恰好 Resume 一次（pauses=%d resumes=%d）", c.pauses, c.resumes)
	}
}
