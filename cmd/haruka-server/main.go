// haruka-server 是 Haruka 服务端：ingest、聚合快照与控制接口。
// 管理类操作只通过本机 CLI 完成（直连 SQLite），没有管理类 HTTP 接口。
//
// 本文件是编译型插件的唯一接线点：新增插件 = 新建 internal/plugins/<名>
// 包（init 自注册）+ 在此加一行空导入；核心代码零改动。
package main

import (
	"os"

	_ "haruka/internal/plugins/mood"
	_ "haruka/internal/plugins/persona"
	_ "haruka/internal/plugins/wakatime"

	"haruka/server"
)

func main() {
	if err := server.RunCLI(os.Args[1:]); err != nil {
		os.Stderr.WriteString("错误: " + err.Error() + "\n")
		os.Exit(1)
	}
}
