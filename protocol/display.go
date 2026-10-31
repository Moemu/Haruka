package protocol

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// CheckDisplayString 校验可展示字符串的底线规则：长度上限、禁止 URL、
// 坐标、路径与 IP、控制字符——合法字段里塞路径/坐标是真实风险（设计案 11 节）。
// 服务端 ingest 拒收与采集端配置预检共用，保证端侧预检与服务端口径一致，
// 配置值不会被服务端整包拒收卡死批次。
func CheckDisplayString(field, v string, maxRunes int) error {
	if v == "" {
		return nil
	}
	if len([]rune(v)) > maxRunes {
		return fmt.Errorf("%s 过长", field)
	}
	if strings.Contains(v, "://") {
		return fmt.Errorf("%s 禁止包含 URL", field)
	}
	if isCoordPair(v) {
		return fmt.Errorf("%s 疑似坐标", field)
	}
	if strings.ContainsAny(v, `/\`) || ipv4Pattern.MatchString(v) {
		return fmt.Errorf("%s 疑似路径或 IP", field)
	}
	for _, token := range ipv6Pattern.FindAllString(v, -1) {
		if net.ParseIP(token) != nil {
			return fmt.Errorf("%s 疑似 IP", field)
		}
	}
	for _, c := range v {
		if unicode.IsControl(c) {
			return fmt.Errorf("%s 含控制字符", field)
		}
	}
	return nil
}

// isCoordPair 识别「31.19, 121.43」形态的经纬度对。
var ipv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var ipv6Pattern = regexp.MustCompile(`(?:[0-9a-fA-F]{0,4}:){2,}[0-9a-fA-F:.]*`)
var coordinatePattern = regexp.MustCompile(`([-+]?\d{1,3}(?:\.\d+)?)\s*[,，]\s*([-+]?\d{1,3}(?:\.\d+)?)`)

func isCoordPair(s string) bool {
	for _, pair := range coordinatePattern.FindAllStringSubmatch(s, -1) {
		lat, _ := strconv.ParseFloat(pair[1], 64)
		lon, _ := strconv.ParseFloat(pair[2], 64)
		if lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			return true
		}
	}
	return false
}
