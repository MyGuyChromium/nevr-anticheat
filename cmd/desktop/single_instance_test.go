package main

import (
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
	first, existing := claimDesktopInstance(a)
	if first == nil || existing != "" {
		t.Fatalf("first database claim: %v, %q", first, existing)
	}
	t.Cleanup(func() { _ = first.close() })
	first.publish("http://127.0.0.1:54321/first/")
	second, existing := claimDesktopInstance(b)
	if second == nil || existing != "" {
		t.Fatalf("colliding database handed off to the wrong app: %v, %q", second, existing)
	}
	t.Cleanup(func() { _ = second.close() })
	if first.listener.Addr().String() == second.listener.Addr().String() {
		t.Fatal("different databases did not use different rendezvous ports")
	}
	secondURL := "http://127.0.0.1:54322/second/"
	second.publish(secondURL)
	for _, closeFirst := range []bool{false, true} {
		if closeFirst {
			_ = first.close()
		}
		next, url := claimDesktopInstance(b)
		if next != nil {
			_ = next.close()
		}
		if next != nil || url != secondURL {
			t.Fatalf("duplicate/re-routed second database after closeFirst=%v: %v, %q", closeFirst, next, url)
		}
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

func TestDesktopInstanceRejectsUnrelatedLegacyAndUnboundedReplies(t *testing.T) {
	identity := strings.Repeat("ab", 32)
	validURL := "http://127.0.0.1:54321/test/"
	for _, tt := range []struct {
		name, reply string
	}{
		{"legacy", "NEVR-ANTICHEAT-DESKTOP/1 " + validURL + "\n"},
		{"wrong database", instanceProtocol + strings.Repeat("cd", 32) + "\n" + validURL + "\n"},
		{"unrelated listener", "HTTP/1.1 200 OK\r\n"},
		{"oversize identity", instanceProtocol + strings.Repeat("a", 2048) + "\n"},
		{"oversize URL", instanceProtocol + identity + "\nhttp://127.0.0.1:1234/" + strings.Repeat("a", 2048) + "\n"},
		{"foreign URL", instanceProtocol + identity + "\nhttps://example.com/\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			address := instanceReplyListener(t, tt.reply, false)
			if url, err := readDesktopInstance(address, identity); err == nil || url != "" {
				t.Fatalf("untrusted response accepted: url=%q err=%v", url, err)
			}
		})
	}
	t.Run("unresponsive listener", func(t *testing.T) {
		address := instanceReplyListener(t, "", true)
		started := time.Now()
		if url, err := readDesktopInstance(address, identity); err == nil || url != "" {
			t.Fatalf("silent listener accepted: url=%q err=%v", url, err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("unrelated listener blocked startup beyond the bounded identity read")
		}
	})
}
