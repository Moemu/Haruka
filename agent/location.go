package agent

import (
	"time"

	"haruka/internal/geo"
	"haruka/protocol"
)

// LocationCollector 按配置的三种方案工作（设计案 6.3 节）：
//   - server（默认）：本机不采集，服务端从连接 IP 解析后立即丢弃，不产生事件；
//   - local（方案 C）：向自托管服务端要自己的出口 IP（whoami-ip），本地 xdb
//     查询为行政区划字符串后上报，只上报区划名；配置 override 时跳过解析，
//     直接上报手动指定的常驻城市（source=manual）；
//   - off：不采集，服务端策略同步强制执行。
type LocationCollector struct {
	deps         Deps
	mode         string
	serverURL    string
	override     string // 非空时手动指定常驻城市，不走 IP 解析
	homeDistrict string // 非空时与覆盖/解析结果匹配，命中附 zone=home
	geo          *geo.Searcher
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
		if c.geo != nil || c.override != "" {
			return CapOK
		}
		return CapDegraded
	default:
		return CapUnsupported
	}
}

func (c *LocationCollector) Tick(now time.Time) ([]protocol.Event, error) {
	if c.mode != "local" {
		return nil, nil
	}
	// 策略必须已被服务端确认为 local 才发送本地位置——
	// 否则服务端可能仍在做 IP 派生，形成双源（设计案 6.3 节）。
	if c.deps.Policy == nil || c.deps.Policy.Confirmed() != "local" {
		return nil, nil
	}
	var loc protocol.LocationData
	if c.override != "" {
		// 手动覆盖：不取出口 IP、不查解析库，断网也如实上报指定城市。
		loc = protocol.LocationData{
			Source:    "manual",
			Precision: protocol.PrecisionCity,
			City:      c.override,
		}
	} else {
		if c.geo == nil {
			return nil, nil
		}
		ipStr, err := FetchEgressIP(c.serverURL, 5*time.Second)
		if err != nil {
			return nil, err
		}
		var ok bool
		loc, ok = c.geo.Resolve(ipStr)
		if !ok {
			// 保留 IP（回环/内网/保留段）或解析失败：不上报，不猜测。
			return nil, nil
		}
	}
	applyHomeZone(&loc, c.homeDistrict)
	ev, err := protocol.NewEvent(protocol.TypeLocation, c.deps.Seq.Next(), now, loc)
	if err != nil {
		return nil, err
	}
	return []protocol.Event{ev}, nil
}

// applyHomeZone 家区命中：override 与 IP 解析共用同一判定（设计案 6.3 节 zone）。
// 桌面解析库没有区县数据，实际能命中的是市/省名；区县级判定只有 Android 有。
func applyHomeZone(loc *protocol.LocationData, homeDistrict string) {
	if homeDistrict == "" {
		return
	}
	if loc.District == homeDistrict || loc.City == homeDistrict || loc.Province == homeDistrict {
		loc.Zone = protocol.ZoneHome
	}
}
