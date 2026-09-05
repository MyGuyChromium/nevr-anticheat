//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyUpdateHelper(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.exe")
	target := filepath.Join(dir, "helper.exe")
	if err := os.WriteFile(source, []byte("signed desktop fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyUpdateHelper(source, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "signed desktop fixture" {
		t.Fatalf("helper contents = %q", got)
	}
}

func TestApplyStagedUpdateRelaunchesAfterVerificationFailure(t *testing.T) {
	dir := t.TempDir()
	installer := filepath.Join(dir, "NEVR-Anticheat-Setup-"+strings.Repeat("ab", 32)+".exe")
	if err := os.WriteFile(installer, []byte("tampered installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	relaunched := false
	err := applyStagedUpdateWith(installer, strings.Repeat("ab", 32), 123, "nevr-desktop.exe", []string{"--no-browser"}, dir,
		func(pid int, timeout time.Duration) bool { return pid == 123 && timeout == 30*time.Second },
		func(exe string, args []string) error {
			relaunched = exe == "nevr-desktop.exe" && len(args) == 1 && args[0] == "--no-browser"
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "changed after verification") {
		t.Fatalf("verification error = %v", err)
	}
	if !relaunched {
		t.Fatal("desktop was not relaunched after a safe verification failure")
	}
	raw, readErr := os.ReadFile(filepath.Join(dir, "last-update-error.txt"))
	if readErr != nil || !strings.Contains(string(raw), "changed after verification") {
		t.Fatalf("persisted update error = %q, %v", raw, readErr)
	}
}

func TestApplyStagedUpdateCombinesRelaunchFailure(t *testing.T) {
	dir := t.TempDir()
	installer := filepath.Join(dir, "NEVR-Anticheat-Setup-"+strings.Repeat("ab", 32)+".exe")
	if err := os.WriteFile(installer, []byte("tampered installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := applyStagedUpdateWith(installer, strings.Repeat("ab", 32), 123, "nevr-desktop.exe", nil, dir,
		func(int, time.Duration) bool { return true },
		func(string, []string) error { return errors.New("mock launch failure") },
	)
	if err == nil || !strings.Contains(err.Error(), "changed after verification") || !strings.Contains(err.Error(), "mock launch failure") {
		t.Fatalf("combined update error = %v", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(dir, "last-update-error.txt"))
	if readErr != nil || !strings.Contains(string(raw), "changed after verification") || !strings.Contains(string(raw), "mock launch failure") {
		t.Fatalf("persisted combined update error = %q, %v", raw, readErr)
	}
}

func TestValidateUpdateHelperPaths(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	updateDir := filepath.Join(local, "NEVR-Anticheat", "updates")
	digest := strings.Repeat("ab", 32)
	installer := filepath.Join(updateDir, "NEVR-Anticheat-Setup-"+digest+".exe")
	relaunch := filepath.Join(local, "Programs", "NEVR-Anticheat", "nevr-desktop.exe")
	if err := validateUpdateHelperPaths(installer, relaunch, updateDir, digest); err != nil {
		t.Fatalf("valid helper paths rejected: %v", err)
	}
	if err := validateUpdateHelperPaths(filepath.Join(local, "other.exe"), relaunch, updateDir, digest); err == nil {
		t.Fatal("installer outside staging directory was accepted")
	}
	if err := validateUpdateHelperPaths(installer, filepath.Join(local, "other.exe"), updateDir, digest); err == nil {
		t.Fatal("foreign relaunch target was accepted")
	}
	if err := validateUpdateHelperPaths(installer, relaunch, updateDir, strings.Repeat("cd", 32)); err == nil {
		t.Fatal("mismatched installer digest was accepted")
	}
}
