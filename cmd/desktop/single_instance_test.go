package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopSingleInstanceDatabaseHashCollision(t *testing.T) {
	// Pigeonhole search guarantees a port collision without a production hook
	// or changing the database identity calculation.
	seen := make(map[string]string)
	dir := t.TempDir()
	var a, b string
	for i := 0; i <= 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("database-%d.db", i))
		address := desktopInstanceAddress(candidate)
		if prior, ok := seen[address]; ok {
			a, b = prior, candidate
			break
		}
		seen[address] = candidate
	}
	if a == "" || desktopInstanceDigest(a) == desktopInstanceDigest(b) {
		t.Fatal("expected two distinct database identities on the same initial port")
	}
	first, running := claimDesktopInstance(a, true)
	if first == nil || running {
		t.Fatalf("first database claim: %v, %v", first, running)
	}
	t.Cleanup(func() { _ = first.close() })
	firstShown, secondShown := make(chan struct{}, 8), make(chan struct{}, 8)
	first.publish(func() { firstShown <- struct{}{} })
	second, running := claimDesktopInstance(b, true)
	if second == nil || running {
		t.Fatalf("colliding database handed off to the wrong app: %v, %v", second, running)
	}
	t.Cleanup(func() { _ = second.close() })
	if first.listener.Addr().String() == second.listener.Addr().String() {
		t.Fatal("different databases did not use different rendezvous ports")
	}
	second.publish(func() { secondShown <- struct{}{} })
	for i, closeFirst := range []bool{false, true} {
		if closeFirst {
			_ = first.close()
		}
		next, running := claimDesktopInstance(b, true)
		if next != nil {
			_ = next.close()
		}
		if next != nil || !running || len(secondShown) != i+1 || len(firstShown) != 0 {
			t.Fatalf("duplicate/re-routed second database after closeFirst=%v: %v, %v (first shown %d, second shown %d)",
				closeFirst, next, running, len(firstShown), len(secondShown))
		}
	}
}

// The rendezvous port is predictable and open to every local process. It used
// to write the app URL, access token included, to whoever connected. Mutation:
// write anything derived from the published state before the client MAC is
// verified and this test sees it.
func TestDesktopInstanceTellsAnUnauthenticatedClientNothing(t *testing.T) {
	db := filepath.Join(t.TempDir(), "evidence.db")
	instance, _ := claimDesktopInstance(db, true)
	if instance == nil {
		t.Fatal("no rendezvous")
	}
	defer instance.close()
	shown := make(chan struct{}, 4)
	instance.publish(func() { shown <- struct{}{} })
	identity := fmt.Sprintf("%x", desktopInstanceDigest(db))

	for name, hello := range map[string]string{
		"silent":         "",
		"legacy":         "NEVR-ANTICHEAT-DESKTOP/2 " + identity + "\n",
		"right identity": instanceProtocol + " " + identity + " " + instanceActionShow + " " + strings.Repeat("0", 32) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := net.Dial("tcp", instance.listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, hello)
			_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
			reader := bufio.NewReader(conn)
			first, _ := reader.ReadString('\n')
			// A wrong client MAC: the caller does not know the secret.
			_, _ = io.WriteString(conn, strings.Repeat("0", 64)+"\n")
			rest, _ := io.ReadAll(reader)
			reply := first + string(rest)
			if strings.Contains(reply, "http") || strings.Contains(reply, "OK") || strings.Contains(reply, "127.0.0.1") {
				t.Fatalf("unauthenticated client received %q", reply)
			}
			if name != "right identity" && reply != "" {
				t.Fatalf("a stranger received %q", reply)
			}
		})
	}
	if len(shown) != 0 {
		t.Fatal("an unauthenticated client made the app open a window")
	}
}

func instanceReplyListener(t *testing.T, reply string, stall bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, reply)
		if stall {
			<-stop
		}
	}()
	return ln.Addr().String()
}

// A process that binds the predictable port first used to be believed: its
// reply made the real app exit and open the squatter's URL in an app window.
// Mutation: accept the server line without checking its MAC.
func TestDesktopInstanceRejectsSquattersAndUnboundedReplies(t *testing.T) {
	identity := strings.Repeat("ab", 32)
	key := []byte(strings.Repeat("k", 32))
	validURL := "http://127.0.0.1:54321/test/"
	for _, tt := range []struct {
		name, reply string
	}{
		{"legacy v1", "NEVR-ANTICHEAT-DESKTOP/1 " + validURL + "\n"},
		{"legacy v2 squatter", "NEVR-ANTICHEAT-DESKTOP/2 " + identity + "\n" + validURL + "\n"},
		{"forged proof", strings.Repeat("0", 32) + " " + strings.Repeat("0", 64) + "\nOK\n"},
		{"proof under another key", strings.Repeat("1", 32) + " " + instanceMAC([]byte(strings.Repeat("x", 32)), "server", identity, instanceActionShow, strings.Repeat("2", 32), strings.Repeat("1", 32)) + "\nOK\n"},
		{"unrelated listener", "HTTP/1.1 200 OK\r\n"},
		{"oversize reply", strings.Repeat("a", 4096) + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			address := instanceReplyListener(t, tt.reply, false)
			if err := askDesktopInstance(address, identity, key, instanceActionShow); err == nil {
				t.Fatal("an unauthenticated listener was accepted as the running app")
			}
		})
	}
	t.Run("unresponsive listener", func(t *testing.T) {
		address := instanceReplyListener(t, "", true)
		started := time.Now()
		if err := askDesktopInstance(address, identity, key, instanceActionShow); err == nil {
			t.Fatal("silent listener accepted")
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("unrelated listener blocked startup beyond the bounded identity read")
		}
	})
}

// With the first probe port squatted the app still starts, keeps its own
// rendezvous on a later probe, and a second launch finds the real app.
func TestDesktopInstanceStartsNormallyBehindASquatter(t *testing.T) {
	db := filepath.Join(t.TempDir(), "evidence.db")
	squatter, err := net.Listen("tcp", desktopInstanceAddress(db))
	if err != nil {
		t.Skipf("probe port unavailable on this machine: %v", err)
	}
	defer squatter.Close()
	identity := fmt.Sprintf("%x", desktopInstanceDigest(db))
	go func() {
		for {
			conn, err := squatter.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(conn, "NEVR-ANTICHEAT-DESKTOP/2 "+identity+"\nhttp://127.0.0.1:6666/looks-like-nevr/\n")
			_ = conn.Close()
		}
	}()
	instance, running := claimDesktopInstance(db, true)
	if instance == nil || running {
		t.Fatalf("squatter stopped the real app from starting: %v, %v", instance, running)
	}
	defer instance.close()
	shown := make(chan struct{}, 1)
	instance.publish(func() { shown <- struct{}{} })
	if second, running := claimDesktopInstance(db, true); second != nil || !running || len(shown) != 1 {
		t.Fatalf("second launch behind a squatter: %v, %v, shown=%d", second, running, len(shown))
	}
}
