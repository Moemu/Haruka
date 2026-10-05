// haruka-agent 是桌面端采集代理。Windows 上必须以已登录用户的会话进程运行；
// Windows 系统级监督服务只拉起和看护安装用户的会话进程。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"haruka/agent"
	"haruka/protocol"
)

const version = "0.1.0-dev"

const usage = `haruka-agent — Haruka 桌面采集代理

用法:
  haruka-agent run        [--config 路径] [--state-dir 目录] [--debug] [调试覆盖项]
  haruka-agent doctor     [--config 路径] [--state-dir 目录]
  haruka-agent status     [--state-dir 目录]
  haruka-agent pause      <时长，如 2h>
  haruka-agent resume
  haruka-agent install    [--config 路径] [--state-dir 目录]   （Windows 动态 UAC 提权；Linux 写 systemd user 单元）
  haruka-agent uninstall  [--config 路径] [--state-dir 目录]
  haruka-agent svc        （Windows 专用：SCM 服务入口，仅供系统调用）
  haruka-agent version

调试覆盖项（run）:
  --flush <时长>  --heartbeat <时长>  --window <时长>  --backoff-init <时长>
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "pause":
		err = cmdPause(os.Args[2:])
	case "resume":
		err = cmdResume(os.Args[2:])
	case "install":
		err = agent.CmdInstall(os.Args[2:])
	case "uninstall":
		err = agent.CmdUninstall(os.Args[2:])
	case "svc":
		err = agent.CmdService(os.Args[2:])
	case "version":
		fmt.Println("haruka-agent", version)
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func flagSet(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	cfg := fs.String("config", agent.DefaultConfigPath(), "配置文件路径")
	state := fs.String("state-dir", agent.DefaultStateDir(), "状态目录（spool、序号、暂停标记）")
	return fs, cfg, state
}

func cmdRun(args []string) error {
	fs, cfgPath, stateDir := flagSet("run")
	debug := fs.Bool("debug", false, "打印每个事件到 stdout")
	flush := fs.Duration("flush", 0, "覆盖 flush_interval（调试用）")
	heartbeat := fs.Duration("heartbeat", 0, "覆盖 heartbeat_interval（调试用）")
	window := fs.Duration("window", 0, "覆盖 window_interval（调试用）")
	backoffInit := fs.Duration("backoff-init", 0, "覆盖初始退避（调试用）")
	fs.Parse(args)

	rt, err := agent.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if flush != nil && *flush > 0 {
		rt.Flush = *flush
	}
	if *heartbeat > 0 {
		rt.Heartbeat = *heartbeat
	}
	if *window > 0 {
		rt.WindowTick = *window
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	// run 可能由监督服务在会话内拉起（无控制台），日志必须同时落文件。
	logWriter := io.Writer(os.Stdout)
	if f, ferr := os.OpenFile(filepath.Join(*stateDir, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil {
		defer f.Close()
		logWriter = io.MultiWriter(os.Stdout, f)
	}
	log := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: level}))
	log.Info("haruka-agent 启动",
		"server", rt.ServerURL,
		"device_id", rt.Cfg.DeviceID,
		"heartbeat", rt.Heartbeat,
		"location_mode", rt.Cfg.Location.Mode)

	seq, err := agent.OpenSeq(*stateDir)
	if err != nil {
		return err
	}
	spool, err := agent.OpenSpool(filepath.Join(*stateDir, "spool.db"))
	if err != nil {
		return err
	}
	defer spool.Close()

	pauseFn := func() (time.Time, bool) {
		until, active, err := agent.ReadPauseMarker(*stateDir)
		if err != nil {
			log.Warn("读取暂停标记失败", "error", err)
		}
		return until, active
	}
	pausedNow := func() bool {
		_, active, _ := agent.ReadPauseMarker(*stateDir)
		return active
	}

	deps := agent.Deps{
		DeviceID:   rt.Cfg.DeviceID,
		DeviceKind: rt.Cfg.DeviceKind,
		Seq:        seq,
		Log:        log,
		Policy:     agent.NewPolicySync(&rt, log),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go deps.Policy.Run(ctx)

	events := make(chan protocol.Event, 256)
	rep := agent.NewReporter(deps, &rt, spool, pauseFn, log)
	rep.Debug(*debug)
	if *backoffInit > 0 {
		rep.SetBackoff(*backoffInit, 30*time.Second)
	}
	go rep.Run(ctx)

	sched := agent.NewScheduler(events, log, pausedNow)
	intervals := agent.CollectorIntervals(&rt)
	collectors := agent.NewCollectors(deps, &rt)
	var presenceCollector agent.TickCollector
	for _, c := range collectors {
		sched.Add(c, intervals[c.Name()])
		capability := agent.CapDegraded
		if pausedNow() {
			if pa, ok := c.(agent.PauseAware); ok {
				pa.PauseCollector()
			}
		} else {
			capability = c.Capability()
		}
		if c.Name() == "presence" {
			presenceCollector = c
		}
		log.Info("采集器就绪", "collector", c.Name(), "capability", capability, "interval", intervals[c.Name()])
	}
	// 关闭或暂停采集器不能被当成「明确不支持」授予 activity 展示资格。
	probePresence := func() agent.Capability {
		if pausedNow() || presenceCollector == nil {
			return agent.CapDegraded
		}
		return presenceCollector.Capability()
	}
	go agent.NewCapSync(&rt, probePresence, log).Run(ctx)
	go func() {
		for ev := range events {
			rep.Enqueue(ev)
		}
	}()
	go sched.Run(ctx)

	// 心跳独立通道：失败即丢，不入 spool。
	go func() {
		ticker := time.NewTicker(rt.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := rep.SendHeartbeat(ctx); err != nil {
					log.Debug("心跳发送失败（不入缓存）", "error", err)
				}
			}
		}
	}()

	// spool 保留策略：定期淘汰过期与超限记录。
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := spool.Cap(rt.SpoolMaxAge, rt.SpoolMaxMax); err != nil {
					log.Warn("spool 保留策略执行失败", "error", err)
				}
			}
		}
	}()

	<-ctx.Done()
	log.Info("haruka-agent 退出")
	return nil
}

func cmdDoctor(args []string) error {
	fs, cfgPath, stateDir := flagSet("doctor")
	fs.Parse(args)

	rt, err := agent.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Println("[配置] 合法")
	fmt.Printf("[服务端] %s\n", rt.ServerURL)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(rt.ServerURL + "/healthz")
	if err != nil {
		fmt.Printf("[连通性] 失败: %v\n", err)
	} else {
		defer resp.Body.Close()
		fmt.Printf("[连通性] %s\n", resp.Status)
		if date := resp.Header.Get("Date"); date != "" {
			if serverTime, err := http.ParseTime(date); err == nil {
				skew := serverTime.Sub(time.Now()).Round(time.Second)
				fmt.Printf("[时钟] 与服务端偏差 %s\n", skew)
				if skew > 24*time.Hour || skew < -24*time.Hour {
					fmt.Println("[时钟] 警告: 偏差超过 24 小时，观测事件将只入库不刷新实时态")
				}
			}
		}
	}

	for _, c := range agent.NewCollectors(agent.Deps{Log: slog.Default()}, &rt) {
		fmt.Printf("[采集器] %s: %s\n", c.Name(), c.Capability())
	}
	if rt.Cfg.Location.Mode == "server" {
		fmt.Println("[位置] 方案 A：由服务端解析连接 IP 后丢弃，本机不上报位置字段")
	}

	spool, err := agent.OpenSpool(filepath.Join(*stateDir, "spool.db"))
	if err != nil {
		return err
	}
	defer spool.Close()
	n, _ := spool.Count()
	fmt.Printf("[spool] 积压 %d 条（保留 %s / %d MB）\n", n, rt.SpoolMaxAge, rt.SpoolMaxMax/(1<<20))

	if _, active, _ := agent.ReadPauseMarker(*stateDir); active {
		fmt.Println("[暂停] 当前处于暂停期")
	} else {
		fmt.Println("[暂停] 未暂停")
	}
	if seq, err := agent.OpenSeq(*stateDir); err == nil {
		fmt.Printf("[序号] 已用至 %d\n", seq.Peek())
	}
	return nil
}

func cmdStatus(args []string) error {
	fs, _, stateDir := flagSet("status")
	fs.Parse(args)

	if until, active, _ := agent.ReadPauseMarker(*stateDir); active {
		fmt.Printf("暂停中，至 %s\n", until.Format(time.RFC3339))
	} else {
		fmt.Println("未暂停")
	}
	spool, err := agent.OpenSpool(filepath.Join(*stateDir, "spool.db"))
	if err != nil {
		return err
	}
	defer spool.Close()
	n, _ := spool.Count()
	fmt.Printf("spool 积压: %d\n", n)
	return nil
}

// parsePauseArgs 手动解析：flag 包遇到首个位置参数（时长）就停止，
// 因此 --state-dir 必须在 flag 包之外处理，且允许与时长任意顺序。
func parsePauseArgs(args []string) (stateDir, durArg string, err error) {
	stateDir = agent.DefaultStateDir()
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--state-dir" && i+1 < len(args):
			stateDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--state-dir="):
			stateDir = strings.TrimPrefix(args[i], "--state-dir=")
		case strings.HasPrefix(args[i], "-"):
			return "", "", fmt.Errorf("未知参数 %q", args[i])
		case durArg == "":
			durArg = args[i]
		default:
			return "", "", fmt.Errorf("多余参数 %q", args[i])
		}
	}
	return stateDir, durArg, nil
}

func cmdPause(args []string) error {
	stateDir, durArg, err := parsePauseArgs(args)
	if err != nil {
		return err
	}
	if durArg == "" {
		return fmt.Errorf("用法: haruka-agent pause <时长>，如 2h")
	}
	d, err := time.ParseDuration(durArg)
	if err != nil || d <= 0 {
		return fmt.Errorf("非法时长 %q", durArg)
	}
	if err := agent.WritePauseMarker(stateDir, time.Now().Add(d)); err != nil {
		return err
	}
	fmt.Printf("已写入暂停标记，至 %s（run 进程在下一轮 flush 生效，将清空队列与 spool）\n",
		time.Now().Add(d).Format(time.RFC3339))
	return nil
}

func cmdResume(args []string) error {
	stateDir, _, err := parsePauseArgs(args)
	if err != nil {
		return err
	}
	if err := agent.ClearPauseMarker(stateDir); err != nil {
		return err
	}
	fmt.Println("已清除暂停标记，从当下重新开始采集与上报")
	return nil
}
