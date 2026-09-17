//go:build !windows

package main

// showStartupDialog has no dialog to show outside Windows; the failure was
// already written to stderr.
func showStartupDialog(string, string) {}

// attachParentConsole: a non-Windows build always has its terminal.
func attachParentConsole() bool { return true }
