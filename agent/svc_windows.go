//go:build windows

package agent

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

// Windows 两级架构（设计案 8.1 节）：
//   - 本文件是 SCM 监督服务（Session 0，LocalSystem）：只负责拉起、看护、终止
//     用户会话内的采集进程，不持有令牌、不做采集。
//   - 会话采集进程由 launch() 经 WTSQueryUserToken + CreateProcessAsUserW
//     在活动控制台会话中创建——GetLastInputInfo 与前台窗口只在用户会话内有效，
//     这就是两级进程存在的原因（M-1 PoC (a)）。

const (
	ServiceName = "HarukaAgent"

	evSessionLogon  = 5 // WTS_SESSION_LOGON
	evSessionLogoff = 6 // WTS_SESSION_LOGOFF

	createUnicodeEnvironment = 0x00000400
	createNoWindow           = 0x08000000
)

var (
	kernel32svc                      = windows.NewLazySystemDLL("kernel32.dll")
	procWTSGetActiveConsoleSessionId = kernel32svc.NewProc("WTSGetActiveConsoleSessionId")
	procCreateProcessAsUserW         = kernel32svc.NewProc("CreateProcessAsUserW")

	wtsapi32              = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQueryUserToken = wtsapi32.NewProc("WTSQueryUserToken")

	userenv                     = windows.NewLazySystemDLL("userenv.dll")
	procCreateEnvironmentBlock  = userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock = userenv.NewProc("DestroyEnvironmentBlock")
)

// startupInfoW / processInformationW 与 winbase.STARTUPINFOW / PROCESS_INFORMATION 布局一致。
type startupInfoW struct {
	Cb              uint32
	LpReserved      *uint16
	LpDesktop       *uint16
	LpTitle         *uint16
	DwX             uint32
	DwY             uint32
	DwXSize         uint32
	DwYSize         uint32
	DwXCountChars   uint32
	DwYCountChars   uint32
	DwFillAttribute uint32
	DwFlags         uint32
	WShowWindow     uint16
	CbReserved2     uint16
	LpReserved2     uintptr
	HStdInput       uintptr
	HStdOutput      uintptr
	HStdError       uintptr
}

type processInformationW struct {
	HProcess    windows.Handle
	HThread     windows.Handle
	DwProcessId uint32
	DwThreadId  uint32
}

// Supervisor 是 SCM 服务处理器。
type Supervisor struct {
	exe      string
	cfgPath  string
	stateDir string
	userSID  string
	log      *slog.Logger

	childProc windows.Handle
	childPID  uint32
}

func (s *Supervisor) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange
	changes <- svc.Status{State: svc.StartPending}
	s.log.Info("监督服务启动", "exe", s.exe, "config", s.cfgPath, "state", s.stateDir)
	s.launchIfSession()
	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s.log.Info("监督服务停止")
				changes <- svc.Status{State: svc.StopPending}
				s.killChild("服务停止")
				return false, 0
			case svc.SessionChange:
				switch c.EventType {
				case evSessionLogon:
					s.log.Info("检测到会话登录")
					s.launchIfSession()
				case evSessionLogoff:
					s.log.Info("检测到会话注销")
					s.killChild("会话注销")
				}
			}
		case <-ticker.C:
			s.watchdog()
		}
	}
}

// watchdog 每 15 秒检查会话采集进程：退出即重新拉起（有活动会话时）。
func (s *Supervisor) watchdog() {
	if s.childProc != 0 {
		state, err := windows.WaitForSingleObject(s.childProc, 0)
		switch {
		case err == nil && state == windows.WAIT_OBJECT_0:
			s.log.Warn("会话采集进程已退出，重新拉起", "pid", s.childPID)
			windows.CloseHandle(s.childProc)
			s.childProc = 0
		case err != nil:
			s.log.Warn("查询采集进程状态失败，重置句柄", "error", err)
			s.childProc = 0
		}
	}
	s.launchIfSession()
}

func (s *Supervisor) launchIfSession() {
	if s.childProc != 0 {
		return
	}
	if err := s.launch(); err != nil {
		s.log.Warn("拉起会话采集进程失败（无活动会话时属预期）", "error", err)
	}
}

func (s *Supervisor) launch() error {
	sessionId, _, _ := procWTSGetActiveConsoleSessionId.Call()
	if sessionId == 0 || sessionId == 0xFFFFFFFF {
		return fmt.Errorf("无活动控制台会话")
	}
	var token windows.Token
	r1, _, callErr := procWTSQueryUserToken.Call(sessionId, uintptr(unsafe.Pointer(&token)))
	if r1 == 0 {
		return fmt.Errorf("WTSQueryUserToken: %v", callErr)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.String() != s.userSID {
		return fmt.Errorf("活动控制台会话不是安装用户，跳过采集")
	}

	// 子进程需要用户会话的环境变量（APPDATA 等）。
	var envBlock uintptr
	r1, _, callErr = procCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&envBlock)), uintptr(token), 1)
	if r1 == 0 {
		return fmt.Errorf("CreateEnvironmentBlock: %v", callErr)
	}
	defer procDestroyEnvironmentBlock.Call(envBlock)

	cmdLine, _ := windows.UTF16PtrFromString(fmt.Sprintf(
		`"%s" run --config "%s" --state-dir "%s"`, s.exe, s.cfgPath, s.stateDir))
	exePtr, _ := windows.UTF16PtrFromString(s.exe)
	desktop, _ := windows.UTF16PtrFromString("winsta0\\default")
	si := &startupInfoW{Cb: uint32(unsafe.Sizeof(startupInfoW{})), LpDesktop: desktop}
	pi := &processInformationW{}
	r1, _, callErr = procCreateProcessAsUserW.Call(
		uintptr(token),
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(cmdLine)),
		0, 0, 0,
		createUnicodeEnvironment|createNoWindow,
		envBlock, // CreateEnvironmentBlock 返回的 OS 内存块，直接以 uintptr 传递
		0,
		uintptr(unsafe.Pointer(si)), uintptr(unsafe.Pointer(pi)))
	if r1 == 0 {
		return fmt.Errorf("CreateProcessAsUserW: %v", callErr)
	}
	windows.CloseHandle(pi.HThread)
	s.childProc = pi.HProcess
	s.childPID = pi.DwProcessId
	s.log.Info("已在用户会话中拉起采集进程", "session", sessionId, "pid", pi.DwProcessId)
	return nil
}

func (s *Supervisor) killChild(reason string) {
	if s.childProc == 0 {
		return
	}
	windows.TerminateProcess(s.childProc, 1)
	windows.CloseHandle(s.childProc)
	s.childProc = 0
	s.log.Info("已终止会话采集进程", "pid", s.childPID, "reason", reason)
}

// CmdService 是 `haruka-agent svc` 入口，仅供 SCM 调用。
func CmdService(args []string) error {
	fs := flag.NewFlagSet("", flag.ExitOnError)
	cfg := fs.String("config", DefaultConfigPath(), "配置文件路径")
	state := fs.String("state-dir", DefaultStateDir(), "状态目录")
	userSID := fs.String("user-sid", "", "安装用户 SID")
	fs.Parse(args)
	if *userSID == "" {
		return fmt.Errorf("服务缺少安装用户 SID，请重新执行 install")
	}

	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		return fmt.Errorf("svc 只能在 Windows 服务上下文中运行；本地调试请用 run，安装请用 install")
	}
	f, err := eventlog.Open(ServiceName)
	if err != nil {
		return err
	}
	defer f.Close()
	log := slog.New(slog.NewTextHandler(serviceLogWriter{f}, nil))

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return svc.Run(ServiceName, &Supervisor{exe: exe, cfgPath: *cfg, stateDir: *state, userSID: *userSID, log: log})
}

// SYSTEM 监督进程只写系统事件日志，避免写入用户可控制的文件路径。
type serviceLogWriter struct{ log *eventlog.Log }

func (w serviceLogWriter) Write(p []byte) (int, error) {
	message := strings.TrimSpace(string(p))
	err := w.log.Info(1, message)
	return len(p), err
}
