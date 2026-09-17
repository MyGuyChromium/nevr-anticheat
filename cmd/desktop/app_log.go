package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
)

// guiSubsystem is set to "true" by the release packaging scripts together
// with the -H=windowsgui linker flag. Such a build has no console, so
// everything it would have printed goes to a size-rotated log file under the
// data directory instead. A plain `go build` keeps console logging.
var guiSubsystem = "false"

const (
	appLogDirName   = "logs"
	appLogFileName  = "nevr-desktop.log"
	crashLogName    = "nevr-desktop-crash.log"
	appLogMaxBytes  = 5 << 20
	appLogKeepFiles = 3
)

// logToFile decides where logs go: a GUI-subsystem build logs to the file
// unless --console asks for the parent console; any build can opt in with
// --log-file.
func logToFile(guiBuild, console, forceFile bool) bool {
	return forceFile || (guiBuild && !console)
}

// rotatingLog is an append-only log file that is rotated by size:
// nevr-desktop.log -> .1 -> .2 -> .3, the oldest dropped. Rotation happens
// between writes, so a line is never split across files.
type rotatingLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	file     *os.File
	size     int64
}

func openRotatingLog(path string, maxBytes int64, keep int) (*rotatingLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &rotatingLog{path: path, maxBytes: maxBytes, keep: keep}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.file, l.size = f, info.Size()
	return nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.maxBytes {
		if err := l.rotate(); err != nil {
			// Keep logging to the current file rather than losing the line.
			if l.file == nil {
				if openErr := l.open(); openErr != nil {
					return 0, openErr
				}
			}
		}
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate closes the current file (Windows cannot rename an open one), shifts
// the older generations and starts a new file.
func (l *rotatingLog) rotate() error {
	if err := l.file.Close(); err != nil {
		return err
	}
	l.file = nil
	_ = os.Remove(fmt.Sprintf("%s.%d", l.path, l.keep))
	for i := l.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1))
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return err
	}
	return l.open()
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// fileLogging is the active redirection of stdout and stderr into the log.
type fileLogging struct {
	path    string
	log     *rotatingLog
	writer  *os.File
	crash   *os.File
	drained chan struct{}
	prevOut *os.File
	prevErr *os.File
}

// startFileLogging points os.Stdout and os.Stderr at the rotated log under
// dataDir. It must run before the engine logger is built: that logger captures
// os.Stderr once. Fatal runtime crashes (which bypass os.Stderr) are appended
// to a separate crash file, because a GUI process has no standard handles for
// the Go runtime to write them to.
func startFileLogging(dataDir string) (*fileLogging, error) {
	dir := filepath.Join(dataDir, appLogDirName)
	log, err := openRotatingLog(filepath.Join(dir, appLogFileName), appLogMaxBytes, appLogKeepFiles)
	if err != nil {
		return nil, fmt.Errorf("opening the app log: %w", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = log.Close()
		return nil, fmt.Errorf("opening the app log pipe: %w", err)
	}
	fl := &fileLogging{path: log.path, log: log, writer: writer, drained: make(chan struct{}), prevOut: os.Stdout, prevErr: os.Stderr}
	go func() {
		defer close(fl.drained)
		_, _ = io.Copy(log, reader)
		_ = reader.Close()
	}()
	os.Stdout, os.Stderr = writer, writer
	if crash, crashErr := os.OpenFile(filepath.Join(dir, crashLogName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); crashErr == nil {
		if debug.SetCrashOutput(crash, debug.CrashOptions{}) == nil {
			fl.crash = crash
		} else {
			_ = crash.Close()
		}
	}
	return fl, nil
}

// stop flushes what was written and restores the previous streams.
func (fl *fileLogging) stop() {
	if fl == nil {
		return
	}
	os.Stdout, os.Stderr = fl.prevOut, fl.prevErr
	_ = fl.writer.Close()
	<-fl.drained
	_ = fl.log.Close()
	if fl.crash != nil {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		_ = fl.crash.Close()
	}
}
