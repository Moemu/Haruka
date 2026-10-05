package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"haruka/protocol"
)

type schedulerTask struct {
	collector TickCollector
	interval  time.Duration
	paused    bool // 每任务独立的暂停状态（并发任务间不共享）
}

// PauseAware 由持有后台常驻资源（如 Wayland 事件流连接）的采集器实现：
// 暂停时关闭资源、恢复时允许重建——「暂停 = 停止全部采集」包括后台读事件。
type PauseAware interface {
	PauseCollector()
	ResumeCollector()
}

// Scheduler 按各自周期驱动采集器；单个采集器的 panic 或错误不影响其他采集器。
// pauseFn 在每轮采样前检查：暂停期间完全停止采集（设计案 6.4 节——暂停不只是
// 停止上报，读取窗口标题、输入状态、whoami-ip 都属于采集）。
type Scheduler struct {
	tasks   []schedulerTask
	out     chan<- protocol.Event
	log     *slog.Logger
	pauseFn func() bool
}

func NewScheduler(out chan<- protocol.Event, log *slog.Logger, pauseFn func() bool) *Scheduler {
	if pauseFn == nil {
		pauseFn = func() bool { return false }
	}
	return &Scheduler{out: out, log: log, pauseFn: pauseFn}
}

func (s *Scheduler) Add(c TickCollector, interval time.Duration) {
	s.tasks = append(s.tasks, schedulerTask{collector: c, interval: interval})
}

func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range s.tasks {
		wg.Add(1)
		go func(t schedulerTask) {
			defer wg.Done()
			s.runTask(ctx, &t)
		}(t)
	}
	wg.Wait()
}

func (s *Scheduler) runTask(ctx context.Context, t *schedulerTask) {
	if pa, ok := t.collector.(PauseAware); ok {
		defer pa.PauseCollector()
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("采集器 panic，已终止该采集器", "collector", t.collector.Name(), "panic", r)
		}
	}()
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.tick(ctx, t, now)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, t *schedulerTask, now time.Time) {
	if s.pauseFn() {
		if !t.paused {
			t.paused = true
			s.log.Info("暂停中：停止全部采集（窗口标题、输入状态、定位均属采集）", "collector", t.collector.Name())
			if pa, ok := t.collector.(PauseAware); ok {
				pa.PauseCollector()
			}
		}
		return
	}
	if t.paused {
		t.paused = false
		s.log.Info("恢复采集", "collector", t.collector.Name())
		if pa, ok := t.collector.(PauseAware); ok {
			pa.ResumeCollector()
		}
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("采集器单次采样 panic", "collector", t.collector.Name(), "panic", r)
		}
	}()
	events, err := t.collector.Tick(now)
	if err != nil {
		s.log.Warn("采集失败", "collector", t.collector.Name(), "error", err)
		return
	}
	for _, ev := range events {
		select {
		case s.out <- ev:
		case <-ctx.Done():
			return
		}
	}
}
