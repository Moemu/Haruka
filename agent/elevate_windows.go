//go:build windows

package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// install/uninstall 以动态 UAC 提权执行：当前进程无管理员权限时，
// 用 ShellExecuteExW 的 runas 动词重新运行自身（--elevated 标记防循环），
// 结果经临时文件写回。成功不弹窗，失败弹 MessageBox（设计案 8.1 节）。

const (
	seeMaskNoCloseProcess = 0x00000040
	mbIconError           = 0x00000010
)

type shellExecuteInfoW struct {
	CbSize         uint32
	FMask          uint32
	Hwnd           uintptr
	LpVerb         *uint16
	LpFile         *uint16
	LpParameters   *uint16
	LpDirectory    *uint16
	NShow          int32
	HInstApp       uintptr
	LpIDList       uintptr
	LpClass        *uint16
	HkeyClass      windows.Handle
	DwHotKey       uint32
	HIconOrMonitor uintptr
	HProcess       windows.Handle
}

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
	procIsUserAnAdmin   = shell32.NewProc("IsUserAnAdmin")

	user32msg       = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW = user32msg.NewProc("MessageBoxW")
)

type installResult struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message"`
}

func isElevated() bool {
	r1, _, _ := procIsUserAnAdmin.Call()
	return r1 != 0
}

func CmdInstall(args []string) error   { return cmdInstallUninstall(args, true) }
func CmdUninstall(args []string) error { return cmdInstallUninstall(args, false) }

func cmdInstallUninstall(args []string, install bool) error {
	fs := flag.NewFlagSet("", flag.ExitOnError)
	cfg := fs.String("config", DefaultConfigPath(), "配置文件路径")
	state := fs.String("state-dir", DefaultStateDir(), "状态目录")
	elevated := fs.Bool("elevated", false, "内部：已提权标记")
	resultFile := fs.String("result-file", "", "内部：提权子进程结果写回路径")
	userSID := fs.String("user-sid", "", "安装用户 SID")
	fs.Parse(args)
	if *userSID == "" {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return err
		}
		*userSID = user.User.Sid.String()
	}
	if _, err := windows.StringToSid(*userSID); err != nil {
		return fmt.Errorf("用户 SID 非法: %w", err)
	}

	cfgAbs, err := filepath.Abs(*cfg)
	if err != nil {
		return err
	}
	stateAbs, err := filepath.Abs(*state)
	if err != nil {
		return err
	}

	if !*elevated && !isElevated() {
		result, err := os.CreateTemp("", "haruka-install-*.json")
		if err != nil {
			return err
		}
		result.Close()
		defer os.Remove(result.Name())

		action := "uninstall"
		if install {
			action = "install"
		}
		code, err := elevateRun([]string{
			action, "--config", cfgAbs, "--state-dir", stateAbs,
			"--user-sid", *userSID,
			"--elevated", "--result-file", result.Name(),
		})
		if err != nil {
			return err
		}
		raw, _ := os.ReadFile(result.Name())
		var res installResult
		if json.Unmarshal(raw, &res) == nil && res.Message != "" {
			fmt.Println(res.Message)
		} else {
			fmt.Printf("提权子进程退出码 %d\n", code)
		}
		if code != 0 {
			return fmt.Errorf("提权子进程失败（退出码 %d）", code)
		}
		return nil
	}

	// 提权侧：执行实际操作，写回结果；失败时弹窗（子进程无控制台输出）。
	var opErr error
	okMsg := ""
	if install {
		opErr = doInstall(cfgAbs, stateAbs, *userSID)
		okMsg = "服务 HarukaAgent 已安装并启动；采集进程将随会话登录自动拉起"
	} else {
		opErr = doUninstall()
		okMsg = "服务 HarukaAgent 已卸载"
	}
	writeResult(*resultFile, opErr, okMsg)
	if opErr != nil {
		messageBox("Haruka Agent 操作失败", opErr.Error())
		return opErr
	}
	return nil
}

func writeResult(path string, opErr error, okMsg string) {
	if path == "" {
		return
	}
	res := installResult{Ok: opErr == nil, Message: okMsg}
	if opErr != nil {
		res.Message = opErr.Error()
	}
	raw, _ := json.Marshal(res)
	os.WriteFile(path, raw, 0o600)
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	x, _ := windows.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(x)), uintptr(unsafe.Pointer(t)), mbIconError)
}

// elevateRun 以 runas 启动自身并等待退出，返回子进程退出码。
func elevateRun(args []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(quoteWindowsArgs(args))
	sei := &shellExecuteInfoW{
		CbSize: uint32(unsafe.Sizeof(shellExecuteInfoW{})),
		FMask:  seeMaskNoCloseProcess,
		LpVerb: verb, LpFile: file, LpParameters: params,
		NShow: 1, // SW_SHOWNORMAL
	}
	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(sei)))
	if r1 == 0 {
		if callErr == windows.ERROR_CANCELLED {
			return 0, fmt.Errorf("UAC 提权被取消，未执行任何更改")
		}
		return 0, fmt.Errorf("提权启动失败: %v", callErr)
	}
	if sei.HProcess == 0 {
		return 0, fmt.Errorf("提权启动失败：未获得进程句柄")
	}
	defer windows.CloseHandle(sei.HProcess)
	if _, err := windows.WaitForSingleObject(sei.HProcess, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.HProcess, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}

func quoteWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	return strings.Join(quoted, " ")
}

// protectedInstallPath 是受保护安装位置：Program Files 的默认 ACL 仅允许
// Administrators 与 SYSTEM 写入，普通用户只有读+执行。把代理注册为
// LocalSystem 服务前必须先复制到这里——直接注册用户可写目录下的 exe，
// 等于把 SYSTEM 权限交给任何能替换该文件的本地用户（审查 P1-1）。
func protectedInstallPath() (string, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Haruka", "bin", "haruka-agent.exe"), nil
}

func protectInstallObject(path string) error {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("安装位置不能是重解析点: %s", path)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;GRGX;;;BU)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	owner, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
}

func prepareInstallPath(dst string) error {
	for _, dir := range []string{filepath.Dir(filepath.Dir(dst)), filepath.Dir(dst)} {
		if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		if err := protectInstallObject(dir); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(dst); err == nil {
		return protectInstallObject(dst)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func copyAndVerify(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	hashSrc := sha256.New()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.MultiWriter(out, hashSrc), in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if !bytes.Equal(sum[:], hashSrc.Sum(nil)) {
		return fmt.Errorf("受保护副本校验失败")
	}
	return nil
}

func doInstall(cfgAbs, stateAbs, userSID string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	if _, err := LoadConfig(cfgAbs); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接 SCM 失败: %w", err)
	}
	defer m.Disconnect()
	if err := removeService(m); err != nil {
		return err
	}
	dst, err := protectedInstallPath()
	if err != nil {
		return err
	}
	if err := prepareInstallPath(dst); err != nil {
		return err
	}
	if !samePath(exe, dst) {
		if err := copyAndVerify(exe, dst); err != nil {
			return fmt.Errorf("复制到受保护目录 %s 失败: %w", filepath.Dir(dst), err)
		}
		exe = dst
	}

	if err := eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("注册事件日志失败: %w", err)
	}

	s, err := m.CreateService(ServiceName, exe, mgr.Config{
		DisplayName: "Haruka Agent Supervisor",
		Description: "Haruka 桌面采集代理的会话监督服务：在已登录用户的会话中拉起并看护 haruka-agent。",
		StartType:   mgr.StartAutomatic,
	}, "svc", "--config", cfgAbs, "--state-dir", stateAbs, "--user-sid", userSID)
	if err != nil {
		return fmt.Errorf("创建服务失败: %w", err)
	}
	defer s.Close()

	// SCM 恢复选项：崩溃后 5s/10s/30s 递增重启（设计案 11 节）。
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 86400); err != nil {
		// 非致命：恢复选项设置失败不影响服务本身。
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("服务启动失败: %w", err)
	}
	return nil
}

func doUninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接 SCM 失败: %w", err)
	}
	defer m.Disconnect()
	if err := removeService(m); err != nil {
		return err
	}
	if err := eventlog.Remove(ServiceName); err != nil && !os.IsNotExist(err) {
		return err
	}
	dst, err := protectedInstallPath()
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func removeService(m *mgr.Mgr) error {
	s, err := m.OpenService(ServiceName)
	if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
		return nil
	}
	if err != nil {
		return err
	}
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if status, err := s.Query(); err == nil && status.State == svc.Stopped {
			return s.Delete()
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("等待 %s 停止超时", ServiceName)
}
