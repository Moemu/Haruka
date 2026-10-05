package agent

import (
	"time"

	"haruka/protocol"
)

// Capability 表达采集器在当前平台会话中的可用程度，
// doctor 与状态页都要如实展示，不满足时宁可少报。
type Capability string

const (
	CapOK          Capability = "ok"
	CapDegraded    Capability = "degraded"
	CapUnsupported Capability = "unsupported"
)

// Collector 是全部采集器的最小接口。
type Collector interface {
	Name() string
	Capability() Capability
}

// TickCollector 按固定周期采样，返回零或多个待上报事件。
type TickCollector interface {
	Collector
	Tick(now time.Time) ([]protocol.Event, error)
}
