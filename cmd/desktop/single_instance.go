package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const instanceProtocol = "NEVR-ANTICHEAT-DESKTOP/1 "

// desktopInstance owns a tiny loopback rendezvous listener derived from the
// database path. A second launch receives the already-running app URL and can
// bring that window back instead of competing for SQLite and opening a dead
// browser tab.
type desktopInstance struct {
	listener net.Listener
	ready    chan struct{}
	once     sync.Once
	mu       sync.RWMutex
	url      string
}

func desktopInstanceAddress(dbPath string) string {
	abs, err := filepath.Abs(dbPath)
	if err == nil {
		dbPath = abs
	}
	dbPath = filepath.Clean(dbPath)
	if runtime.GOOS == "windows" {
		dbPath = strings.ToLower(dbPath)
	}
	digest := sha256.Sum256([]byte(dbPath))
	// Stay below the default Windows ephemeral range while spreading separate
	// databases across 10,000 loopback ports.
	port := 32000 + int(binary.BigEndian.Uint16(digest[:2]))%10000
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func claimDesktopInstance(dbPath string) (*desktopInstance, string) {
	address := desktopInstanceAddress(dbPath)
	ln, err := net.Listen("tcp", address)
	if err == nil {
		instance := &desktopInstance{listener: ln, ready: make(chan struct{})}
		go instance.serve()
		return instance, ""
	}
	// A valid NEVR response means this is our existing instance. If an
	// unrelated local program happens to own the derived port, fail open and
	// run normally rather than preventing NEVR from starting.
	if existing, readErr := readDesktopInstance(address); readErr == nil {
		return nil, existing
	}
	return nil, ""
}

func (d *desktopInstance) publish(appURL string) {
	d.mu.Lock()
	d.url = appURL
	d.mu.Unlock()
	d.once.Do(func() { close(d.ready) })
}

func (d *desktopInstance) close() error { return d.listener.Close() }

func (d *desktopInstance) serve() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		go d.answer(conn)
	}
}

func (d *desktopInstance) answer(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	select {
	case <-d.ready:
	case <-time.After(8 * time.Second):
		return
	}
	d.mu.RLock()
	appURL := d.url
	d.mu.RUnlock()
	_, _ = fmt.Fprintln(conn, instanceProtocol+appURL)
}

func readDesktopInstance(address string) (string, error) {
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(9 * time.Second))
	line, err := bufio.NewReaderSize(conn, 2048).ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, instanceProtocol) {
		return "", fmt.Errorf("unexpected desktop-instance response")
	}
	appURL := strings.TrimSpace(strings.TrimPrefix(line, instanceProtocol))
	parsed, err := url.Parse(appURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path == "" {
		return "", fmt.Errorf("invalid desktop-instance URL")
	}
	return appURL, nil
}
