//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func replaceFile(src, dst string) error {
	from, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func restrictFile(path string) {
	user := os.Getenv("USERNAME")
	if user == "" {
		return
	}
	if domain := os.Getenv("USERDOMAIN"); domain != "" {
		user = domain + `\` + user
	}
	cmd := exec.Command("icacls", path, "/inheritance:r", "/grant:r", user+":(F)")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
