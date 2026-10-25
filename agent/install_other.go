//go:build !windows

package agent

import "fmt"

func CmdInstall(args []string) error {
	return fmt.Errorf("当前平台暂不支持 install")
}

func CmdUninstall(args []string) error {
	return fmt.Errorf("当前平台暂不支持 uninstall")
}

func CmdService(args []string) error {
	return fmt.Errorf("当前平台暂不支持 svc")
}
