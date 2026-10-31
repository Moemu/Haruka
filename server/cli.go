package server

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"haruka/internal/geo"
)

const cliUsage = `haruka-server — Haruka 服务端

用法:
  haruka-server serve          [-c 配置]
  haruka-server device add     [-c 配置] --name 名称 --kind desktop|android [--interval 10m] [--location-policy server|local|off]
  haruka-server device list    [-c 配置]
  haruka-server device edit    [-c 配置] --id 设备ID [--name 名称] [--interval 10m] [--location-policy server|local|off]
  haruka-server device revoke  [-c 配置] --id 设备ID
  haruka-server owner-token    [-c 配置] [--generate] [--rotate]
  haruka-server hide           [-c 配置] [时长，缺省不限期]
  haruka-server show           [-c 配置]
  haruka-server backup         [-c 配置] -o 目标文件
`

// RunCLI 是 haruka-server 的命令行入口。管理类命令直连本机 SQLite，
// 不存在管理类 HTTP 接口（设计案 7.1 节）。
func RunCLI(args []string) error {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, cliUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "serve":
		return cliServe(args[1:])
	case "device":
		return cliDevice(args[1:])
	case "owner-token":
		return cliOwnerToken(args[1:])
	case "hide":
		return cliHide(args[1:])
	case "show":
		return cliShow(args[1:])
	case "backup":
		return cliBackup(args[1:])
	default:
		fmt.Fprint(os.Stderr, cliUsage)
		os.Exit(2)
	}
	return nil
}

// configFlag 返回已注册 -c 的 FlagSet；调用方先补齐自己的 flag，再统一 Parse。
// 注意 flag 包遇到首个位置参数即停止，位置参数（如 hide 的时长）必须放在 flag 之后。
func configFlag(args []string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("", flag.ExitOnError)
	cfg := fs.String("c", "haruka-server.yml", "配置文件路径")
	return fs, cfg
}

func openEngine(cfgPath string) (*Engine, *Runtime, error) {
	rt, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	db, err := OpenDB(rt.DBPath)
	if err != nil {
		return nil, nil, err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return NewEngine(&rt, db, nil, log), &rt, nil
}

func cliServe(args []string) error {
	fs, cfg := configFlag(args)
	fs.Parse(args)
	rt, err := LoadConfig(*cfg)
	if err != nil {
		return err
	}
	db, err := OpenDB(rt.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	var resolver LocationResolver
	if rt.Cfg.LocationLookupDB != "" {
		s, err := geo.NewSearcher(rt.Cfg.LocationLookupDB)
		if err != nil {
			return fmt.Errorf("方案 A 位置解析库加载失败: %w", err)
		}
		resolver = s // geo.Searcher.Resolve 直接满足 LocationResolver
		log.Info("方案 A 位置解析已启用", "db", rt.Cfg.LocationLookupDB)
	}
	engine := NewEngine(&rt, db, resolver, log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go engine.RunRetention(ctx)

	srv := &http.Server{
		Addr:              rt.Listen,
		Handler:           engine.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("服务端停止超时，关闭连接", "error", err)
			srv.Close()
		}
	}()
	log.Info("haruka-server 启动", "listen", rt.Listen, "db", rt.DBPath)
	err = srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func cliDevice(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法: haruka-server device add|list|edit|revoke ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		fs, cfg := configFlag(rest)
		name := fs.String("name", "", "设备名称")
		kind := fs.String("kind", "desktop", "desktop | android")
		interval := fs.Duration("interval", 0, "心跳间隔（决定离线窗口 = ×3）")
		policy := fs.String("location-policy", "server", "server | local | off")
		fs.Parse(rest)
		if *name == "" {
			return fmt.Errorf("--name 不能为空")
		}
		engine, _, err := openEngine(*cfg)
		if err != nil {
			return err
		}
		defer engine.db.Close()
		d, token, err := engine.DeviceAdd(*name, *kind, *interval, *policy)
		if err != nil {
			return err
		}
		fmt.Printf("设备已登记:\n  id:     %s\n  name:   %s\n  kind:   %s\n  interval: %s\n  location_policy: %s\n  token:  %s\n（令牌只显示这一次，请立即保存）\n",
			d.ID, d.Name, d.Kind, d.Interval, d.LocationPolicy, token)
	case "list":
		fs, cfg := configFlag(rest)
		fs.Parse(rest)
		engine, _, err := openEngine(*cfg)
		if err != nil {
			return err
		}
		defer engine.db.Close()
		devices, err := engine.DeviceList()
		if err != nil {
			return err
		}
		for _, d := range devices {
			state := "正常"
			if d.Revoked {
				state = "已吊销"
			}
			fmt.Printf("%s  %-12s %-8s interval=%-6s policy=%-6s %s\n", d.ID, d.Name, d.Kind, d.Interval, d.LocationPolicy, state)
		}
	case "edit":
		fs, cfg := configFlag(rest)
		id := fs.String("id", "", "设备 ID")
		name := fs.String("name", "", "新设备名称（留空不变）")
		interval := fs.Duration("interval", 0, "新心跳间隔（留空不变）")
		policy := fs.String("location-policy", "", "server | local | off（留空不变）")
		fs.Parse(rest)
		if *id == "" {
			return fmt.Errorf("--id 不能为空")
		}
		engine, _, err := openEngine(*cfg)
		if err != nil {
			return err
		}
		defer engine.db.Close()
		d, err := engine.DeviceEdit(*id, *name, *interval, *policy)
		if err != nil {
			return err
		}
		fmt.Printf("设备已更新:\n  id:     %s\n  name:   %s\n  kind:   %s\n  interval: %s\n  location_policy: %s\n",
			d.ID, d.Name, d.Kind, d.Interval, d.LocationPolicy)
	case "revoke":
		fs, cfg := configFlag(rest)
		id := fs.String("id", "", "设备 ID")
		fs.Parse(rest)
		if *id == "" {
			return fmt.Errorf("--id 不能为空")
		}
		engine, _, err := openEngine(*cfg)
		if err != nil {
			return err
		}
		defer engine.db.Close()
		if err := engine.DeviceRevoke(*id); err != nil {
			return err
		}
		fmt.Printf("已吊销设备 %s\n", *id)
	default:
		return fmt.Errorf("未知子命令 device %q", sub)
	}
	return nil
}

func cliOwnerToken(args []string) error {
	fs, cfg := configFlag(args)
	generate := fs.Bool("generate", false, "签发（已存在则报错）")
	rotate := fs.Bool("rotate", false, "轮换（覆盖旧令牌）")
	fs.Parse(args)
	if !*generate && !*rotate {
		return fmt.Errorf("请指定 --generate 或 --rotate")
	}
	engine, _, err := openEngine(*cfg)
	if err != nil {
		return err
	}
	defer engine.db.Close()
	token, err := engine.OwnerTokenGenerate(*rotate)
	if err != nil {
		return err
	}
	fmt.Printf("owner 令牌: %s\n（用于 POST /v1/control 与远程设备管理，只显示这一次）\n", token)
	return nil
}

func cliHide(args []string) error {
	// 手动解析，兼容 `hide 5m -c x` 与 `hide -c x 5m`（flag 包遇位置参数即停）。
	cfgPath := "haruka-server.yml"
	var durArg string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c" && i+1 < len(args):
			cfgPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "-c="):
			cfgPath = strings.TrimPrefix(args[i], "-c=")
		case durArg == "":
			durArg = args[i]
		default:
			return fmt.Errorf("多余参数 %q", args[i])
		}
	}
	engine, _, err := openEngine(cfgPath)
	if err != nil {
		return err
	}
	defer engine.db.Close()
	var until *time.Time
	if durArg != "" {
		d, err := time.ParseDuration(durArg)
		if err != nil || d <= 0 {
			return fmt.Errorf("非法时长 %q", durArg)
		}
		t := engine.now().Add(d)
		until = &t
	}
	if err := engine.Hide(until); err != nil {
		return err
	}
	if until != nil {
		fmt.Printf("已进入隐身，至 %s\n", until.Format(time.RFC3339))
	} else {
		fmt.Println("已进入隐身（不限期，恢复请执行 show）")
	}
	return nil
}

func cliShow(args []string) error {
	fs, cfg := configFlag(args)
	fs.Parse(args)
	engine, _, err := openEngine(*cfg)
	if err != nil {
		return err
	}
	defer engine.db.Close()
	if err := engine.Show(); err != nil {
		return err
	}
	fmt.Println("已恢复可见；resume_at 边界已记录，边界前的积压不会回填公开状态")
	return nil
}

func cliBackup(args []string) error {
	fs, cfg := configFlag(args)
	out := fs.String("o", "", "备份目标文件")
	fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("-o 目标文件不能为空")
	}
	rt, err := LoadConfig(*cfg)
	if err != nil {
		return err
	}
	db, err := OpenDB(rt.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	// WAL 模式下直接复制主库文件会丢已提交数据；VACUUM INTO 是在线备份（设计案 7.3 节）。
	if _, err := db.Exec(`VACUUM INTO ?`, *out); err != nil {
		return fmt.Errorf("备份失败: %w", err)
	}
	fmt.Printf("已备份到 %s（建议定期执行并做一次恢复演练）\n", *out)
	return nil
}
