package agent

import (
	"time"
)

// NewCollectors 按配置装配当前平台的采集器集合。
// 构造函数 NewPresenceCollector / NewWindowCollector 由各平台文件提供：
// Windows 必须以已登录用户会话进程运行（SCM 监督服务负责拉进会话）；
// Linux 使用 X11/EWMH，并按会话能力降级到 Wayland 空闲协议。
func NewCollectors(deps Deps, rt *Runtime) []TickCollector {
	var out []TickCollector
	if rt.Cfg.Collectors.Presence {
		out = append(out, NewPresenceCollector(deps, rt.AwayAfter))
	}
	if rt.Cfg.Collectors.Activity {
		out = append(out, NewWindowCollector(deps, NewClassifier(rt)))
	}
	// device（电量等设备自述状态）不受 collectors 开关控制：开销可忽略且无隐私面，
	// 无电池的平台如实不产出事件。
	out = append(out, NewDeviceCollector(deps))
	if rt.Cfg.Collectors.Location {
		loc := NewLocationCollector(deps, rt.Cfg.Location.Mode)
		loc.serverURL = rt.ServerURL
		loc.override = rt.Cfg.Location.Override
		loc.homeDistrict = rt.Cfg.Privacy.HomeDistrict
		if rt.Cfg.Location.Mode == "local" && rt.Cfg.Location.Override == "" {
			// 方案 C：本地解析库加载失败则降级为 degraded（doctor 如实展示），
			// 不阻断整个代理启动；手动覆盖时不需要解析库。
			if err := loc.EnableLocal(rt.Cfg.Location.LocalDB); err != nil && deps.Log != nil {
				deps.Log.Warn("本地位置解析库加载失败，location 降级", "db", rt.Cfg.Location.LocalDB, "error", err)
			}
		}
		out = append(out, loc)
	}
	return out
}

// CollectorIntervals 返回每个采集器的采样周期（presence 随心跳采样，见设计案 5.5 节）。
func CollectorIntervals(rt *Runtime) map[string]time.Duration {
	return map[string]time.Duration{
		"presence": rt.Heartbeat,
		"window":   rt.WindowTick,
		"location": rt.LocationInt,
		"device":   rt.Heartbeat,
	}
}
