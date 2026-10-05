// Package geo 封装 ip2region.xdb 的本地查询：agent（方案 C）与服务端（方案 A）
// 共用同一套区域串解析与保留 IP 判定。xdb 为纯 Go 实现，保持无 CGO 交叉编译。
package geo

import (
	"fmt"
	"net"
	"strings"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"

	"haruka/protocol"
)

// Searcher 是已加载进内存的 xdb 查询器（并发安全）。
type Searcher struct {
	inner *xdb.Searcher
}

// NewSearcher 加载 xdb 文件（约 11 MB 常驻内存，换查询零 IO）。
func NewSearcher(dbPath string) (*Searcher, error) {
	header, err := xdb.LoadHeaderFromFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("读取 xdb 头: %w", err)
	}
	version, err := xdb.VersionFromHeader(header)
	if err != nil {
		return nil, fmt.Errorf("识别 xdb 版本: %w", err)
	}
	cBuff, err := xdb.LoadContentFromFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("加载 xdb 内容: %w", err)
	}
	inner, err := xdb.NewWithBuffer(version, cBuff)
	if err != nil {
		return nil, err
	}
	return &Searcher{inner: inner}, nil
}

// Region 返回 xdb 的原始区域串（国家|区域|省份|城市|ISP），供诊断与测试。
func (s *Searcher) Region(ipStr string) (string, error) {
	return s.inner.Search(ipStr)
}

// Resolve 把 IP 解析为脱敏的行政区划字符串。返回 false 表示
// 保留 IP（回环/内网/保留段）或解析失败——调用方应跳过而不是猜测。
func (s *Searcher) Resolve(ipStr string) (protocol.LocationData, bool) {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil || isReserved(ip) {
		return protocol.LocationData{}, false
	}
	region, err := s.inner.Search(ipStr)
	if err != nil {
		return protocol.LocationData{}, false
	}
	// v4 数据的区域串格式：国家|省份|城市|机构|国家代码，缺省段为 "0"
	// （实测：中国|浙江省|杭州市|阿里|CN；United States|California|0|Google LLC|US）。
	parts := strings.Split(region, "|")
	if len(parts) < 3 {
		return protocol.LocationData{}, false
	}
	country, province, city := parts[0], parts[1], parts[2]
	if !usable(country) {
		return protocol.LocationData{}, false
	}
	loc := protocol.LocationData{Source: "ip", Country: country}
	switch {
	case usable(city):
		// 城市级：schema 要求 province 非空且 district 为空。
		if !usable(province) {
			return protocol.LocationData{}, false
		}
		loc.Precision = protocol.PrecisionCity
		loc.Province = province
		loc.City = city
	case usable(province):
		loc.Precision = protocol.PrecisionProvince
		loc.Province = province
	default:
		loc.Precision = protocol.PrecisionCountry
	}
	return loc, true
}

func usable(s string) bool {
	return s != "" && s != "0"
}

// IsReserved 判断 IP 是否属于不应公开定位的范围。
func IsReserved(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLoopback() ||
		ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

func isReserved(ip net.IP) bool { return IsReserved(ip) }
