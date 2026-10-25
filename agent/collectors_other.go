//go:build !windows

// 非 Windows 桌面采集器：Linux 桌面适配已于 2026-10 取消，采集端在这些平台
// 只上报心跳在线，空闲与活动采集如实降级为 unsupported。

package agent

import (
	"time"

	"haruka/protocol"
)

type stubCollector struct {
	name string
}

func (s stubCollector) Name() string           { return s.name }
func (s stubCollector) Capability() Capability { return CapUnsupported }
func (s stubCollector) Tick(now time.Time) ([]protocol.Event, error) {
	return nil, nil
}

func NewPresenceCollector(deps Deps, awayAfter time.Duration) TickCollector {
	return stubCollector{name: "presence"}
}

func NewWindowCollector(deps Deps, classifier *Classifier) TickCollector {
	return stubCollector{name: "window"}
}
