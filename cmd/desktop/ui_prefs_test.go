package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putPrefs(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func getPrefs(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("GET ui-prefs status=%d type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	return string(bytes.TrimSpace(body))
}

func TestUIPrefsRoundTripBesideTheDatabase(t *testing.T) {
	s, ts := newTestServer(t)
	url := ts.URL + "/" + testToken + "/api/ui-prefs"
	if got := getPrefs(t, url); got != "{}" {
		t.Fatalf("empty preferences = %q, want {}", got)
	}
	const doc = `{"theme":"light","density":"compact","blind":true,"drafts":{"SYN-FIXTURE-001":"check the second throw"}}`
	if resp := putPrefs(t, url, doc); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status=%d, want 204", resp.StatusCode)
	}
	if got := getPrefs(t, url); got != doc {
		t.Fatalf("stored preferences = %s", got)
	}

	// Persisted beside the database (where the desktop settings file lives),
	// so they survive the random port of the next launch.
	dataDir := filepath.Dir(s.engine.Store().Path())
	if filepath.Dir(s.runtime.settingsPath) != dataDir {
		t.Fatalf("settings live in %s, database in %s", filepath.Dir(s.runtime.settingsPath), dataDir)
	}
	onDisk, err := os.ReadFile(filepath.Join(dataDir, uiPrefsFileName))
	if err != nil || string(onDisk) != doc {
		t.Fatalf("file = %s err=%v", onDisk, err)
	}
	reopened := &uiPrefsStore{path: filepath.Join(dataDir, uiPrefsFileName)}
	if again, err := reopened.load(); err != nil || string(again) != doc {
		t.Fatalf("a new process would read %s err=%v", again, err)
	}
	entries, _ := os.ReadDir(dataDir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("atomic write left %s behind", entry.Name())
		}
	}
}

func TestUIPrefsRejectsAnythingButOneBoundedObject(t *testing.T) {
	_, ts := newTestServer(t)
	url := ts.URL + "/" + testToken + "/api/ui-prefs"
	if resp := putPrefs(t, url, `{"theme":"dark"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("seed status=%d", resp.StatusCode)
	}
	for name, body := range map[string]string{
		"array":         `["light"]`,
		"string":        `"light"`,
		"number":        `7`,
		"null":          `null`,
		"empty":         ``,
		"malformed":     `{"theme":`,
		"two documents": `{"a":1}{"b":2}`,
	} {
		if resp := putPrefs(t, url, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400", name, resp.StatusCode)
		}
	}
	exact := `{"pad":"` + strings.Repeat("x", int(maxUIPrefsBytes)-len(`{"pad":""}`)) + `"}`
	if len(exact) != int(maxUIPrefsBytes) {
		t.Fatalf("test document is %d bytes", len(exact))
	}
	if resp := putPrefs(t, url, exact); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a 64 KiB document was refused: %d", resp.StatusCode)
	}
	tooBig := `{"pad":"` + strings.Repeat("x", int(maxUIPrefsBytes)) + `"}`
	if resp := putPrefs(t, url, tooBig); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized document status=%d, want 413", resp.StatusCode)
	}
	// Rejected writes never replace what was stored.
	if got := getPrefs(t, url); got != exact {
		t.Fatalf("a rejected write changed the stored preferences (%d bytes)", len(got))
	}
}

func TestUIPrefsDamagedFileReadsAsEmpty(t *testing.T) {
	s, ts := newTestServer(t)
	url := ts.URL + "/" + testToken + "/api/ui-prefs"
	if err := os.WriteFile(s.uiPrefs.path, []byte(`{"theme":"li`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := getPrefs(t, url); got != "{}" {
		t.Fatalf("damaged file served as %q; the page must still start", got)
	}
	if resp := putPrefs(t, url, `{"theme":"light"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("could not replace a damaged file: %d", resp.StatusCode)
	}
	if got := getPrefs(t, url); got != `{"theme":"light"}` {
		t.Fatalf("after repair = %s", got)
	}
}
