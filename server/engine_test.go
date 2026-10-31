package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"haruka/protocol"
)

func discardLoggerS() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// 测试基础设施：临时库 + 可注入时钟。

type testEnv struct {
	e    *Engine
	now  time.Time
	seqs map[string]uint64
}

func newTestEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "test.db")
	if mutate != nil {
		mutate(&cfg)
	}
	rt, err := ValidateConfig(cfg)
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	db, err := OpenDB(rt.DBPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	env := &testEnv{
		now:  time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC),
		seqs: map[string]uint64{},
	}
	engine := NewEngine(&rt, db, nil, discardLoggerS())
	engine.SetClock(func() time.Time { return env.now })
	env.e = engine
	return env
}

func (te *testEnv) addDevice(t *testing.T, kind string, interval time.Duration) Device {
	t.Helper()
	policy := "server"
	d, _, err := te.e.DeviceAdd("dev-"+kind, kind, interval, policy)
	if err != nil {
		t.Fatalf("DeviceAdd: %v", err)
	}
	return d
}

// emit 构造并 ingest 一个事件；receivedAt 即当前时钟。
func (te *testEnv) emit(t *testing.T, dev Device, typ string, ts time.Time, data any) {
	t.Helper()
	te.seqs[dev.ID]++
	ev, err := protocol.NewEvent(typ, te.seqs[dev.ID], ts, data)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	te.emitRaw(t, dev, ev)
}

func (te *testEnv) emitRaw(t *testing.T, dev Device, ev protocol.Event) {
	t.Helper()
	env := protocol.BuildEnvelope(dev.ID, dev.Kind, false, []protocol.Event{ev})
	// 模拟设备从固定出口 IP 上报（方案 A 服务端派生依赖它）。
	if _, err := te.e.IngestEnvelope(dev, net.ParseIP("203.0.113.7"), &env); err != nil {
		t.Fatalf("ingest raw: %v", err)
	}
}

func (te *testEnv) heartbeat(t *testing.T, dev Device) {
	t.Helper()
	te.emit(t, dev, protocol.TypeHeartbeat, te.now, protocol.HeartbeatData{})
}

func (te *testEnv) snapshot(t *testing.T) *PublicStatus {
	t.Helper()
	snap, err := te.e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return snap
}

// 断言 1：无事件也会过期——没有新事件触发更新，字段照样按 TTL 消失。
func TestAssertion1ExpiryWithoutEvents(t *testing.T) {
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, dev)
	originalTS := te.now
	te.emit(t, dev, protocol.TypePresence, te.now, protocol.PresenceData{State: protocol.StateActive, IdleMinutes: 0})
	te.emit(t, dev, protocol.TypeActivity, te.now, protocol.ActivityData{Kind: "ide", App: "IntelliJ IDEA", Category: "coding"})
	te.emit(t, dev, protocol.TypeLocation, te.now, protocol.LocationData{Source: "ip", Precision: "city", City: "上海", Province: "上海", Country: "中国"})

	snap := te.snapshot(t)
	if snap.Activity == nil || snap.Location == nil || snap.Presence.State != protocol.StateActive {
		t.Fatalf("初始快照应完整: activity=%v location=%v presence=%+v",
			snap.Activity, snap.Location, *snap.Presence)
	}

	// activity TTL = max(2×间隔, 10 分钟) = 20 分钟：无新 activity 事件即过期消失。
	// presence 与 activity 的 TTL 公式相同，这里补一拍 presence 让两者错开，
	// 单独观察 activity 的过期（设备仍在线且 active）。
	te.now = te.now.Add(20*time.Minute + time.Second)
	secondActiveTS := te.now
	te.emit(t, dev, protocol.TypePresence, te.now, protocol.PresenceData{State: protocol.StateActive, IdleMinutes: 0})
	snap = te.snapshot(t)
	if snap.Activity != nil {
		t.Fatalf("activity 应已过期: %+v", snap.Activity)
	}
	if snap.Presence.State != protocol.StateActive || !snap.Online {
		t.Fatalf("设备仍应在线且 active: %+v", snap.Presence)
	}

	// location TTL 2 小时：过期后按设计 5.5 节保留旧值（as_of 不变，页面标注
	// 「x 小时前」），而不是随查询时刻推进。
	te.now = te.now.Add(2 * time.Hour)
	snap = te.snapshot(t)
	if snap.Location == nil || snap.Location.AsOf != formatTime(originalTS) {
		t.Fatalf("过期 location 应保留旧值并标注观测时间: %+v", snap.Location)
	}

	// 心跳窗口 = 间隔×3 = 30 分钟（自最后一次心跳起）。离线后 as_of 应指向
	// 最后一次 active 观测（last_active_at），而不是查询时刻。
	te.now = te.now.Add(time.Second)
	snap = te.snapshot(t)
	if snap.Online || snap.Presence.State != "offline" {
		t.Fatalf("设备应已离线: %+v", snap.Presence)
	}
	if snap.Presence.LastActiveAt != formatTime(secondActiveTS) || snap.Presence.AsOf != formatTime(secondActiveTS) {
		t.Fatalf("离线后 as_of 应为 last_active_at 且不随轮询推进: %+v", *snap.Presence)
	}
}

// 断言 2：旧补报不覆盖新状态。
func TestAssertion2BackfillDoesNotOverride(t *testing.T) {
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, dev)
	te.emit(t, dev, protocol.TypePresence, te.now, protocol.PresenceData{State: protocol.StateActive, IdleMinutes: 0})

	newTS := te.now
	te.emit(t, dev, protocol.TypeActivity, newTS, protocol.ActivityData{Kind: "ide", App: "GoLand", Category: "coding"})

	// 30 分钟前的积压事件现在到达：只入库，不刷新实时态。
	te.now = te.now.Add(time.Minute)
	te.emit(t, dev, protocol.TypeActivity, newTS.Add(-30*time.Minute),
		protocol.ActivityData{Kind: "ide", App: "GoLand", Category: "coding"})

	snap := te.snapshot(t)
	if snap.Activity == nil || snap.Activity.Display != "正在用 GoLand 写代码" {
		t.Fatalf("实时态应保持为较新事件: %+v", snap.Activity)
	}
	if !snap.Activity.AsOfEqual(formatTime(newTS)) {
		t.Fatalf("as_of 应为新事件的观测时间 %s: %s", formatTime(newTS), snap.Activity.AsOf)
	}

	// 同秒乱序：seq 更小的事件不得覆盖。
	te.seqs[dev.ID] = 100
	oldSeqEv, _ := protocol.NewEvent(protocol.TypeActivity, 1, newTS,
		protocol.ActivityData{Kind: "ide", App: "GoLand", Category: "coding"})
	te.emitRaw(t, dev, oldSeqEv)
	if snap = te.snapshot(t); snap.Activity.Display != "正在用 GoLand 写代码" {
		t.Fatalf("同秒低序号不得覆盖: %+v", snap.Activity)
	}
}

// AsOfEqual 辅助：比较快照字段的时间戳。
func (v *ActivityV) AsOfEqual(s string) bool { return v.AsOf == s }

// 断言 3：隐身边界阻断回填——「隐身前一分钟」与「隐身期间」的积压都不得恢复字段。
func TestAssertion3HideBoundaryBlocksBackfill(t *testing.T) {
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, dev)
	te.emit(t, dev, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "ip", Precision: "city", City: "上海", Province: "上海", Country: "中国"})

	// 14:00 隐身。
	if err := te.e.Hide(nil); err != nil {
		t.Fatal(err)
	}
	snap := te.snapshot(t)
	if snap.Visibility != "hidden" || snap.Location != nil {
		t.Fatalf("隐身后位置应立即消失: %+v", snap)
	}

	// 14:02 恢复 → resume_at = 14:02。
	te.now = te.now.Add(2 * time.Minute)
	if err := te.e.Show(); err != nil {
		t.Fatal(err)
	}

	// 14:03 收到积压：一条 13:59（隐身前一分钟）、一条 14:01（隐身期间）。
	// 都在 10 分钟新鲜度窗口内，但 ts ≤ resume_at，不得回填。
	te.now = te.now.Add(time.Minute)
	backlog1, _ := protocol.NewEvent(protocol.TypeLocation, 90,
		te.now.Add(-4*time.Minute), // 13:59
		protocol.LocationData{Source: "ip", Precision: "city", City: "北京", Province: "北京", Country: "中国"})
	te.emitRaw(t, dev, backlog1)
	backlog2, _ := protocol.NewEvent(protocol.TypeLocation, 91,
		te.now.Add(-2*time.Minute), // 14:01
		protocol.LocationData{Source: "ip", Precision: "city", City: "广州", Province: "广东", Country: "中国"})
	te.emitRaw(t, dev, backlog2)

	if snap = te.snapshot(t); snap.Location != nil {
		t.Fatalf("恢复边界之前的积压不得回填: %+v", snap.Location)
	}

	// 边界之后的新观测正常刷新。
	te.emit(t, dev, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "ip", Precision: "city", City: "深圳", Province: "广东", Country: "中国"})
	if snap = te.snapshot(t); snap.Location == nil || snap.Location.Display != "深圳" {
		t.Fatalf("边界后的新观测应正常展示: %+v", snap.Location)
	}
}

// 断言 4：多设备按规则聚合。
func TestAssertion4MultiDeviceAggregation(t *testing.T) {
	te := newTestEnv(t, nil)
	desktop := te.addDevice(t, protocol.KindDesktop, time.Minute)
	phone := te.addDevice(t, protocol.KindAndroid, 10*time.Minute)

	// 手机 away 压不住电脑 active。
	te.heartbeat(t, desktop)
	te.heartbeat(t, phone)
	te.emit(t, desktop, protocol.TypePresence, te.now, protocol.PresenceData{State: protocol.StateActive, IdleMinutes: 0})
	te.emit(t, phone, protocol.TypePresence, te.now, protocol.PresenceData{State: protocol.StateAway, IdleMinutes: 3})
	if snap := te.snapshot(t); snap.Presence.State != protocol.StateActive {
		t.Fatalf("任一 active 即 active: %+v", snap.Presence)
	}

	// 在线但无有效 presence → unknown，online 仍为 true。
	te2 := newTestEnv(t, nil)
	d2 := te2.addDevice(t, protocol.KindDesktop, time.Minute)
	te2.heartbeat(t, d2)
	snap := te2.snapshot(t)
	if !snap.Online || snap.Presence.State != "unknown" {
		t.Fatalf("在线无 presence 应为 unknown: %+v", snap.Presence)
	}

	// 全部离线。
	te2.now = te2.now.Add(5 * time.Minute)
	if snap = te2.snapshot(t); snap.Online || snap.Presence.State != "offline" {
		t.Fatalf("全部离线: %+v", snap.Presence)
	}

	// location 优先级：android 优先于 desktop，即使 desktop 的更新。
	te.emit(t, phone, protocol.TypeLocation, te.now.Add(-time.Minute),
		protocol.LocationData{Source: "ip", Precision: "city", City: "上海", Province: "上海", Country: "中国"})
	te.emit(t, desktop, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "ip", Precision: "city", City: "北京", Province: "北京", Country: "中国"})
	if snap = te.snapshot(t); snap.Location == nil || snap.Location.Display != "上海" {
		t.Fatalf("location 应取优先级高的来源: %+v", snap.Location)
	}
}

// 断言 5：manual 排序与防复活。
func TestAssertion5ManualOrderingAndTombstone(t *testing.T) {
	te := newTestEnv(t, nil)
	phone := te.addDevice(t, protocol.KindAndroid, 10*time.Minute)
	te.heartbeat(t, phone)

	// 离线 20 分钟的设置事件（超出新鲜度窗口）仍必须发布——manual 是用户操作。
	setTS := te.now.Add(-20 * time.Minute)
	te.emit(t, phone, protocol.TypeManual, setTS, protocol.ManualData{Mood: "focused", Text: "赶 deadline 中"})
	if snap := te.snapshot(t); snap.Mood == nil || snap.Mood.Mood != "focused" {
		t.Fatalf("离线设置应正常发布: %+v", snap.Mood)
	}

	// 一小时后清除（另一设备或本机）。
	te.now = te.now.Add(time.Hour)
	clearDev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, clearDev)
	te.emit(t, clearDev, protocol.TypeManual, te.now, protocol.ManualData{Clear: true})
	if snap := te.snapshot(t); snap.Mood != nil {
		t.Fatalf("清除后 mood 应消失: %+v", snap.Mood)
	}

	// 手机重放 30 分钟前的旧设置：ts ≤ cleared_ts，不得复活。
	te.emit(t, phone, protocol.TypeManual, te.now.Add(-30*time.Minute),
		protocol.ManualData{Mood: "chilling"})
	if snap := te.snapshot(t); snap.Mood != nil {
		t.Fatalf("清除墓碑之后的旧设置不得复活: %+v", snap.Mood)
	}

	// 墓碑之后的新设置正常生效。
	te.emit(t, phone, protocol.TypeManual, te.now.Add(time.Minute), protocol.ManualData{Mood: "tired"})
	if snap := te.snapshot(t); snap.Mood == nil || snap.Mood.Mood != "tired" {
		t.Fatalf("墓碑后的新设置应生效: %+v", snap.Mood)
	}
}

// 断言 6：定位策略由服务端强制执行。
func TestAssertion6LocationPolicyEnforced(t *testing.T) {
	te := newTestEnv(t, func(c *Config) {})
	// 注入 fake resolver：任何 IP 解析为上海。
	rt := te.e.rt
	db := te.e.db
	fake := fakeResolver{loc: protocol.LocationData{Source: "ip", Precision: "city", City: "上海", Province: "上海", Country: "中国"}}
	te.e = NewEngine(rt, db, fake, discardLoggerS())
	te.e.SetClock(func() time.Time { return te.now })

	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, dev)
	if snap := te.snapshot(t); snap.Location == nil || snap.Location.Display != "上海" {
		t.Fatalf("方案 A 应由服务端派生位置: %+v", snap.Location)
	}

	// 关闭定位：旧位置清除，后续心跳不再派生；位置事件仅入库、不得刷新实时态。
	if err := te.e.SetDevicePolicy(dev.ID, "off"); err != nil {
		t.Fatal(err)
	}
	// 真实流程中每次请求都会重新读设备；测试同样取最新策略。
	if d, derr := te.e.DeviceByID(dev.ID); derr != nil {
		t.Fatal(derr)
	} else {
		dev = d
	}
	if snap := te.snapshot(t); snap.Location != nil {
		t.Fatalf("off 后旧位置应清除: %+v", snap.Location)
	}
	te.now = te.now.Add(time.Minute)
	te.heartbeat(t, dev)
	if snap := te.snapshot(t); snap.Location != nil {
		t.Fatalf("off 后不得再派生位置: %+v", snap.Location)
	}
	// off 设备的 location 事件（即使新鲜）也不得刷新实时态。
	te.emit(t, dev, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "gps_coarse", Precision: "city", City: "北京", Province: "北京", Country: "中国"})
	if snap := te.snapshot(t); snap.Location != nil {
		t.Fatalf("off 设备的位置事件不得刷新实时态: %+v", snap.Location)
	}
}

type fakeResolver struct{ loc protocol.LocationData }

func (f fakeResolver) Resolve(ip string) (protocol.LocationData, bool) {
	return f.loc, true
}

// 断言 7：heartbeat 的在线窗口以接收时间为基准，事件自带 ts 不参与。
func TestAssertion7HeartbeatWindowFromReceivedAt(t *testing.T) {
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, time.Minute)

	// 心跳事件声称 ts 在一小时前：不影响在线窗口。
	hbEv, err := protocol.NewEvent(protocol.TypeHeartbeat, 1, te.now.Add(-time.Hour), protocol.HeartbeatData{})
	if err != nil {
		t.Fatal(err)
	}
	te.emitRaw(t, dev, hbEv)
	if snap := te.snapshot(t); !snap.Online {
		t.Fatalf("实时到达的心跳应使设备在线: %+v", snap.Presence)
	}

	te.now = te.now.Add(2 * time.Minute)
	if snap := te.snapshot(t); !snap.Online {
		t.Fatalf("+2m 仍在 3×60s 窗口内: %+v", snap.Presence)
	}

	// 超出在线窗口（设备已离线）后重放同一事件 id：零副作用，不得复活在线状态。
	te.now = te.now.Add(2 * time.Minute)
	if snap := te.snapshot(t); snap.Online {
		t.Fatalf("+4m 应已离线: %+v", snap.Presence)
	}
	te.emitRaw(t, dev, hbEv)
	if snap := te.snapshot(t); snap.Online || snap.Presence.State != "offline" {
		t.Fatalf("重复心跳重放不得复活在线窗口: %+v", snap.Presence)
	}

	te.now = te.now.Add(time.Minute + time.Second)
	if snap := te.snapshot(t); snap.Online || snap.Presence.State != "offline" {
		t.Fatalf("超过 3×interval 应离线: %+v", snap.Presence)
	}
}

// 断言 8：隐身恢复的两条路径都建立恢复边界。
//
//	(a) 定时隐身到期自愈：resume_at = 到期时刻，隐身期间的补报不得回填；
//	(b) keep 模式：隐身期间事件仅历史入库（心跳除外），Show 后不得公开。
func TestAssertion8HideExpirySelfHealAndKeepMode(t *testing.T) {
	// (a) 定时到期：到期后的第一条 ingest 触发自愈，隐身期间的补报被边界挡住。
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, dev)
	until := te.now.Add(time.Minute)
	if err := te.e.Hide(&until); err != nil {
		t.Fatal(err)
	}
	te.now = until.Add(30 * time.Second) // 到期 30 秒后设备才恢复连接
	te.emit(t, dev, protocol.TypeLocation, until.Add(-10*time.Second),
		protocol.LocationData{Source: "ip", Precision: "city", City: "上海", Province: "上海", Country: "中国"})
	snap := te.snapshot(t)
	if snap.Visibility != "visible" {
		t.Fatalf("到期后应自愈为 visible: %+v", snap)
	}
	if snap.Location != nil {
		t.Fatalf("隐身期间的补报不得借到期回填: %+v", snap.Location)
	}
	te.emit(t, dev, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "ip", Precision: "city", City: "北京", Province: "北京", Country: "中国"})
	if snap = te.snapshot(t); snap.Location == nil || snap.Location.Display != "北京" {
		t.Fatalf("到期后的新观测应正常展示: %+v", snap.Location)
	}

	// (b) keep 模式：隐身期间的事件只进历史，Show 后不得公开。
	te2 := newTestEnv(t, func(c *Config) { c.OnHide = "keep" })
	dev2 := te2.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te2.heartbeat(t, dev2)
	te2.emit(t, dev2, protocol.TypePresence, te2.now, protocol.PresenceData{State: protocol.StateActive, IdleMinutes: 0})
	if err := te2.e.Hide(nil); err != nil {
		t.Fatal(err)
	}
	te2.now = te2.now.Add(30 * time.Second)
	te2.emit(t, dev2, protocol.TypeActivity, te2.now,
		protocol.ActivityData{Kind: "ide", App: "GoLand", Category: "coding"})
	if err := te2.e.Show(); err != nil {
		t.Fatal(err)
	}
	te2.now = te2.now.Add(time.Minute)
	te2.heartbeat(t, dev2) // 心跳在 keep 模式下仍然续活
	snap = te2.snapshot(t)
	if snap.Visibility != "visible" {
		t.Fatalf("show 后应可见: %+v", snap)
	}
	if !snap.Online {
		t.Fatalf("keep 模式心跳应维持在线: %+v", snap.Presence)
	}
	if snap.Activity != nil {
		t.Fatalf("隐身期间的观测不得在 Show 后公开: %+v", snap.Activity)
	}
	te2.emit(t, dev2, protocol.TypeActivity, te2.now,
		protocol.ActivityData{Kind: "ide", App: "GoLand", Category: "coding"})
	if snap = te2.snapshot(t); snap.Activity == nil || snap.Activity.Display != "正在用 GoLand 写代码" {
		t.Fatalf("Show 后的新观测应正常展示: %+v", snap.Activity)
	}
}

// DeviceEdit：指定字段更新、其余保持、空编辑与未知设备报错、已吊销设备不可编辑。
func TestDeviceEdit(t *testing.T) {
	te := newTestEnv(t, nil)
	d := te.addDevice(t, protocol.KindDesktop, time.Minute)

	got, err := te.e.DeviceEdit(d.ID, "yukigakure", 5*time.Minute, "local")
	if err != nil {
		t.Fatalf("DeviceEdit: %v", err)
	}
	if got.Name != "yukigakure" || got.Interval != 5*time.Minute || got.LocationPolicy != "local" || got.Kind != "desktop" {
		t.Fatalf("字段未按预期更新: %+v", got)
	}
	again, err := te.e.DeviceByID(d.ID)
	if err != nil || again.Name != "yukigakure" || again.Interval != 5*time.Minute {
		t.Fatalf("未落库或读回不一致: %+v err=%v", again, err)
	}

	if _, err := te.e.DeviceEdit(d.ID, "", 0, ""); err == nil {
		t.Fatal("空编辑应报错")
	}
	if _, err := te.e.DeviceEdit("d_none0000", "x", 0, ""); err == nil {
		t.Fatal("未知设备应报错")
	}
	if _, err := te.e.DeviceEdit(d.ID, "", 0, "bogus"); err == nil {
		t.Fatal("非法 policy 应报错")
	}

	if err := te.e.DeviceRevoke(d.ID); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}
	if _, err := te.e.DeviceEdit(d.ID, "x", 0, ""); err == nil {
		t.Fatal("已吊销设备应不可编辑")
	}
}

// 设备电量聚合与分类动词：device 事件只在设备在线且未过期时出现在公开快照。
func TestDeviceStatusAndCategoryVerbs(t *testing.T) {
	te := newTestEnv(t, nil)
	phone := te.addDevice(t, protocol.KindAndroid, 10*time.Minute)
	te.heartbeat(t, phone)
	te.emit(t, phone, protocol.TypePresence, te.now, protocol.PresenceData{State: protocol.StateActive, IdleMinutes: 0})
	te.emit(t, phone, protocol.TypeActivity, te.now, protocol.ActivityData{Kind: "app", App: "B 站", Category: "media"})
	te.emit(t, phone, protocol.TypeDevice, te.now, protocol.DeviceData{BatteryLevel: 85, Charging: true, Steps: 4317})
	pc := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, pc) // 台式机不发 device 事件

	snap := te.snapshot(t)
	if snap.Activity == nil || snap.Activity.Display != "正在刷 B 站" {
		t.Fatalf("media 分类动词: %+v", snap.Activity)
	}
	if len(snap.Devices) != 1 || snap.Devices[0].Kind != "android" || snap.Devices[0].Name != "dev-android" ||
		snap.Devices[0].Battery == nil ||
		snap.Devices[0].Battery.Level != 85 || !snap.Devices[0].Battery.Charging || snap.Devices[0].Steps != 4317 {
		t.Fatalf("设备电量聚合: %+v", snap.Devices)
	}

	te.now = te.now.Add(21 * time.Minute)
	if snap = te.snapshot(t); len(snap.Devices) != 0 {
		t.Fatalf("过期 device 事件应从快照消失: %+v", snap.Devices)
	}
}

// zone 决定公开展示（school 与 home 同权），白名单外的 zone 在 ingest 拒收。
func TestLocationZoneDisplayAndAllowlist(t *testing.T) {
	te := newTestEnv(t, nil)
	phone := te.addDevice(t, protocol.KindAndroid, 10*time.Minute)
	te.emit(t, phone, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "gps_coarse", Precision: "district",
			Country: "中国", Province: "广西", City: "柳州", District: "城中区", Zone: protocol.ZoneSchool})
	snap := te.snapshot(t)
	if snap.Location == nil || snap.Location.Display != "在学校" {
		t.Fatalf("zone=school 应展示「在学校」: %+v", snap.Location)
	}

	te.emit(t, phone, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "gps_coarse", Precision: "district",
			Country: "中国", Province: "广西", City: "柳州", District: "鹿寨县", Zone: protocol.ZoneHome})
	if snap = te.snapshot(t); snap.Location == nil || snap.Location.Display != "在家" {
		t.Fatalf("zone=home 应展示「在家」: %+v", snap.Location)
	}

	te.seqs[phone.ID]++
	bad, err := protocol.NewEvent(protocol.TypeLocation, te.seqs[phone.ID], te.now,
		protocol.LocationData{Source: "gps_coarse", Precision: "district",
			Country: "中国", Province: "广西", City: "柳州", District: "鹿寨县", Zone: "office"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	env := protocol.BuildEnvelope(phone.ID, phone.Kind, false, []protocol.Event{bad})
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := ValidateEnvelope(body); !errors.Is(err, errValidation) {
		t.Fatalf("白名单外的 zone 应被拒收: %v", err)
	}
}

// source=manual（采集端 location.override 手动覆盖）走城市展示，zone 命中仍优先。
func TestLocationManualSource(t *testing.T) {
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.emit(t, dev, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "manual", Precision: "city", City: "杭州"})
	if snap := te.snapshot(t); snap.Location == nil || snap.Location.Display != "杭州" {
		t.Fatalf("manual 城市应原样展示: %+v", snap.Location)
	}
	te.emit(t, dev, protocol.TypeLocation, te.now.Add(time.Minute),
		protocol.LocationData{Source: "manual", Precision: "city", City: "杭州", Zone: protocol.ZoneHome})
	if snap := te.snapshot(t); snap.Location == nil || snap.Location.Display != "在家" {
		t.Fatalf("manual + zone=home 应展示「在家」: %+v", snap.Location)
	}
}
