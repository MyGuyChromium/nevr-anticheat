package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	instanceProtocol = "NEVR-ANTICHEAT-DESKTOP/3"
	instanceProbes   = 8
	// instanceKeyFileName holds the per-installation rendezvous secret beside
	// the database. Whoever can read it can read the database itself, so it
	// grants nothing the data folder's own permissions do not already grant.
	instanceKeyFileName = "nevr-desktop-instance.key"

	instanceActionShow = "show" // bring the running app's window back
	instanceActionPing = "ping" // only find out whether the app is running
)

// desktopInstance owns a tiny loopback rendezvous listener derived from the
// database path, so a second launch brings the running app's window back
// instead of competing for SQLite.
//
// The listener is reachable by every local process, including other Windows
// accounts on a shared PC, and its port is predictable. It therefore never
// sends the app URL (whose random token is the app's only access control):
// both sides prove knowledge of the secret in the data folder with an HMAC
// challenge-response, and the running instance then opens its own window. A
// process that squats the port cannot impersonate the app, and a process that
// merely connects learns nothing.
type desktopInstance struct {
	listener  net.Listener
	identity  string
	key       []byte
	ready     chan struct{}
	done      chan struct{}
	once      sync.Once
	closeOnce sync.Once
	mu        sync.RWMutex
	show      func()
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

func instanceKeyPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), instanceKeyFileName)
}

// loadInstanceKey reads the rendezvous secret, creating it when create is set
// and it does not exist yet. Two launches racing to create it agree on the
// one that won the exclusive create.
func loadInstanceKey(dbPath string, create bool) ([]byte, error) {
	path := instanceKeyPath(dbPath)
	for attempt := 0; attempt < 2; attempt++ {
		doc, err := os.ReadFile(path)
		if err == nil {
			key, decodeErr := hex.DecodeString(strings.TrimSpace(string(doc)))
			if decodeErr != nil || len(key) != 32 {
				return nil, errors.New("desktop-instance key is damaged")
			}
			return key, nil
		}
		if !errors.Is(err, os.ErrNotExist) || !create {
			return nil, err
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // another launch created it first; read theirs
		}
		if err != nil {
			return nil, err
		}
		_, writeErr := f.WriteString(hex.EncodeToString(key) + "\n")
		if closeErr := f.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(path)
			return nil, writeErr
		}
		return key, nil
	}
	return nil, errors.New("desktop-instance key could not be created")
}

func instanceMAC(key []byte, role, identity, action, clientNonce, serverNonce string) string {
	mac := hmac.New(sha256.New, key)
	for _, part := range []string{instanceProtocol, role, identity, action, clientNonce, serverNonce} {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func instanceNonce() (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce), nil
}

// claimDesktopInstance returns the rendezvous this launch now owns, or
// running=true when an authenticated instance for the same database is
// already running (it was asked to show its window when showWindow is set).
func claimDesktopInstance(dbPath string, showWindow bool) (instance *desktopInstance, running bool) {
	// The secret lives beside the database file; a URI or in-memory database
	// has no such place (and is only used by tests and tools).
	if query := strings.IndexByte(dbPath, '?'); query >= 1 && !strings.HasPrefix(dbPath, "file:") {
		dbPath = dbPath[:query]
	}
	if dbPath == "" || dbPath == ":memory:" || strings.HasPrefix(dbPath, "file:") {
		return nil, false
	}
	digest := desktopInstanceDigest(dbPath)
	identity := fmt.Sprintf("%x", digest)
	key, keyErr := loadInstanceKey(dbPath, true)
	if keyErr != nil {
		// Without a shared secret neither side can be trusted; run without a
		// rendezvous rather than trusting an unauthenticated one.
		return nil, false
	}
	action := instanceActionPing
	if showWindow {
		action = instanceActionShow
	}
	var reserved *desktopInstance
	for probe := 0; probe < instanceProbes; probe++ {
		address := desktopInstanceProbeAddress(digest, probe)
		ln, err := net.Listen("tcp", address)
		if err == nil {
			if reserved == nil {
				reserved = &desktopInstance{listener: ln, identity: identity, key: key, ready: make(chan struct{}), done: make(chan struct{})}
				go reserved.serve()
			} else {
				_ = ln.Close()
			}
			// Keep searching: a matching instance may already own a later
			// probe after an earlier colliding database has shut down.
			continue
		}
		if askDesktopInstance(address, identity, key, action) == nil {
			if reserved != nil {
				_ = reserved.close()
			}
			return nil, true
		}
	}
	// A port collision is not a database identity, and a listener that cannot
	// prove the secret is not this app. If all probes are occupied, run
	// without rendezvous.
	return reserved, false
}

// publish marks the app as started; show brings its window back on request.
func (d *desktopInstance) publish(show func()) {
	d.mu.Lock()
	d.show = show
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

// answer runs the server half of the handshake:
//
//	client: PROTOCOL <identity> <action> <clientNonce>
//	server: <serverNonce> <MAC(server, ...)>      proves this is the app
//	client: <MAC(client, ...)>                    proves the caller is the user
//	server: OK                                    after showing the window
//
// Nothing secret is ever written to the connection.
func (d *desktopInstance) answer(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReaderSize(conn, 512)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(string(line))
	if len(fields) != 4 || fields[0] != instanceProtocol || fields[1] != d.identity || len(fields[3]) != 32 ||
		(fields[2] != instanceActionShow && fields[2] != instanceActionPing) {
		return // another database on a colliding port, a legacy client or a stranger
	}
	action, clientNonce := fields[2], fields[3]
	serverNonce, err := instanceNonce()
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(conn, "%s %s\n", serverNonce, instanceMAC(d.key, "server", d.identity, action, clientNonce, serverNonce)); err != nil {
		return
	}
	line, err = reader.ReadSlice('\n')
	if err != nil {
		return
	}
	want := instanceMAC(d.key, "client", d.identity, action, clientNonce, serverNonce)
	if !hmac.Equal([]byte(strings.TrimSpace(string(line))), []byte(want)) {
		return
	}
	if action == instanceActionShow {
		_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
		select {
		case <-d.ready:
		case <-d.done:
			return
		case <-time.After(8 * time.Second):
			return
		}
		d.mu.RLock()
		show := d.show
		d.mu.RUnlock()
		if show != nil {
			show()
		}
	}
	_, _ = fmt.Fprintln(conn, "OK")
}

// askDesktopInstance runs the client half. nil means the listener proved it is
// this database's app; whether the window then came back is best effort.
func askDesktopInstance(address, identity string, key []byte, action string) error {
	conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	clientNonce, err := instanceNonce()
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := fmt.Fprintf(conn, "%s %s %s %s\n", instanceProtocol, identity, action, clientNonce); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(conn, 512)
	// ReadSlice enforces the fixed buffer limit; ReadString can allocate an
	// unbounded reply from an unrelated local listener until its deadline.
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return err
	}
	fields := strings.Fields(string(line))
	if len(fields) != 2 || len(fields[0]) != 32 ||
		!hmac.Equal([]byte(fields[1]), []byte(instanceMAC(key, "server", identity, action, clientNonce, fields[0]))) {
		return errors.New("the listener did not prove it is this database's NEVR-Anticheat")
	}
	if _, err := fmt.Fprintln(conn, instanceMAC(key, "client", identity, action, clientNonce, fields[0])); err != nil {
		return nil // authenticated; the request to show the window was lost
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, _ = reader.ReadSlice('\n') // wait for the window; an instance still starting may take a while
	return nil
}
