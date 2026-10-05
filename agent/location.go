package agent

import (
	"time"

	"haruka/internal/geo"
	"haruka/protocol"
)

// LocationCollector 按配置的三种方案工作（设计案 6.3 节）：
//   - server（默认）：本机不采集，服务端从连接 IP 解析后立即丢弃，不产生事件；
//   - local（方案 C）：向自托管服务端要自己的出口 IP（whoami-ip），本地 xdb
//     查询为行政区划字符串后上报，只上报区划名；
//   - off：不采集，服务端策略同步强制执行。
type LocationCollector struct {
	deps      Deps
	mode      string
	serverURL string
	geo       *geo.Searcher
	policy    *PolicySync
}

func NewLocationCollector(deps Deps, mode string) *LocationCollector {
	return &LocationCollector{deps: deps, mode: mode}
}

// EnableLocal 为方案 C 装配本地解析库；加载失败返回错误（fail-fast）。
func (c *LocationCollector) EnableLocal(xdbPath string) error {
	s, err := geo.NewSearcher(xdbPath)
	if err != nil {
		return err
	}
	c.geo = s
	return nil
}

func (c *LocationCollector) Name() string { return "location" }

func (c *LocationCollector) Capability() Capability {
	switch c.mode {
	case "server":
		return CapOK
	case "local":
		if c.geo != nil {
			return CapOK
		}
		return CapDegraded
	default:
		return CapUnsupported
	}
}

func (c *LocationCollector) Tick(now time.Time) ([]protocol.Event, error) {
	if c.mode != "local" || c.geo == nil {
		return nil, nil
	}
	// 策略必须已被服务端确认为 local 才发送本地解析的位置——
	// 否则服务端可能仍在做 IP 派生，形成双源（设计案 6.3 节）。
	if c.policy == nil || c.policy.Confirmed() != "local" {
		return nil, nil
	}
	ipStr, err := FetchEgressIP(c.serverURL, 5*time.Second)
	if err != nil {
		return nil, err
	}
	loc, ok := c.geo.Resolve(ipStr)
	if !ok {
		// 保留 IP（回环/内网/保留段）或解析失败：不上报，不猜测。
		return nil, nil
	}
	ev, err := protocol.NewEvent(protocol.TypeLocation, c.deps.Seq.Next(), now, loc)
	if err != nil {
		return nil, err
	}
	return []protocol.Event{ev}, nil
}
