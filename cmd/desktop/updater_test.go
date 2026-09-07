package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type updateRoundTripper func(*http.Request) (*http.Response, error)

func (f updateRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubGetRedirectSecurity(t *testing.T) {
	t.Setenv("NEVR_GITHUB_TOKEN", "private-test-token")
	for _, tt := range []struct {
		name, target         string
		wantToken, wantError bool
	}{
		{"same origin", "https://api.github.com/asset", true, false},
		{"different port", "https://api.github.com:8443/asset", false, false},
		{"subdomain", "https://assets.api.github.com/asset", false, false},
		{"download host", "https://release-assets.githubusercontent.com/asset", false, false},
		{"insecure downgrade", "http://api.github.com/asset", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: updateRoundTripper(func(r *http.Request) (*http.Response, error) {
				requests++
				if requests == 1 {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {tt.target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				if got := r.Header.Get("Authorization") != ""; got != tt.wantToken {
					t.Errorf("redirect forwarded private token = %v, want %v", got, tt.wantToken)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("asset")), Request: r}, nil
			})}
			rt := &desktopRuntime{updateURL: "https://api.github.com"}
			resp, err := rt.githubGet(context.Background(), client, rt.updateURL+"/start", "")
			if resp != nil {
				resp.Body.Close()
			}
			if (err != nil) != tt.wantError {
				t.Fatalf("redirect error = %v, want error %v", err, tt.wantError)
			}
			if tt.wantError && requests != 1 {
				t.Fatal("insecure redirect was contacted")
			}
			if client.CheckRedirect != nil {
				t.Fatal("shared HTTP client was modified")
			}
		})
	}
}

func TestGitHubGetPreservesRedirectPolicy(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/again", http.StatusFound) }))
	defer api.Close()
	rt := &desktopRuntime{updateURL: api.URL}
	blocked := errors.New("caller blocked redirect")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return blocked }}
	resp, err := rt.githubGet(context.Background(), client, api.URL, "")
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, blocked) {
		t.Fatalf("caller redirect policy was ignored: %v", err)
	}
	resp, err = rt.githubGet(context.Background(), &http.Client{}, api.URL, "")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("default redirect limit was lost: %v", err)
	}
}

func TestSameHTTPOrigin(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"https://api.github.com/repos/a", "https://API.GITHUB.COM/repos/b", true},
		{"http://127.0.0.1:123/a", "http://127.0.0.1:123/b", true},
		{"https://api.github.com/a", "http://api.github.com/b", false},
		{"https://api.github.com/a", "https://github.com/b", false},
		{"file:///tmp/a", "file:///tmp/b", false},
		{"not a url", "https://api.github.com", false},
	}
	for _, tt := range tests {
		if got := sameHTTPOrigin(tt.a, tt.b); got != tt.want {
			t.Errorf("sameHTTPOrigin(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestGitHubGetRestrictsPrivateTokenToUpdateOrigin(t *testing.T) {
	t.Setenv("NEVR_GITHUB_TOKEN", "private-test-token")
	trustedAuth, untrustedAuth := "", ""
	trusted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trustedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer trusted.Close()
	untrusted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		untrustedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer untrusted.Close()
	rt := &desktopRuntime{updateURL: trusted.URL}
	client := &http.Client{Timeout: time.Second}
	for _, target := range []string{trusted.URL + "/asset", untrusted.URL + "/asset"} {
		resp, err := rt.githubGet(context.Background(), client, target, "")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if trustedAuth != "Bearer private-test-token" {
		t.Fatalf("trusted Authorization = %q", trustedAuth)
	}
	if untrustedAuth != "" {
		t.Fatalf("private token leaked to another origin: %q", untrustedAuth)
	}
}

func TestParsePublishedChecksum(t *testing.T) {
	digest := strings.Repeat("ab", sha256.Size)
	for _, input := range []string{digest + "  " + updateInstallerName + "\n", digest + " *" + updateInstallerName + "\r\n"} {
		got, err := parsePublishedChecksum([]byte(input))
		if err != nil || got != digest {
			t.Fatalf("parsePublishedChecksum() = %q, %v", got, err)
		}
	}
	for _, input := range []string{"", digest, digest + "  other.exe", "xyz  " + updateInstallerName} {
		if _, err := parsePublishedChecksum([]byte(input)); err == nil {
			t.Errorf("parsePublishedChecksum(%q) unexpectedly succeeded", input)
		}
	}
}

func TestDownloadVerifiedUpdate(t *testing.T) {
	oldCommit := strings.Repeat("b", 40)
	previousCommit := buildCommit
	buildCommit = oldCommit
	t.Cleanup(func() { buildCommit = previousCommit })
	for _, tt := range []struct {
		name   string
		tamper bool
	}{
		{"verified", false},
		{"tampered download", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installer := []byte("signed setup fixture")
			served := append([]byte(nil), installer...)
			if tt.tamper {
				served[0] ^= 0xff
			}
			sum := sha256.Sum256(installer)
			digest := hex.EncodeToString(sum[:])
			commit := strings.Repeat("a", 40)
			manifest := updateManifest{SchemaVersion: 1, Tag: updateTag, Commit: commit, Version: "0.10.0"}
			manifest.Installer.Name = updateInstallerName
			manifest.Installer.SHA256 = digest
			manifest.Installer.Size = int64(len(installer))

			refRequests := 0
			var api *httptest.Server
			api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/git/ref/tags/" + updateTag:
					refRequests++
					_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": commit}})
				case "/releases/tags/" + updateTag:
					_ = json.NewEncoder(w).Encode(githubRelease{TagName: updateTag, Assets: []githubReleaseAsset{
						{Name: updateInstallerName, URL: api.URL + "/assets/installer", Size: int64(len(installer))},
						{Name: updateChecksumName, URL: api.URL + "/assets/checksum"},
						{Name: updateManifestName, URL: api.URL + "/assets/manifest"},
					}})
				case "/assets/installer":
					_, _ = w.Write(served)
				case "/assets/checksum":
					_, _ = io.WriteString(w, digest+"  "+updateInstallerName+"\n")
				case "/assets/manifest":
					_ = json.NewEncoder(w).Encode(manifest)
				default:
					http.NotFound(w, r)
				}
			}))
			defer api.Close()

			rt := &desktopRuntime{
				updateURL:    api.URL,
				httpClient:   &http.Client{Timeout: time.Second},
				updateClient: &http.Client{Timeout: time.Second},
				updateDir:    filepath.Join(t.TempDir(), "updates"),
			}
			path, gotCommit, err := rt.downloadVerifiedUpdate(context.Background())
			if tt.tamper {
				if err == nil || !strings.Contains(err.Error(), "SHA-256") {
					t.Fatalf("tampered installer error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotCommit != commit || refRequests != 2 {
				t.Fatalf("commit=%q ref requests=%d", gotCommit, refRequests)
			}
			if stagedHash, err := stagedInstallerSHA256(path); err != nil || stagedHash != digest {
				t.Fatalf("staged digest = %q, %v", stagedHash, err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(installer) {
				t.Fatalf("staged installer = %q", got)
			}
		})
	}
}

func TestValidateUpdateManifestRejectsUnboundRelease(t *testing.T) {
	commit := strings.Repeat("a", 40)
	hash := strings.Repeat("b", 64)
	asset := githubReleaseAsset{Name: updateInstallerName, Size: 10}
	valid := updateManifest{SchemaVersion: 1, Tag: updateTag, Commit: commit, Version: "0.10.0"}
	valid.Installer.Name, valid.Installer.SHA256, valid.Installer.Size = updateInstallerName, hash, 10
	if err := validateUpdateManifest(valid, commit, hash, asset); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*updateManifest)
	}{
		{"wrong commit", func(m *updateManifest) { m.Commit = strings.Repeat("c", 40) }},
		{"wrong hash", func(m *updateManifest) { m.Installer.SHA256 = strings.Repeat("d", 64) }},
		{"wrong size", func(m *updateManifest) { m.Installer.Size++ }},
		{"missing version", func(m *updateManifest) { m.Version = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valid
			tt.edit(&got)
			if err := validateUpdateManifest(got, commit, hash, asset); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
}

func TestInstallUpdateRejectsActiveAnalysis(t *testing.T) {
	s, ts := newTestServer(t)
	_, analysisID := s.beginAnalysis()
	defer s.finishAnalysis(analysisID)
	resp := postAPI(t, ts.URL+"/"+testToken+"/api/update/install", nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	if s.runtime.updateInProgress() {
		t.Fatal("failed update attempt left the runtime locked")
	}
}
