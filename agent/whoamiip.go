package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// FetchEgressIP 调用自托管服务端的 GET /v1/whoami-ip（方案 C，设计案 6.3 节）。
// 服务端本来就看得见连接 IP，由它回显比自己调第三方「查 IP」接口更克制；
// 该端点不写日志、不落盘。
func FetchEgressIP(serverURL string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(serverURL + "/v1/whoami-ip")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("whoami-ip 返回 %s", resp.Status)
	}
	var body struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.IP == "" {
		return "", fmt.Errorf("whoami-ip 响应为空")
	}
	return body.IP, nil
}
