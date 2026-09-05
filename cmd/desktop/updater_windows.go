//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const updateHelperMode = "--nevr-update-helper"

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	written, err := io.Copy(h, io.LimitReader(f, maxUpdateInstaller+1))
	if err != nil {
		return "", err
	}
	if written > maxUpdateInstaller {
		return "", errors.New("staged installer exceeds the size safety limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func updateInstallSupport() (bool, string) {
	exe, err := os.Executable()
	if err != nil {
		return false, "Windows could not locate the running NEVR executable"
	}
	local := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if local == "" {
		return false, "LOCALAPPDATA is unavailable"
	}
	exeDir, err := filepath.Abs(filepath.Dir(exe))
	if err != nil {
		return false, "Windows could not resolve the NEVR installation folder"
	}
	installedDir, err := filepath.Abs(filepath.Join(local, "Programs", "NEVR-Anticheat"))
	if err != nil || !strings.EqualFold(filepath.Clean(exeDir), filepath.Clean(installedDir)) {
		return false, "One-click updates require the installed app; run NEVR-Anticheat-Setup.exe once to convert this portable copy"
	}
	return true, ""
}

func launchUpdateHelper(req updateLaunchRequest) error {
	installer, err := filepath.Abs(req.InstallerPath)
	if err != nil {
		return err
	}
	updateDir, err := filepath.Abs(req.UpdateDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(updateDir, installer)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("staged installer is outside the update directory")
	}
	currentExe, err := os.Executable()
	if err != nil {
		return err
	}
	currentExe, err = filepath.Abs(currentExe)
	if err != nil {
		return err
	}
	commit := strings.ToLower(strings.TrimSpace(req.LatestCommit))
	if !validCommitID(commit) {
		return errors.New("update helper received an invalid release revision")
	}
	expectedHash := strings.ToLower(strings.TrimSpace(req.ExpectedHash))
	if len(expectedHash) != 64 {
		return errors.New("update helper received an invalid installer checksum")
	}
	if _, err := hex.DecodeString(expectedHash); err != nil {
		return errors.New("update helper received an invalid installer checksum")
	}
	helperPath := filepath.Join(updateDir, "nevr-update-helper-"+commit[:12]+".exe")
	if err := copyUpdateHelper(currentExe, helperPath); err != nil {
		return fmt.Errorf("stage update helper: %w", err)
	}
	relaunchJSON, err := json.Marshal(os.Args[1:])
	if err != nil {
		return err
	}
	relaunchArgs := base64.RawURLEncoding.EncodeToString(relaunchJSON)
	// #nosec G702 -- helperPath is constructed from NEVR's private update directory
	// and a validated hexadecimal release revision, then populated from this executable.
	cmd := exec.Command(helperPath,
		updateHelperMode,
		"--installer", installer,
		"--sha256", expectedHash,
		"--wait-pid", strconv.Itoa(os.Getpid()),
		"--relaunch-exe", currentExe,
		"--relaunch-args", relaunchArgs,
		"--update-dir", updateDir,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func copyUpdateHelper(source, target string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func maybeRunUpdateHelper(args []string) (bool, error) {
	if len(args) == 0 || args[0] != updateHelperMode {
		return false, nil
	}
	fs := flag.NewFlagSet("nevr-update-helper", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	installer := fs.String("installer", "", "")
	expectedHash := fs.String("sha256", "", "")
	waitPID := fs.Int("wait-pid", 0, "")
	relaunchExe := fs.String("relaunch-exe", "", "")
	relaunchEncoded := fs.String("relaunch-args", "", "")
	updateDir := fs.String("update-dir", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return true, err
	}
	if *installer == "" || *expectedHash == "" || *waitPID <= 0 || *relaunchExe == "" || *updateDir == "" {
		return true, errors.New("update helper arguments are incomplete")
	}
	if err := validateUpdateHelperPaths(*installer, *relaunchExe, *updateDir, *expectedHash); err != nil {
		return true, err
	}
	if len(*relaunchEncoded) > 64<<10 {
		return true, errors.New("update helper relaunch arguments are too large")
	}
	rawArgs, err := base64.RawURLEncoding.DecodeString(*relaunchEncoded)
	if err != nil {
		return true, errors.New("update helper relaunch arguments are invalid")
	}
	var relaunchArgs []string
	if err := json.Unmarshal(rawArgs, &relaunchArgs); err != nil {
		return true, errors.New("update helper relaunch arguments are invalid")
	}
	return true, applyStagedUpdate(*installer, *expectedHash, *waitPID, *relaunchExe, relaunchArgs, *updateDir)
}

func validateUpdateHelperPaths(installer, relaunchExe, updateDir, expectedHash string) error {
	installer, err := filepath.Abs(installer)
	if err != nil {
		return errors.New("update helper installer path is invalid")
	}
	updateDir, err = filepath.Abs(updateDir)
	if err != nil {
		return errors.New("update helper directory is invalid")
	}
	rel, err := filepath.Rel(updateDir, installer)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("update helper installer is outside its staging directory")
	}
	digest, err := stagedInstallerSHA256(installer)
	if err != nil || !strings.EqualFold(digest, strings.TrimSpace(expectedHash)) {
		return errors.New("update helper installer name is invalid")
	}
	if !strings.EqualFold(filepath.Base(relaunchExe), "nevr-desktop.exe") {
		return errors.New("update helper relaunch target is not NEVR desktop")
	}
	local := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	installedDir := filepath.Join(local, "Programs", "NEVR-Anticheat")
	if local == "" || !strings.EqualFold(filepath.Clean(filepath.Dir(relaunchExe)), filepath.Clean(installedDir)) {
		return errors.New("update helper relaunch target is outside the NEVR installation")
	}
	return nil
}

func applyStagedUpdate(installer, expectedHash string, waitPID int, relaunchExe string, relaunchArgs []string, updateDir string) error {
	return applyStagedUpdateWith(installer, expectedHash, waitPID, relaunchExe, relaunchArgs, updateDir, waitForDesktopExit, relaunchUpdatedDesktop)
}

func applyStagedUpdateWith(installer, expectedHash string, waitPID int, relaunchExe string, relaunchArgs []string, updateDir string,
	waitForExit func(int, time.Duration) bool, relaunchDesktop func(string, []string) error,
) (resultErr error) {
	if !waitForExit(waitPID, 30*time.Second) {
		err := errors.New("NEVR did not close within 30 seconds; the installer was not started")
		writeUpdateFailure(updateDir, err)
		return err
	}
	// Once the original desktop has exited, every remaining path must attempt to
	// reopen it. This includes a second-checksum failure and an installer error;
	// otherwise a safe update refusal would still strand the user with no app.
	defer func() {
		if err := relaunchDesktop(relaunchExe, relaunchArgs); err != nil {
			err = fmt.Errorf("relaunch NEVR: %w", err)
			resultErr = errors.Join(resultErr, err)
			writeUpdateFailure(updateDir, resultErr)
		}
	}()
	actualHash, err := fileSHA256(installer)
	if err != nil || !strings.EqualFold(actualHash, expectedHash) {
		if err == nil {
			err = errors.New("staged installer changed after verification")
		} else {
			err = fmt.Errorf("verify staged installer again: %w", err)
		}
		writeUpdateFailure(updateDir, err)
		return err
	}
	logPath := filepath.Join(updateDir, "installer.log")
	cmd := exec.Command(installer,
		"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-", "/CLOSEAPPLICATIONS", "/LOG="+logPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	installErr := cmd.Run()
	_ = os.Remove(installer)
	if installErr != nil {
		writeUpdateFailure(updateDir, installErr)
	} else {
		_ = os.Remove(filepath.Join(updateDir, "last-update-error.txt"))
	}

	return installErr
}

func relaunchUpdatedDesktop(relaunchExe string, relaunchArgs []string) error {
	relaunch := exec.Command(relaunchExe, relaunchArgs...)
	relaunch.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := relaunch.Start(); err != nil {
		return err
	}
	return relaunch.Process.Release()
}

func writeUpdateFailure(updateDir string, err error) {
	_ = os.WriteFile(filepath.Join(updateDir, "last-update-error.txt"), []byte(time.Now().Format(time.RFC3339)+" "+err.Error()+"\n"), 0o600)
}

func waitForDesktopExit(pid int, timeout time.Duration) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		_, _ = process.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
