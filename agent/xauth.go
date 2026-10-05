//go:build linux

package agent

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
)

// Xauthority 认证（设计案 P2-9）：要求 MIT-MAGIC-COOKIE-1 的 X server 会拒绝
// 空认证的连接。条目格式为大端 u16 长度前缀的五个字段：
// family, address, display-number, auth-name, auth-data。
// 仅按「显示号」匹配 FamilyLocal(256)/FamilyWild(65535)——本采集器只连
// unix 套接字，TCP+IP 匹配场景不覆盖。

const (
	xauthFamilyLocal = 256
	xauthFamilyWild  = 65535
	xauthMagicName   = "MIT-MAGIC-COOKIE-1"
)

// loadXauthCookie 返回指定显示号的 magic cookie；找不到时返回空（免认证连接）。
func loadXauthCookie(displayNum int) (name string, data []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	b := raw
	for len(b) >= 2 {
		family := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < 2 {
			break
		}
		addrLen := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < addrLen+2 {
			break
		}
		addr := b[:addrLen]
		b = b[addrLen:]
		if len(b) < 2 {
			break
		}
		numLen := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < numLen+2 {
			break
		}
		numStr := string(b[:numLen])
		b = b[numLen:]
		if len(b) < 2 {
			break
		}
		nameLen := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < nameLen+2 {
			break
		}
		nameStr := string(b[:nameLen])
		b = b[nameLen:]
		if len(b) < 2 {
			break
		}
		dataLen := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < dataLen {
			break
		}
		cookie := b[:dataLen]
		b = b[dataLen:]

		if nameStr != xauthMagicName {
			continue
		}
		if family == xauthFamilyWild ||
			(family == xauthFamilyLocal && numStr == strconv.Itoa(displayNum)) {
			return xauthMagicName, cookie
		}
		_ = addr
	}
	return "", nil
}
