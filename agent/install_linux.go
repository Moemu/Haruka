//go:build linux

package agent

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux 采集代理以 systemd user 服务运行（设计案 8.1 节）：
// PartOf=graphical-session.target 显式绑定图形会话——user 管理器与图形会话
// 不是同一概念，SSH 登录不会触发 graphical-session.target，采集进程因此
// 不会在无头会话中误启动；注销时随图形会话停止。

const linuxUnitName = "haruka-agent.service"

func CmdInstall(args []string) error {
	fs := flag.NewFlagSet("", flag.ExitOnError)
	cfg := fs.String("config", DefaultConfigPath(), "配置文件路径")
	state := fs.String("state-dir", DefaultStateDir(), "状态目录")
	fs.Parse(args)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exeAbs, err := filepath.Abs(exe)
	if err != nil {
		return err
	}
	cfgAbs, err := filepath.Abs(*cfg)
	if err != nil {
		return err
	}
	stateAbs, err := filepath.Abs(*state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateAbs, 0o700); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=Haruka 桌面采集代理
PartOf=graphical-session.target
After=graphical-session.target

[Service]
ExecStart=%q run --config %q --state-dir %q
Restart=always
RestartSec=10

[Install]
WantedBy=graphical-session.target
`, exeAbs, cfgAbs, stateAbs)
	unitPath := filepath.Join(unitDir, linuxUnitName)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}

	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user 不可用（systemd 未启用？）: %w", err)
	}

	// 如实区分两种情况（M-1(d) 实测发现：PartOf 只绑定生命周期，不阻止
	// enable --now 立即启动；因此在图形会话未激活时绝不 --now，避免无头采集）。
	gsOut, gsErr := exec.Command("systemctl", "--user", "is-active", "graphical-session.target").Output()
	gsState := strings.TrimSpace(string(gsOut))
	if gsErr == nil && gsState == "active" {
		if err := runCmd("systemctl", "--user", "enable", "--now", linuxUnitName); err != nil {
			return fmt.Errorf("启用服务失败: %w", err)
		}
		fmt.Printf("服务单元已安装并启动: %s\n", unitPath)
		return nil
	}
	if err := runCmd("systemctl", "--user", "enable", linuxUnitName); err != nil {
		return fmt.Errorf("启用服务失败: %w", err)
	}
	fmt.Printf("服务单元已安装并启用（开机随图形会话自启）: %s\n", unitPath)
	fmt.Printf("注意: graphical-session.target 当前未激活（%s），本次未立即启动——\n", gsState)
	fmt.Println("      PartOf 只绑定生命周期，enable --now 会绕过它；服务将在图形会话启动时自动拉起。")
	return nil
}

func CmdUninstall(args []string) error {
	_ = runCmd("systemctl", "--user", "disable", "--now", linuxUnitName)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", linuxUnitName)
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	fmt.Printf("服务单元已卸载: %s\n", unitPath)
	return nil
}

func CmdService(args []string) error {
	return fmt.Errorf("Linux 无 svc 子命令：服务由 systemd user 单元管理（install/uninstall）")
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
