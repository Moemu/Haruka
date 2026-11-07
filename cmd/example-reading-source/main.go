// example-reading-source 是声明型插件的示例来源程序（docs/plugin-dev.md 的
// 活样例）：每 5 分钟向服务端推一条 custom:reading 事件，配合服务端配置
// 顶层 reading 节（title/fields），状态页即自动出现「正在读」卡。
//
// 任意语言可写同类来源程序——只需 device token 与 ingest API：
//
//	POST /v1/ingest
//	{"v":1,"device_id":"…","device_kind":"desktop","sent_at":"…",
//	 "events":[{"id":"e_…","seq":1,"ts":"…","type":"custom",
//	            "data":{"source":"reading","payload":{"title":"…"}}}]}
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"haruka/protocol"
)

func main() {
	server := flag.String("server", "http://127.0.0.1:8080", "服务端地址")
	deviceID := flag.String("device", "", "设备 ID（与令牌同一次 device add 签发）")
	token := flag.String("token", "", "设备令牌（haruka-server device add 签发）")
	interval := flag.Duration("interval", 5*time.Minute, "推送间隔")
	flag.Parse()
	if *token == "" || *deviceID == "" {
		log.Fatal("必须提供 --device 与 --token（haruka-server device add 签发）")
	}

	books := []map[string]any{
		{"title": "三体", "author": "刘慈欣", "progress": 0.42},
		{"title": "雪国", "author": "川端康成", "progress": 0.81},
	}
	var seq uint64
	client := &http.Client{Timeout: 10 * time.Second}
	for tick := 0; ; tick = (tick + 1) % len(books) {
		seq++
		ev, err := protocol.NewEvent(protocol.TypeCustom, seq, time.Now(),
			protocol.CustomData{Source: "reading", Payload: mustJSON(books[tick])})
		if err != nil {
			log.Fatal(err)
		}
		env := protocol.BuildEnvelope(*deviceID, protocol.KindDesktop, false, []protocol.Event{ev})
		body, _ := json.Marshal(env)
		resp, err := client.Post(*server+"/v1/ingest", "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("推送失败，下一拍重试: %v", err)
		} else {
			resp.Body.Close()
			log.Printf("已推送: %s（HTTP %d）", books[tick]["title"], resp.StatusCode)
		}
		time.Sleep(*interval)
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("payload 序列化失败: %v", err))
	}
	return raw
}
