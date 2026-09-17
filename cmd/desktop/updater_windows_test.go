//go:build windows

package main

import (
	"context"
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

// testUpdateDir points LOCALAPPDATA at a temporary profile and returns the
// only staging directory the Windows updater accepts inside it, so no test can
// touch the real %LOCALAPPDATA%\NEVR-Anticheat of the machine running it.
func testUpdateDir(t *testing.T) string {
	t.Helper()
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	return filepath.Join(local, "NEVR-Anticheat", "updates")
}

func stagedInstallerName(digest string) string { return "NEVR-Anticheat-Setup-" + digest + ".exe" }

func TestLaunchUpdateHelperRejectsInvalidInputsBeforeStaging(t *testing.T) {
	updateDir := testUpdateDir(t)
	if err := os.MkdirAll(updateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("ab", 32)
	for _, tt := range []struct {
		name, installer, updateDir, want string
	}{
		{"installer name without its digest", filepath.Join(updateDir, "unexpected-installer.exe"), updateDir, "installer name is invalid"},
		// guard: verifyUpdateStagingDir in validateUpdateHelperPaths. The request
		// is self-consistent (installer inside its update directory); only the
		// fact that the directory is not NEVR's own can refuse it.
		{"self-consistent request outside the profile", filepath.Join(t.TempDir(), stagedInstallerName(digest)), "", "staged only in"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requestDir := tt.updateDir
			if requestDir == "" {
				requestDir = filepath.Dir(tt.installer)
			}
			err := launchUpdateHelper(updateLaunchRequest{
				InstallerPath: tt.installer, UpdateDir: requestDir,
				LatestCommit: strings.Repeat("a", 40), ExpectedHash: digest,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("helper launch error = %v, want one containing %q", err, tt.want)
			}
			for _, dir := range []string{updateDir, requestDir} {
				files, readErr := os.ReadDir(dir)
				if readErr != nil || len(files) != 0 {
					t.Fatalf("refused launch staged a helper in %s: %v, %v", dir, files, readErr)
				}
			}
		})
	}
}

func TestValidateUpdateHelperPaths(t *testing.T) {
	updateDir := testUpdateDir(t)
	local := os.Getenv("LOCALAPPDATA")
	if err := os.MkdirAll(filepath.Join(updateDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("ab", 32)
	name := stagedInstallerName(digest)
	installer := filepath.Join(updateDir, name)
	relaunch := filepath.Join(local, "Programs", "NEVR-Anticheat", "nevr-desktop.exe")
	if err := validateUpdateHelperPaths(installer, relaunch, updateDir, digest); err != nil {
		t.Fatalf("valid helper paths rejected: %v", err)
	}
	// Every rejected installer below carries a VALID staged name, so the name
	// check cannot be what refuses it. Removing the containment comparison in
	// validateUpdateHelperPaths makes the first three cases fail.
	for _, tt := range []struct {
		name, installer, relaunch, updateDir, digest, want string
	}{
		{"installer beside the staging directory", filepath.Join(filepath.Dir(updateDir), name), relaunch, updateDir, digest, "outside its staging directory"},
		{"installer reached through dot-dot", updateDir + `\..\..\` + name, relaunch, updateDir, digest, "outside its staging directory"},
		{"installer in a subdirectory of staging", filepath.Join(updateDir, "nested", name), relaunch, updateDir, digest, "outside its staging directory"},
		{"staging directory chosen by the caller", filepath.Join(local, "elsewhere", name), relaunch, filepath.Join(local, "elsewhere"), digest, "staged only in"},
		{"foreign relaunch target", installer, filepath.Join(local, "other.exe"), updateDir, digest, "relaunch target"},
		{"relaunch target outside the installation", installer, filepath.Join(local, "nevr-desktop.exe"), updateDir, digest, "outside the NEVR installation"},
		{"mismatched installer digest", installer, relaunch, updateDir, strings.Repeat("cd", 32), "installer name is invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUpdateHelperPaths(tt.installer, tt.relaunch, tt.updateDir, tt.digest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestVerifyUpdateStagingDir(t *testing.T) {
	updateDir := testUpdateDir(t)
	local := os.Getenv("LOCALAPPDATA")
	if err := verifyUpdateStagingDir(updateDir); err != nil {
		t.Fatalf("staging directory that does not exist yet was refused: %v", err)
	}
	if err := verifyUpdateStagingDir(strings.ToUpper(updateDir) + `\.`); err != nil {
		t.Fatalf("equivalent spelling of the staging directory was refused: %v", err)
	}
	// The pre-fix location: "updates" beside a database configured elsewhere.
	shared := filepath.Join(t.TempDir(), "team-share", "updates")
	if err := verifyUpdateStagingDir(shared); err == nil || !strings.Contains(err.Error(), "staged only in") {
		t.Fatalf("staging beside a relocated database was accepted: %v", err)
	}
	t.Run("unavailable profile", func(t *testing.T) {
		for _, value := range []string{"", "relative-profile"} {
			t.Setenv("LOCALAPPDATA", value)
			if err := verifyUpdateStagingDir(updateDir); err == nil || !strings.Contains(err.Error(), "LOCALAPPDATA") {
				t.Fatalf("LOCALAPPDATA=%q accepted: %v", value, err)
			}
		}
	})
	// guard: the Lstat link check. A junction needs no privilege, and would
	// silently redirect the download and the helper copy to its target.
	for _, linked := range []string{updateDir, filepath.Dir(updateDir)} {
		t.Run("junction at "+filepath.Base(linked), func(t *testing.T) {
			target := t.TempDir()
			if err := os.RemoveAll(filepath.Join(local, "NEVR-Anticheat")); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(linked), 0o700); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", linked, target).CombinedOutput(); err != nil {
				t.Skipf("cannot create a junction here: %v: %s", err, out)
			}
			defer os.Remove(linked)
			if err := verifyUpdateStagingDir(updateDir); err == nil || !strings.Contains(err.Error(), "is a link") {
				t.Fatalf("junction %s was accepted as the staging directory: %v", linked, err)
			}
		})
	}
	if err := os.RemoveAll(filepath.Join(local, "NEVR-Anticheat")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "NEVR-Anticheat"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyUpdateStagingDir(updateDir); err == nil {
		t.Fatal("a file in place of the data directory was accepted")
	}
}

func TestDownloadVerifiedUpdateRefusesForeignStagingDirectory(t *testing.T) {
	setInstalledBuild(t, strings.Repeat("b", 40))
	f := newUpdateFixture(t)
	rt := f.start(t)
	rt.updateDir = filepath.Join(t.TempDir(), "team-share", "updates")
	_, _, err := rt.downloadVerifiedUpdate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "staged only in") {
		t.Fatalf("update staged beside a relocated database: %v", err)
	}
	if f.refRequests+f.releaseRequests+f.assetRequests != 0 {
		t.Fatal("the release was contacted before the staging directory was refused")
	}
	if _, statErr := os.Stat(rt.updateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused staging directory was created: %v", statErr)
	}
}

// The helper used to take --update-dir from its own command line and compare
// it only with --installer from the same command line. It now derives the
// directory from where the helper copy itself runs.
func TestRunUpdateHelperDerivesStagingFromItsOwnLocation(t *testing.T) {
	updateDir := testUpdateDir(t)
	local := os.Getenv("LOCALAPPDATA")
	if err := os.MkdirAll(updateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("ab", 32)
	relaunch := filepath.Join(local, "Programs", "NEVR-Anticheat", "nevr-desktop.exe")
	helper := filepath.Join(updateDir, "nevr-update-helper-aaaaaaaaaaaa.exe")
	arguments := func(installer string, extra ...string) []string {
		return append([]string{updateHelperMode, "--installer", installer, "--sha256", digest, "--wait-pid", "4242",
			"--relaunch-exe", relaunch, "--relaunch-args", "WyItLW5vLWJyb3dzZXIiXQ"}, extra...)
	}
	applied := 0
	apply := func(installer, expectedHash string, waitPID int, relaunchExe string, relaunchArgs []string, gotDir string) error {
		applied++
		if !samePath(gotDir, updateDir) || !samePath(filepath.Dir(installer), updateDir) || expectedHash != digest || waitPID != 4242 ||
			relaunchExe != relaunch || len(relaunchArgs) != 1 || relaunchArgs[0] != "--no-browser" {
			t.Errorf("apply(%q, %q, %d, %q, %q, %q)", installer, expectedHash, waitPID, relaunchExe, relaunchArgs, gotDir)
		}
		return nil
	}
	if err := runUpdateHelper(arguments(filepath.Join(updateDir, stagedInstallerName(digest))), helper, apply); err != nil || applied != 1 {
		t.Fatalf("valid helper invocation: err=%v applied=%d", err, applied)
	}

	foreign := t.TempDir()
	for _, tt := range []struct {
		name, helper string
		args         []string
		want         string
	}{
		// The exact proxy-execution invocation from the finding: every path is
		// chosen by the caller and they are consistent with each other.
		{"caller-chosen directory", filepath.Join(foreign, "nevr-update-helper-aaaaaaaaaaaa.exe"),
			arguments(filepath.Join(foreign, stagedInstallerName(digest))), "staged only in"},
		{"helper in staging, installer elsewhere", helper,
			arguments(filepath.Join(foreign, stagedInstallerName(digest))), "outside its staging directory"},
		{"update-dir is no longer an argument", helper,
			arguments(filepath.Join(foreign, stagedInstallerName(digest)), "--update-dir", foreign), "not defined"},
		{"installed desktop run in helper mode", filepath.Join(updateDir, "nevr-desktop.exe"),
			arguments(filepath.Join(updateDir, stagedInstallerName(digest))), "only available to the staged update helper"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := applied
			err := runUpdateHelper(tt.args, tt.helper, apply)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
			if applied != before {
				t.Fatal("a refused helper invocation still ran the installer step")
			}
		})
	}
}
