//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
