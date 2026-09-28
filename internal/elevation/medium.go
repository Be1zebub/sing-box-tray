//go:build windows

package elevation

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procCreateProcessWithTokenW = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithTokenW")

// StartDropAdmin runs commandLine. When this process is elevated, the child
// is started with Explorer's token so it does not inherit the admin token.
// launchedElevated is set when that hand-off failed and the child kept this
// process's token. commandLine is a full command line starting with cmd.exe.
func StartDropAdmin(commandLine string) (launchedElevated bool, err error) {
	if !IsElevated() {
		return false, runCmdLine(commandLine)
	}
	if err := runAsShellUser(commandLine); err == nil {
		return false, nil
	} else if err2 := runCmdLine(commandLine); err2 != nil {
		return false, fmt.Errorf("%w (elevated retry: %v)", err, err2)
	} else {
		return true, nil
	}
}

func runCmdLine(commandLine string) error {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       commandLine,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd.Run()
}

// runAsShellUser duplicates the interactive shell token. Explorer runs at
// medium integrity even when this tray was relaunched for TUN.
func runAsShellUser(commandLine string) error {
	hwnd := windows.GetShellWindow()
	if hwnd == 0 {
		return fmt.Errorf("shell window not found")
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil {
		return err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		proc, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			return err
		}
	}
	defer windows.CloseHandle(proc)

	var token windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()

	_ = enablePrivilege("SeImpersonatePrivilege")

	var dup windows.Token
	const maximumAllowed = 0x02000000
	if err := windows.DuplicateTokenEx(token, maximumAllowed, nil, windows.SecurityImpersonation, windows.TokenPrimary, &dup); err != nil {
		return err
	}
	defer dup.Close()

	cmdline, err := windows.UTF16FromString(commandLine)
	if err != nil {
		return err
	}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	r, _, callErr := procCreateProcessWithTokenW.Call(
		uintptr(dup),
		0,
		0,
		uintptr(unsafe.Pointer(&cmdline[0])),
		uintptr(windows.CREATE_NO_WINDOW),
		0,
		0,
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if r == 0 {
		if callErr == syscall.Errno(0) {
			return fmt.Errorf("CreateProcessWithTokenW failed")
		}
		return callErr
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("exit %d", code)
	}
	return nil
}

func enablePrivilege(name string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, n, &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{{
			Luid:       luid,
			Attributes: windows.SE_PRIVILEGE_ENABLED,
		}},
	}
	return windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil)
}
