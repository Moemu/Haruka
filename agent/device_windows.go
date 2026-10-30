//go:build windows

package agent

import (
	"fmt"
	"time"
	"unsafe"

	"haruka/protocol"
)

// DeviceCollector 读取电源状态（电量百分比、是否在充电）。台式机无电池时不产出
// 事件，状态页也就不展示该设备。
type DeviceCollector struct {
	deps Deps
}

func NewDeviceCollector(deps Deps) *DeviceCollector { return &DeviceCollector{deps: deps} }

func (c *DeviceCollector) Name() string           { return "device" }
func (c *DeviceCollector) Capability() Capability { return CapOK }

// systemPowerStatus 与 winbase.SYSTEM_POWER_STATUS 对应。
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte // 位域：1 高 2 低 4 临界 8 充电中 128 无电池 255 未知
	BatteryLifePercent  byte // 0-100，255 未知
	Reserved            byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func (c *DeviceCollector) Tick(now time.Time) ([]protocol.Event, error) {
	var ps systemPowerStatus
	if r1, _, callErr := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&ps))); r1 == 0 {
		return nil, fmt.Errorf("GetSystemPowerStatus: %v", callErr)
	}
	if ps.BatteryFlag&128 != 0 || ps.BatteryLifePercent == 255 {
		return nil, nil
	}
	ev, err := protocol.NewEvent(protocol.TypeDevice, c.deps.Seq.Next(), now, protocol.DeviceData{
		BatteryLevel: int(ps.BatteryLifePercent),
		Charging:     ps.BatteryFlag&8 != 0,
	})
	if err != nil {
		return nil, err
	}
	return []protocol.Event{ev}, nil
}
