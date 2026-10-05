package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"haruka/protocol"
)

// Reporter 负责批量上报：内存队列批量发送，失败转 spool 落盘并退避；
// 恢复连接后的第一包是快照（snapshot=true，见设计案 5.3 节），随后按序
// 重放 spool。heartbeat 走独立通道、失败即丢（不入 spool）。
type Reporter struct {
	deps    Deps
	rt      *Runtime
	client  *http.Client
	spool   *Spool
	pauseFn func() (time.Time, bool)
	log     *slog.Logger
	debug   bool

	mu            sync.Mutex
	queue         []protocol.Event
	failStreak    int
	nextTry       time.Time
	needSnapshot  bool
	wasPaused     bool
	lastSentSpool bool

	backoffInit time.Duration
	backoffMax  time.Duration
}

func NewReporter(deps Deps, rt *Runtime, spool *Spool, pauseFn func() (time.Time, bool), log *slog.Logger) *Reporter {
	return &Reporter{
		deps:         deps,
		rt:           rt,
		client:       &http.Client{Timeout: 10 * time.Second},
		spool:        spool,
		pauseFn:      pauseFn,
		log:          log,
		needSnapshot: true,
		backoffInit:  time.Minute,
		backoffMax:   30 * time.Minute,
	}
}

// Debug 开关：把每个入队事件以 JSON 打到 stdout（开发调试用）。
func (r *Reporter) Debug(on bool) { r.debug = on }

// SetBackoff 覆盖退避参数（调试与测试用）。
func (r *Reporter) SetBackoff(init, max time.Duration) {
	r.backoffInit, r.backoffMax = init, max
}

// Enqueue 把采集器产出的事件放入内存队列；暂停期间直接丢弃（设计案 6.4 节：
// 暂停 = 停止采集缓存并清空队列）。
func (r *Reporter) Enqueue(events ...protocol.Event) {
	if len(events) == 0 {
		return
	}
	if _, paused := r.pauseFn(); paused {
		return
	}
	r.mu.Lock()
	r.queue = append(r.queue, events...)
	sort.SliceStable(r.queue, func(i, j int) bool {
		if r.queue[i].TS == r.queue[j].TS {
			return r.queue[i].Seq < r.queue[j].Seq
		}
		return r.queue[i].TS < r.queue[j].TS
	})
	var overflow []protocol.Event
	if len(r.queue) > 100 {
		overflow = r.queue[:len(r.queue)-100]
		r.queue = append([]protocol.Event(nil), r.queue[len(r.queue)-100:]...)
	}
	r.mu.Unlock()
	if len(overflow) > 0 {
		if err := r.spool.Add(overflow); err != nil {
			r.log.Error("缓冲写入失败", "count", len(overflow), "error", err)
		} else if err := r.spool.Cap(r.rt.SpoolMaxAge, r.rt.SpoolMaxMax); err != nil {
			r.log.Warn("缓冲容量清理失败", "error", err)
		}
	}
	if r.debug {
		for _, ev := range events {
			raw, _ := json.Marshal(ev)
			fmt.Printf("event %s\n", raw)
		}
	}
}

// SendHeartbeat 立即发送证活事件。失败即丢弃：heartbeat 不入 spool，
// 重放旧心跳会按新的接收时间虚增在线状态（设计案 5.3 节）。暂停期间不发。
func (r *Reporter) SendHeartbeat(ctx context.Context) error {
	if _, paused := r.pauseFn(); paused {
		return nil
	}
	ev, err := protocol.NewEvent(protocol.TypeHeartbeat, r.deps.Seq.Next(), time.Now(), protocol.HeartbeatData{})
	if err != nil {
		return err
	}
	return r.post(ctx, protocol.BuildEnvelope(r.deps.DeviceID, r.deps.DeviceKind, false, []protocol.Event{ev}))
}

// Run 周期执行 FlushOnce 直到 ctx 结束。
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.rt.Flush)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.FlushOnce(ctx); err != nil {
				r.log.Warn("上报失败", "error", err)
			}
		}
	}
}

// FlushOnce 执行一轮上报决策：暂停清理 → 快照 → spool/新观测交错 → 队列批量。
// 任何一包都不超过 BatchSize（协议单包上限 100，长积压不得整包重发）。
func (r *Reporter) FlushOnce(ctx context.Context) error {
	if _, paused := r.pauseFn(); paused {
		r.mu.Lock()
		first := !r.wasPaused
		r.wasPaused = true
		r.queue = nil
		r.mu.Unlock()
		if first {
			if err := r.spool.Purge(); err != nil {
				return fmt.Errorf("暂停期间清空 spool: %w", err)
			}
			r.log.Info("设备已暂停：清空队列与 spool，停止上报")
		}
		return nil
	}

	r.mu.Lock()
	r.wasPaused = false
	if time.Now().Before(r.nextTry) {
		r.mu.Unlock()
		return nil
	}
	start, end := 0, len(r.queue)
	if end > r.rt.BatchSize {
		end = r.rt.BatchSize
		if r.needSnapshot {
			start, end = len(r.queue)-r.rt.BatchSize, len(r.queue)
		}
	}
	take := append([]protocol.Event(nil), r.queue[start:end]...)
	r.queue = append(r.queue[:start], r.queue[end:]...)
	snapshot := r.needSnapshot && len(take) > 0
	r.mu.Unlock()

	if snapshot {
		if err := r.post(ctx, protocol.BuildEnvelope(r.deps.DeviceID, r.deps.DeviceKind, true, take)); err != nil {
			r.fail(take)
			return err
		}
		r.success()
		r.lastSentSpool = false
		r.log.Info("已发送重连快照", "events", len(take))
		return nil
	}

	// spool 与新观测交错补报：长积压不得饿死实时态（activity 的 TTL 只有
	// 10 分钟）。两者都在时按「上一包来源」交替；快照后从最老的历史补起。
	spooled, _ := r.spool.Count()
	if spooled > 0 && len(take) > 0 && r.lastSentSpool {
		return r.sendBatch(ctx, take) // 轮到新观测
	}
	if spooled > 0 {
		events, ids, err := r.spool.List(r.rt.BatchSize)
		if err != nil {
			r.Enqueue(take...)
			return fmt.Errorf("读取 spool: %w", err)
		}
		if err := r.post(ctx, protocol.BuildEnvelope(r.deps.DeviceID, r.deps.DeviceKind, false, events)); err != nil {
			r.fail(take)
			return err
		}
		if err := r.spool.Delete(ids); err != nil {
			return fmt.Errorf("删除已上报的 spool 记录: %w", err)
		}
		r.success()
		r.lastSentSpool = true
		r.log.Info("已补报积压", "events", len(events), "remaining", spooled-int64(len(events)))
		r.Enqueue(take...) // 本轮取出的新观测放回队尾，下一轮发出
		return nil
	}

	if len(take) == 0 {
		return nil
	}
	return r.sendBatch(ctx, take)
}

func (r *Reporter) sendBatch(ctx context.Context, events []protocol.Event) error {
	if err := r.post(ctx, protocol.BuildEnvelope(r.deps.DeviceID, r.deps.DeviceKind, false, events)); err != nil {
		r.fail(events)
		return err
	}
	r.success()
	return nil
}

// fail 把失败的事件转入 spool（持久化），并推进退避；同时要求下一次成功
// 发送前先补一包快照，保证实时态不依赖积压重放。
func (r *Reporter) fail(events []protocol.Event) {
	if len(events) > 0 {
		if err := r.spool.Add(events); err != nil {
			r.log.Error("spool 写入失败，事件丢失", "count", len(events), "error", err)
		}
		if err := r.spool.Cap(r.rt.SpoolMaxAge, r.rt.SpoolMaxMax); err != nil {
			r.log.Warn("缓冲容量清理失败", "error", err)
		}
	}
	r.mu.Lock()
	r.failStreak++
	d := r.backoffInit << r.failStreak
	if d <= 0 || d > r.backoffMax {
		d = r.backoffMax
	}
	r.nextTry = time.Now().Add(d)
	r.needSnapshot = true
	r.mu.Unlock()
	r.log.Info("转入退避", "streak", r.failStreak, "retry_in", d)
}

func (r *Reporter) success() {
	r.mu.Lock()
	r.failStreak = 0
	r.nextTry = time.Time{}
	r.needSnapshot = false
	r.lastSentSpool = false
	r.mu.Unlock()
}

// Stats 返回队列与 spool 的当前积压量，供 status 命令展示。
func (r *Reporter) Stats() (queued, spooled int64) {
	r.mu.Lock()
	queued = int64(len(r.queue))
	r.mu.Unlock()
	spooled, _ = r.spool.Count()
	return queued, spooled
}

func (r *Reporter) post(ctx context.Context, env protocol.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.rt.ServerURL+"/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.rt.Cfg.Token)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("ingest 返回 %s", resp.Status)
	}
	return nil
}
