//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS (DWORD -1)

// showStartupDialog shows a blocking error dialog. It is only used when the
// process has no console to print the failure to.
func showStartupDialog(title, message string) {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	messagePtr, err := syscall.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	const mbOK, mbIconError, mbSetForeground = 0x0, 0x10, 0x10000
	messageBox := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)), mbOK|mbIconError|mbSetForeground)
}

// attachParentConsole lets a GUI-subsystem build started from a terminal with
// --console print there. It reports whether a console is now attached.
func attachParentConsole() bool {
	attach := syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")
	if ok, _, _ := attach.Call(attachParentProcess); ok == 0 {
		return false
	}
	out, err := syscall.UTF16PtrFromString("CONOUT$")
	if err != nil {
		return false
	}
	handle, err := syscall.CreateFile(out, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false
	}
	console := os.NewFile(uintptr(handle), "CONOUT$")
	if console == nil {
		return false
	}
	os.Stdout, os.Stderr = console, console
	return true
}
