//go:build windows

// Package winmenu asks Windows to draw Win32 menus in the current app theme.
// Light theme stays light. Dark theme draws the tray menu dark.
package winmenu

import (
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

const preferredAppModeAllowDark = 1

var followOnce sync.Once

// FollowSystem opts this process into the system app theme for Win32 menus
// and refreshes them when the user switches that theme.
func FollowSystem() {
	followOnce.Do(func() {
		apply()
		go watchPersonalize()
	})
}

func apply() {
	uxtheme := windows.NewLazySystemDLL("uxtheme.dll")
	if err := uxtheme.Load(); err != nil {
		return
	}
	get := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcAddress")
	call := func(ord uintptr, args ...uintptr) {
		p, _, _ := get.Call(uxtheme.Handle(), ord)
		if p != 0 {
			syscall.SyscallN(p, args...)
		}
	}
	call(104)                            // RefreshImmersiveColorPolicyState
	call(135, preferredAppModeAllowDark) // SetPreferredAppMode(AllowDark)
	call(136)                            // FlushMenuThemes
}

func watchPersonalize() {
	var key windows.Handle
	sub, err := windows.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`)
	if err != nil {
		return
	}
	if err := windows.RegOpenKeyEx(windows.HKEY_CURRENT_USER, sub, 0, windows.KEY_NOTIFY, &key); err != nil {
		return
	}
	defer windows.RegCloseKey(key)

	evt, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return
	}
	defer windows.CloseHandle(evt)

	for {
		if err := windows.RegNotifyChangeKeyValue(key, false, windows.REG_NOTIFY_CHANGE_LAST_SET, evt, true); err != nil {
			return
		}
		if _, err := windows.WaitForSingleObject(evt, windows.INFINITE); err != nil {
			return
		}
		apply()
	}
}
