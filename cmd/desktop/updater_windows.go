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
	"unsafe"
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

// trustedUpdateDir is the one directory NEVR stages an update into and
// executes it from: %LOCALAPPDATA%\NEVR-Anticheat\updates. It is resolved from
// the user profile and from nothing else. The evidence database may be
// configured anywhere (installed.toml db_path can name a shared, synced or
// network folder whose ACLs NEVR does not control), so "next to the database"
// is not a place to run executables from.
func trustedUpdateDir() (string, error) {
	local := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if local == "" || !filepath.IsAbs(local) {
		return "", errors.New("LOCALAPPDATA is unavailable, so NEVR has no private update directory")
	}
	return filepath.Join(filepath.Clean(local), "NEVR-Anticheat", "updates"), nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// verifyUpdateStagingDir accepts dir only when it IS the trusted update
// directory and neither it nor its parent has been replaced by a junction or
// symbolic link that would redirect staging somewhere else.
//
// This is a same-user control. It cannot stop another process running as the
// same Windows user from racing the final hash check and the launch.
func verifyUpdateStagingDir(dir string) error {
	trusted, err := trustedUpdateDir()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return errors.New("update staging directory is invalid")
	}
	if !samePath(abs, trusted) {
		return fmt.Errorf("one-click updates are staged only in %s, but this installation keeps its data in %s; download and run NEVR-Anticheat-Setup.exe instead", trusted, filepath.Dir(abs))
	}
	for _, path := range []string{filepath.Dir(trusted), trusted} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect update staging directory: %w", err)
		}
		// A symbolic link is ModeSymlink. A junction is ModeSymlink or a
		// non-directory ModeIrregular depending on the Go version's winsymlink
		// setting; every spelling of "not a plain directory" is refused.
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !info.IsDir() {
			return fmt.Errorf("refusing to stage an update through %s because it is a link or not a directory", path)
		}
	}
	return nil
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
	// Refuse invalid helper inputs while the desktop is still running. A helper
	// that exits during argument validation cannot bring the desktop back.
	if err := validateUpdateHelperPaths(installer, currentExe, updateDir, expectedHash); err != nil {
		return err
	}
	// The download took time; another NEVR program may have been started since
	// the preflight at the beginning of downloadVerifiedUpdate.
	if err := updatePreflight(); err != nil {
		return err
	}
	relaunchJSON, err := json.Marshal(stripUpdateOverrideArgs(os.Args[1:]))
	if err != nil {
		return err
	}
	relaunchArgs := base64.RawURLEncoding.EncodeToString(relaunchJSON)
	if len(relaunchArgs) > 64<<10 {
		return errors.New("update helper relaunch arguments are too large")
	}
	helperPath := filepath.Join(updateDir, "nevr-update-helper-"+commit[:12]+".exe")
	if err := copyUpdateHelper(currentExe, helperPath); err != nil {
		return fmt.Errorf("stage update helper: %w", err)
	}
	// #nosec G702 -- helperPath is %LOCALAPPDATA%\NEVR-Anticheat\updates (verified
	// above to be exactly that directory and not a link) plus a validated
	// hexadecimal release revision, and was just populated from this executable.
	// The helper takes no directory argument: it derives the staging directory
	// from its own location and re-verifies it, see runUpdateHelper.
	cmd := exec.Command(helperPath,
		updateHelperMode,
		"--installer", installer,
		"--sha256", expectedHash,
		"--wait-pid", strconv.Itoa(os.Getpid()),
		"--relaunch-exe", currentExe,
		"--relaunch-args", relaunchArgs,
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
	helperExe, err := os.Executable()
	if err != nil {
		return true, errors.New("update helper could not locate itself")
	}
	return true, runUpdateHelper(args, helperExe, applyStagedUpdate)
}

// runUpdateHelper is the helper-mode entry point of the shipped binary. The
// helper is a copy of nevr-desktop.exe placed INSIDE the staging directory, so
// the staging directory is wherever the helper itself is running from. It used
// to be a command-line argument validated only against the installer argument
// beside it, which made "the installer is inside the staging directory" true
// for any pair of paths a caller chose and let the shipped binary hidden-launch
// an arbitrary correctly-named executable and write logs to any directory.
func runUpdateHelper(args []string, helperExe string,
	apply func(installer, expectedHash string, waitPID int, relaunchExe string, relaunchArgs []string, updateDir string) error,
) error {
	helperExe, err := filepath.Abs(helperExe)
	if err != nil {
		return errors.New("update helper could not locate itself")
	}
	updateDir := filepath.Dir(helperExe)
	if name := strings.ToLower(filepath.Base(helperExe)); !strings.HasPrefix(name, "nevr-update-helper-") || !strings.HasSuffix(name, ".exe") {
		return errors.New("update helper mode is only available to the staged update helper")
	}
	fs := flag.NewFlagSet("nevr-update-helper", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	installer := fs.String("installer", "", "")
	expectedHash := fs.String("sha256", "", "")
	waitPID := fs.Int("wait-pid", 0, "")
	relaunchExe := fs.String("relaunch-exe", "", "")
	relaunchEncoded := fs.String("relaunch-args", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *installer == "" || *expectedHash == "" || *waitPID <= 0 || *relaunchExe == "" {
		return errors.New("update helper arguments are incomplete")
	}
	if err := validateUpdateHelperPaths(*installer, *relaunchExe, updateDir, *expectedHash); err != nil {
		return err
	}
	if len(*relaunchEncoded) > 64<<10 {
		return errors.New("update helper relaunch arguments are too large")
	}
	rawArgs, err := base64.RawURLEncoding.DecodeString(*relaunchEncoded)
	if err != nil {
		return errors.New("update helper relaunch arguments are invalid")
	}
	var relaunchArgs []string
	if err := json.Unmarshal(rawArgs, &relaunchArgs); err != nil {
		return errors.New("update helper relaunch arguments are invalid")
	}
	return apply(*installer, *expectedHash, *waitPID, *relaunchExe, relaunchArgs, updateDir)
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
	if err := verifyUpdateStagingDir(updateDir); err != nil {
		return err
	}
	// The installer must be a file directly inside the staging directory, not
	// merely somewhere below it: downloadVerifiedUpdate never creates
	// subdirectories, so anything deeper was not staged by NEVR.
	if !samePath(filepath.Dir(installer), updateDir) {
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
	waitForExit func(int, time.Duration) error, relaunchDesktop func(string, []string) error,
) (resultErr error) {
	if err := waitForExit(waitPID, 30*time.Second); err != nil {
		err = fmt.Errorf("the installer was not started: %w", err)
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

func waitForDesktopExit(pid int, timeout time.Duration) error {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return errors.New("NEVR process ID is invalid")
	}
	// Request only the right needed for waiting. os.Process.Wait also queries
	// process details, whose failure is not evidence that the process exited.
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// A positive PID which no longer exists produces ERROR_INVALID_PARAMETER.
		// The desktop may have already exited before its helper started.
		const errorInvalidParameter syscall.Errno = 87
		if errors.Is(err, errorInvalidParameter) {
			return nil
		}
		return fmt.Errorf("could not open the NEVR process for exit verification: %w", err)
	}
	defer syscall.CloseHandle(handle)
	return waitForDesktopHandle(handle, timeout)
}

func waitForDesktopHandle(handle syscall.Handle, timeout time.Duration) error {
	// A bounded native wait leaves no goroutine or open process handle behind
	// when the desktop fails to shut down. Never convert a timeout to INFINITE.
	milliseconds := max(time.Duration(0), timeout/time.Millisecond)
	if timeout > 0 && timeout%time.Millisecond != 0 {
		milliseconds++
	}
	milliseconds = min(milliseconds, time.Duration(syscall.INFINITE-1))
	event, err := syscall.WaitForSingleObject(handle, uint32(milliseconds))
	if err != nil {
		return fmt.Errorf("could not verify that NEVR exited: %w", err)
	}
	switch event {
	case syscall.WAIT_OBJECT_0:
		return nil
	case syscall.WAIT_TIMEOUT:
		return fmt.Errorf("NEVR did not close within %s", timeout)
	default:
		return fmt.Errorf("unexpected Windows process wait result: %d", event)
	}
}

// The installer replaces all five NEVR programs and runs with
// /CLOSEAPPLICATIONS, so Windows Restart Manager is asked to close every
// process using a payload file: a live nevr-server.exe (telemetry ingest) or
// nevr-bridge.exe started from the install folder, not just the desktop. The
// helper waits for and reopens ONLY the desktop. A one-click update would then
// end live capture with no message, or, if such a process does not exit, abort
// after some executables were already replaced. The PowerShell installer and
// rollback refuse in exactly this situation (Assert-NEVRProgramsClosed); the
// one-click path now follows the same rule and terminates nothing.
var nevrProgramNames = map[string]bool{
	"nevr-desktop.exe": true, "nevr-ac.exe": true, "nevr-server.exe": true,
	"nevr-bridge.exe": true, "nevr-compat.exe": true,
}

type runningProgram struct {
	PID   uint32
	Name  string // executable file name as the process list reports it
	Image string // full image path; empty when Windows would not disclose it
}

// listRunningPrograms is replaced in tests that must not depend on what else is
// running on the machine.
var listRunningPrograms = listNEVRProcesses

var procQueryFullProcessImageName = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

func listNEVRProcesses() ([]runningProgram, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("list running processes: %w", err)
	}
	defer syscall.CloseHandle(snapshot)
	var entry syscall.ProcessEntry32
	// #nosec G103 G115 -- Process32First requires the structure's own size, a
	// small compile-time constant; no pointer arithmetic is involved.
	entry.Size = uint32(unsafe.Sizeof(entry))
	var programs []runningProgram
	for err = syscall.Process32First(snapshot, &entry); err == nil; err = syscall.Process32Next(snapshot, &entry) {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if nevrProgramNames[strings.ToLower(name)] {
			programs = append(programs, runningProgram{PID: entry.ProcessID, Name: name, Image: processImagePath(entry.ProcessID)})
		}
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("list running processes: %w", err)
	}
	return programs, nil
}

func processImagePath(pid uint32) string {
	const processQueryLimitedInformation = 0x1000
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(handle)
	const maxImagePath = 32768 // the longest path Windows can return, in UTF-16 units
	buffer := make([]uint16, maxImagePath)
	size := uint32(maxImagePath)
	// #nosec G103 -- QueryFullProcessImageNameW writes at most size UTF-16 units
	// into buffer and stores the written length in size; both outlive the call.
	ok, _, _ := procQueryFullProcessImageName.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || size > maxImagePath {
		return ""
	}
	return syscall.UTF16ToString(buffer[:size])
}

// refuseUpdateWhileProgramsRun reports the NEVR programs, other than this
// process, that run from installDir. A NEVR-named process whose location
// Windows will not disclose is refused too: "probably somewhere else" is not a
// reason to let an installer close it.
func refuseUpdateWhileProgramsRun(installDir string, self uint32, programs []runningProgram) error {
	for _, program := range programs {
		if program.PID == self {
			continue
		}
		if program.Image == "" {
			return fmt.Errorf("cannot verify whether %s (process %d) runs from this installation; close it and update again. No process was terminated", program.Name, program.PID)
		}
		if samePath(filepath.Dir(program.Image), installDir) {
			return fmt.Errorf("close %s (process %d) before updating: the installer replaces every NEVR program in %s and reopens only the desktop app. No process was terminated", program.Name, program.PID, installDir)
		}
	}
	return nil
}

// updatePreflight runs before anything is downloaded and again immediately
// before the helper is launched.
func updatePreflight() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	programs, err := listRunningPrograms()
	if err != nil {
		return err
	}
	self := os.Getpid()
	if self <= 0 || uint64(self) > uint64(^uint32(0)) {
		return errors.New("NEVR process ID is invalid")
	}
	return refuseUpdateWhileProgramsRun(filepath.Dir(exe), uint32(self), programs)
}
