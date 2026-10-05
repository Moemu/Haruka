// haruka-server 是 Haruka 服务端：ingest、聚合快照与控制接口。
// 管理类操作只通过本机 CLI 完成（直连 SQLite），没有管理类 HTTP 接口。
package main

import (
	"os"

	"haruka/server"
)

func main() {
	if err := server.RunCLI(os.Args[1:]); err != nil {
		os.Stderr.WriteString("错误: " + err.Error() + "\n")
		os.Exit(1)
	}
}
