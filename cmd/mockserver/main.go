// mockserver 是开发专用的 ingest 接收端：实现 /v1/ingest、/v1/device-policy
// 与 /healthz 的最小说明，用于 M1 切片的端到端联调，不是 M0 交付物。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"haruka/protocol"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "监听地址")
	failFirst := flag.Int("fail-first", 0, "前 N 次 /v1/ingest 返回 500（模拟服务故障，测试 spool）")
	flag.Parse()

	var served, failed atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if *failFirst > 0 && failed.Load() < int64(*failFirst) {
			failed.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, "simulated outage")
			return
		}
		var env protocol.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, "bad envelope:", err)
			return
		}
		snapshot := ""
		if env.Snapshot {
			snapshot = " [snapshot]"
		}
		for _, ev := range env.Events {
			data, _ := json.Marshal(ev.Data)
			log.Printf("ingest device=%s%s %s ts=%s seq=%d data=%s",
				env.DeviceID, snapshot, ev.Type, ev.TS, ev.Seq, data)
		}
		served.Add(int64(len(env.Events)))
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "{\"accepted\": %d}\n", len(env.Events))
	})
	mux.HandleFunc("POST /v1/device-policy", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		log.Printf("device-policy: %v", body)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{\"ok\": true}")
	})

	log.Printf("mockserver 监听 %s（fail-first=%d）", *addr, *failFirst)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
