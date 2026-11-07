// Package host 是核心与数据源插件之间的唯一稳定契约（设计案 §7.7）。
//
// 核心只认识本包定义的插件系统，不认识任何具体插件：编译型插件在自己的
// 包里 init() 调用 Register 自注册，由 cmd/haruka-server 的空导入接线；
// 声明型插件（无服务端代码，数据来自同名 custom 事件）由核心按同一契约
// 构造。两条实现来源共用同一装载、校验、运行与清单路径，核心没有第二
// 条放行通道。
package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// NamePattern 是插件名的合法形态，同时是配置节名、快照顶层键与
// custom 事件 source 的统一身份规则。
var NamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ValidName 报告 name 是否满足统一身份规则。
func ValidName(name string) bool { return NamePattern.MatchString(name) }

// Host 是核心提供给插件的运行时能力面。
type Host interface {
	DB() *sql.DB
	Logger() *slog.Logger
	Now() time.Time
	// Config 返回 yml 中与本插件同名的顶层配置节；缺节返回 nil。
	Config(name string) *yaml.Node
}

// Plugin 是数据源插件。Name 即配置节名、快照顶层节名与 custom 事件 source。
type Plugin interface {
	Name() string
	Enabled() bool
}

// Validator 由需要配置的插件实现：Enabled()=true 时启动期调用，报错则
// 拒绝启动（三态语义：启用但无效绝不静默降级为「事实上禁用」）。
type Validator interface {
	Validate() error
}

// Runner 是拉取型插件的后台采集循环；仅 Enabled 时被启动，阻塞至 ctx 结束。
type Runner interface {
	Run(ctx context.Context)
}

// Delivered 是派发给 EventHandler 的一条已去重入库的 custom 事件。
// 核心已统一执行隐私边界（resume_at）与时钟护栏；新鲜度语义由插件自决。
type Delivered struct {
	DeviceID   string
	TS         time.Time
	ReceivedAt time.Time
	Source     string
	Payload    json.RawMessage
}

// EventHandler 订阅 custom 事件，在 ingest 事务内被调用。返回错误 = 该事件
// 被拒收，错误信息原样反馈到上报端（须可读、可指导修复）。
type EventHandler interface {
	Sources() []string
	HandleEvent(ev Delivered, q Queryer) error
}

// Contributor 向公开快照贡献一个顶层 JSON 节（键 = SnapshotKey，约定 =
// 插件名）。在快照读事务内调用；返回 nil = 本拍无数据（节缺席，前端不出
// 卡）；返回错误 = 核心记日志跳过，不拖垮页面。
type Contributor interface {
	SnapshotKey() string
	ContributeSnapshot(q Queryer, now time.Time) (json.RawMessage, error)
}

// Queryer 是插件在核心事务/直连上的 SQL 访问面。EventHandler 可读写；
// Contributor 只读使用。*sql.DB 与 *sql.Tx 均满足本接口。
type Queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// Field 描述快照节中一个可展示字段，前端按清单渲染（format 见状态页约定）。
type Field struct {
	Key    string            `json:"key"`
	Label  string            `json:"label,omitempty"`
	Format string            `json:"format,omitempty"` // text | number | duration | duration_total | time
	Labels map[string]string `json:"labels,omitempty"`
}

// Descriptor 是 capabilities 清单里的插件描述。Title/Icon/Fields 公开
// （状态页渲染所需最少信息）；Choices/Settings 是私有槽位，仅在携带
// 设备/owner 令牌时输出。
type Descriptor struct {
	Title    string   `json:"title,omitempty"`
	Icon     string   `json:"icon,omitempty"`
	Fields   []Field  `json:"fields,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Settings any      `json:"settings,omitempty"`
}

// Describer 由参与 capabilities 清单的插件实现。
type Describer interface {
	Describe() Descriptor
}

var factories []func(Host) (Plugin, error)

// Register 注册编译型插件构造器（插件包 init() 调用，每个插件一次）。
// 构造器可返回错误（配置节解析失败等），启动期统一收集、拒绝启动。
func Register(make func(Host) (Plugin, error)) { factories = append(factories, make) }

// Factories 返回全部已注册的编译型插件构造器。
func Factories() []func(Host) (Plugin, error) { return factories }

// FormatTime/ParseTime 是插件侧的 UTC RFC3339 时间口径（与核心一致）。
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func ParseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// --- server_state 插件命名空间 ---

// StateGet 读取插件命名空间的 server_state 键（实际键 = plugin:<插件名>:<key>）。
func StateGet(q Queryer, plugin, key string) (string, bool, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM server_state WHERE key = ?`, "plugin:"+plugin+":"+key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// StateSet 写入插件命名空间的 server_state 键。
func StateSet(q Queryer, plugin, key, value string) error {
	_, err := q.Exec(`INSERT INTO server_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, "plugin:"+plugin+":"+key, value)
	return err
}
