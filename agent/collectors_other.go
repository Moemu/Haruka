//go:build !windows && !linux

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
