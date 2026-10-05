package server

import (
	"haruka/internal/geo"
	"haruka/protocol"
)

// XDBResolver 是方案 A 的服务端位置解析器（设计案 6.3 节）：
// 从设备连接 IP 就地解析市/区，解析完立即丢弃 IP，不落盘、日志只记哈希。
type XDBResolver struct {
	geo *geo.Searcher
}

func NewXDBResolver(xdbPath string) (*XDBResolver, error) {
	s, err := geo.NewSearcher(xdbPath)
	if err != nil {
		return nil, err
	}
	return &XDBResolver{geo: s}, nil
}

func (r *XDBResolver) Resolve(ip string) (protocol.LocationData, bool) {
	return r.geo.Resolve(ip)
}
