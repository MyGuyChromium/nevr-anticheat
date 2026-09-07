//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
		func(pid int, timeout time.Duration) error {
			if pid != 123 || timeout != 30*time.Second {
				t.Fatalf("wait arguments = %d, %s", pid, timeout)
			}
			return nil
		},
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
		func(int, time.Duration) error { return nil },
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

func TestApplyStagedUpdateRefusesUnverifiedExit(t *testing.T) {
	dir := t.TempDir()
	installer := filepath.Join(dir, "installer.exe")
	if err := os.WriteFile(installer, []byte("must remain untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := applyStagedUpdateWith(installer, "unused", 123, "nevr-desktop.exe", nil, dir,
		func(int, time.Duration) error { return syscall.ERROR_ACCESS_DENIED },
		func(string, []string) error {
			t.Fatal("must not relaunch while the original desktop may still be running")
			return nil
		},
	)
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("exit verification error was lost: %v", err)
	}
	if _, statErr := os.Stat(installer); statErr != nil {
		t.Fatalf("installer was touched after an unverified exit: %v", statErr)
	}
	raw, readErr := os.ReadFile(filepath.Join(dir, "last-update-error.txt"))
	if readErr != nil || !strings.Contains(string(raw), "installer was not started") {
		t.Fatalf("persisted update refusal = %q, %v", raw, readErr)
	}
}

func TestWaitForDesktopExit(t *testing.T) {
	if os.Getenv("NEVR_TEST_EXIT_PROCESS") == "1" {
		return
	}
	t.Run("invalid process ID", func(t *testing.T) {
		for _, pid := range []int{0, -1} {
			if err := waitForDesktopExit(pid, 0); err == nil {
				t.Fatalf("invalid PID %d was treated as a successful exit", pid)
			}
		}
	})
	t.Run("live process timeout", func(t *testing.T) {
		if err := waitForDesktopExit(os.Getpid(), time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not close") {
			t.Fatalf("running process was treated as exited: %v", err)
		}
	})
	t.Run("failed native wait", func(t *testing.T) {
		const errorInvalidHandle syscall.Errno = 6
		if err := waitForDesktopHandle(0, 0); !errors.Is(err, errorInvalidHandle) {
			t.Fatalf("invalid process handle was treated as exited: %v", err)
		}
	})
	t.Run("child exit", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWaitForDesktopExit$")
		cmd.Env = append(os.Environ(), "NEVR_TEST_EXIT_PROCESS=1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// cmd retains its process handle until Wait, so the native wait observes
		// the actual process object even if the child has already finished.
		err := waitForDesktopExit(cmd.Process.Pid, 10*time.Second)
		if err != nil {
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		if err != nil || waitErr != nil {
			t.Fatalf("child exit verification = %v; child wait = %v", err, waitErr)
		}
		if err := waitForDesktopExit(cmd.Process.Pid, 0); err != nil {
			t.Fatalf("already-exited child rejected: %v", err)
		}
	})
}

func TestLaunchUpdateHelperRejectsInvalidInputsBeforeStaging(t *testing.T) {
	dir := t.TempDir()
	err := launchUpdateHelper(updateLaunchRequest{
		InstallerPath: filepath.Join(dir, "unexpected-installer.exe"),
		UpdateDir:     dir,
		LatestCommit:  strings.Repeat("a", 40),
		ExpectedHash:  strings.Repeat("ab", 32),
	})
	if err == nil || !strings.Contains(err.Error(), "installer name is invalid") {
		t.Fatalf("helper accepted invalid installer before launch: %v", err)
	}
	files, readErr := os.ReadDir(dir)
	if readErr != nil || len(files) != 0 {
		t.Fatalf("invalid launch staged a helper: %v, %v", files, readErr)
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
