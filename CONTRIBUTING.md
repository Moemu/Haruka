# 参与 Haruka

欢迎修复问题、补充平台验证和完善使用说明。提交内容按 Apache-2.0 提供。

## 本地开发

安装 `go.mod` 指定的 Go 版本，然后运行：

```sh
go mod download
go vet ./...
go test -count=1 ./...
go build ./...
```

在 Linux 上还需运行 `go test -race -count=1 ./agent ./server`。修改平台代码时检查 Windows amd64 构建；桌面采集器仅支持 Windows，服务端保持 Linux amd64 可构建。

`cmd/mockserver` 是联调工具，不能代替生产服务端。测试使用生成的临时令牌和合成状态。

## 提交要求

- 说明触发条件、改动后的行为和验证方法。
- 新平台能力区分可用、故障和明确不支持。
- 协议字段变更同步更新类型、JSON Schema、示例和回归测试。
- 保持本机脱敏。窗口标题、原始路径、IP 和精确坐标不得进入事件历史或公开状态快照。
- 用脱敏的日志与合成数据报告问题。配置、数据库、令牌、个人项目名和设备路径留在本机。

本地 `docs/`、`output/`、`tmp-dev/` 和构建产物不进入仓库。提交前用 `git diff --cached` 检查公开内容。

## 安全问题

按 [SECURITY.md](SECURITY.md) 的方式私下报告漏洞。公开 Issue 适合一般错误和功能建议。
