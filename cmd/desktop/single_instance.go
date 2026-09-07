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

const (
	instanceProtocol = "NEVR-ANTICHEAT-DESKTOP/2 "
	instanceProbes   = 8
)

// desktopInstance owns a tiny loopback rendezvous listener derived from the
// database path. A second launch receives the already-running app URL and can
// bring that window back instead of competing for SQLite and opening a dead
// browser tab.
type desktopInstance struct {
	listener  net.Listener
	identity  string
	ready     chan struct{}
	done      chan struct{}
	once      sync.Once
	closeOnce sync.Once
	mu        sync.RWMutex
	url       string
}

func desktopInstanceDigest(dbPath string) [32]byte {
	abs, err := filepath.Abs(dbPath)
	if err == nil {
		dbPath = abs
	}
	dbPath = filepath.Clean(dbPath)
	if runtime.GOOS == "windows" {
		dbPath = strings.ToLower(dbPath)
	}
	return sha256.Sum256([]byte(dbPath))
}

func desktopInstanceAddress(dbPath string) string {
	digest := desktopInstanceDigest(dbPath)
	return desktopInstanceProbeAddress(digest, 0)
}

func desktopInstanceProbeAddress(digest [32]byte, probe int) string {
	// Stay below the default Windows ephemeral range while spreading separate
	// databases across 10,000 loopback ports.
	port := 32000 + (int(binary.BigEndian.Uint16(digest[:2]))+probe)%10000
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func claimDesktopInstance(dbPath string) (*desktopInstance, string) {
	digest := desktopInstanceDigest(dbPath)
	identity := fmt.Sprintf("%x", digest)
	var reserved *desktopInstance
	for probe := 0; probe < instanceProbes; probe++ {
		address := desktopInstanceProbeAddress(digest, probe)
		ln, err := net.Listen("tcp", address)
		if err == nil {
			if reserved == nil {
				reserved = &desktopInstance{listener: ln, identity: identity, ready: make(chan struct{}), done: make(chan struct{})}
				go reserved.serve()
			} else {
				_ = ln.Close()
			}
			// Keep searching: a matching instance may already own a later
			// probe after an earlier colliding database has shut down.
			continue
		}
		if existing, readErr := readDesktopInstance(address, identity); readErr == nil {
			if reserved != nil {
				_ = reserved.close()
			}
			return nil, existing
		}
	}
	// A port collision is not a database identity. Legacy/unrelated replies
	// are never handed off; if all probes are occupied, run without rendezvous.
	return reserved, ""
}

func (d *desktopInstance) publish(appURL string) {
	d.mu.Lock()
	d.url = appURL
	d.mu.Unlock()
	d.once.Do(func() { close(d.ready) })
}

func (d *desktopInstance) close() (err error) {
	d.closeOnce.Do(func() {
		close(d.done)
		err = d.listener.Close()
	})
	return err
}

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
	// Publish identity immediately so another database does not wait for this
	// app's startup/migrations before discovering that the port merely collided.
	if _, err := fmt.Fprintln(conn, instanceProtocol+d.identity); err != nil {
		return
	}
	select {
	case <-d.ready:
	case <-d.done:
		return
	case <-time.After(8 * time.Second):
		return
	}
	d.mu.RLock()
	appURL := d.url
	d.mu.RUnlock()
	_, _ = fmt.Fprintln(conn, appURL)
}

func readDesktopInstance(address, identity string) (string, error) {
	conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	reader := bufio.NewReaderSize(conn, 512)
	// ReadSlice enforces the fixed buffer limit; ReadString can allocate an
	// unbounded reply from an unrelated local listener until its deadline.
	header, err := reader.ReadSlice('\n')
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(header)) != instanceProtocol+identity {
		return "", fmt.Errorf("unexpected desktop-instance response")
	}
	_ = conn.SetReadDeadline(time.Now().Add(9 * time.Second))
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return "", err
	}
	appURL := strings.TrimSpace(string(line))
	parsed, err := url.Parse(appURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid desktop-instance URL")
	}
	return appURL, nil
}
