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

// updateFixture is a fake GitHub API origin serving one rolling release. Every
// knob defaults to a fully valid release so that each refusal test changes
// exactly one thing; a test that still passes after its guard is deleted would
// therefore be a test of nothing.
type updateFixture struct {
	commit      string
	refCommits  []string // successive tag-ref answers; the last one repeats
	installer   []byte   // bytes the checksum and manifest describe
	served      []byte   // bytes the installer asset URL actually returns
	manifest    updateManifest
	manifestRaw []byte // served verbatim when set
	releaseRaw  []byte // served verbatim when set
	refRaw      []byte // served verbatim when set
	release     func(apiURL string, valid githubRelease) githubRelease

	refRequests, releaseRequests, assetRequests, installerRequests int
	api                                                            *httptest.Server
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	f := &updateFixture{commit: strings.Repeat("a", 40), installer: []byte("integrity-checked setup fixture")}
	f.served = append([]byte(nil), f.installer...)
	sum := sha256.Sum256(f.installer)
	f.manifest = updateManifest{SchemaVersion: 1, Tag: updateTag, Commit: f.commit, CommitTime: publishedCommitTime, Version: "0.10.0"}
	f.manifest.Installer.Name = updateInstallerName
	f.manifest.Installer.SHA256 = hex.EncodeToString(sum[:])
	f.manifest.Installer.Size = int64(len(f.installer))
	return f
}

func (f *updateFixture) digest() string { return f.manifest.Installer.SHA256 }

func (f *updateFixture) start(t *testing.T) *desktopRuntime {
	t.Helper()
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/git/ref/tags/" + updateTag:
			f.refRequests++
			if f.refRaw != nil {
				_, _ = w.Write(f.refRaw)
				return
			}
			commit := f.commit
			if len(f.refCommits) > 0 {
				commit = f.refCommits[min(f.refRequests, len(f.refCommits))-1]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": commit}})
		case "/releases/tags/" + updateTag:
			f.releaseRequests++
			if f.releaseRaw != nil {
				_, _ = w.Write(f.releaseRaw)
				return
			}
			release := githubRelease{TagName: updateTag, Assets: []githubReleaseAsset{
				{Name: updateInstallerName, URL: f.api.URL + "/assets/installer", Size: f.manifest.Installer.Size},
				{Name: updateChecksumName, URL: f.api.URL + "/assets/checksum"},
				{Name: updateManifestName, URL: f.api.URL + "/assets/manifest"},
			}}
			if f.release != nil {
				release = f.release(f.api.URL, release)
			}
			_ = json.NewEncoder(w).Encode(release)
		case "/assets/installer":
			f.assetRequests++
			f.installerRequests++
			_, _ = w.Write(f.served)
		case "/assets/checksum":
			f.assetRequests++
			_, _ = io.WriteString(w, f.digest()+"  "+updateInstallerName+"\n")
		case "/assets/manifest":
			f.assetRequests++
			if f.manifestRaw != nil {
				_, _ = w.Write(f.manifestRaw)
				return
			}
			_ = json.NewEncoder(w).Encode(f.manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.api.Close)
	return &desktopRuntime{
		updateURL:    f.api.URL,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		updateClient: &http.Client{Timeout: 5 * time.Second},
		updateDir:    testUpdateDir(t),
	}
}

// The fixture release was committed one day after the fixture installed build.
const (
	installedCommitTime = "2026-03-01T10:00:00Z"
	publishedCommitTime = "2026-03-02T10:00:00Z"
)

// setInstalledBuild pretends the running binary is a packaged build of commit,
// committed at installedCommitTime. Test binaries carry no VCS stamp and
// buildTime is "unknown", so buildCommitTime alone decides the installed moment.
func setInstalledBuild(t *testing.T, commit string) {
	t.Helper()
	previousCommit, previousTime, previousOverride := buildCommit, buildCommitTime, *allowUpdateDowngrade
	buildCommit, buildCommitTime, *allowUpdateDowngrade = commit, installedCommitTime, false
	t.Cleanup(func() {
		buildCommit, buildCommitTime, *allowUpdateDowngrade = previousCommit, previousTime, previousOverride
	})
}

func assertNothingStaged(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("refused update left %s in the staging directory", entry.Name())
	}
}

func TestDownloadVerifiedUpdate(t *testing.T) {
	setInstalledBuild(t, strings.Repeat("b", 40))
	f := newUpdateFixture(t)
	rt := f.start(t)
	path, gotCommit, err := rt.downloadVerifiedUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotCommit != f.commit || f.refRequests != 2 {
		t.Fatalf("commit=%q ref requests=%d", gotCommit, f.refRequests)
	}
	if filepath.Dir(path) != rt.updateDir {
		t.Fatalf("installer staged at %q, outside %q", path, rt.updateDir)
	}
	if stagedHash, err := stagedInstallerSHA256(path); err != nil || stagedHash != f.digest() {
		t.Fatalf("staged digest = %q, %v", stagedHash, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(f.installer) {
		t.Fatalf("staged installer = %q", got)
	}
}

// The override exists so that a deliberate downgrade stays possible without
// deleting the check; it must skip only the ordering rule and nothing else.
func TestDownloadVerifiedUpdateDowngradeOverride(t *testing.T) {
	setInstalledBuild(t, strings.Repeat("b", 40))
	*allowUpdateDowngrade = true
	f := newUpdateFixture(t)
	f.manifest.CommitTime = "2026-02-27T09:00:00Z"
	rt := f.start(t)
	path, _, err := rt.downloadVerifiedUpdate(context.Background())
	if err != nil {
		t.Fatalf("explicit --%s did not permit an older release: %v", allowDowngradeFlag, err)
	}
	if stagedHash, err := stagedInstallerSHA256(path); err != nil || stagedHash != f.digest() {
		t.Fatalf("staged digest = %q, %v", stagedHash, err)
	}

	tampered := newUpdateFixture(t)
	tampered.manifest.CommitTime = "2026-02-27T09:00:00Z"
	tampered.served[0] ^= 0xff
	rt = tampered.start(t)
	if _, _, err := rt.downloadVerifiedUpdate(context.Background()); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("the downgrade override also skipped integrity checking: %v", err)
	}
}

func TestRefuseUpdateDowngrade(t *testing.T) {
	at := func(value string) time.Time {
		parsed, ok := parseUpdateTime(value)
		if !ok {
			t.Fatalf("bad test time %q", value)
		}
		return parsed
	}
	manifest := func(commitTime string) updateManifest {
		return updateManifest{Commit: strings.Repeat("a", 40), CommitTime: commitTime}
	}
	installed := buildMoment{Commit: strings.Repeat("b", 40), Time: at("2026-03-01T10:00:00Z"), Exact: true}
	for _, tt := range []struct {
		name      string
		published string
		installed buildMoment
		override  bool
		want      string // empty: accepted
	}{
		{"newer", "2026-03-01T10:00:01Z", installed, false, ""},
		{"newer in another zone", "2026-03-01T12:30:00+02:00", installed, false, ""},
		{"older", "2026-03-01T09:59:59Z", installed, false, "refusing to downgrade"},
		{"older in another zone", "2026-03-01T11:30:00+02:00", installed, false, "refusing to downgrade"},
		{"equal", "2026-03-01T10:00:00Z", installed, false, "refusing to downgrade"},
		{"older with override", "2020-01-01T00:00:00Z", installed, true, ""},
		{"missing with override", "", installed, true, ""},
		{"missing", "", installed, false, "does not state"},
		{"unknown installed moment", "2026-03-02T10:00:00Z", buildMoment{Commit: "b"}, false, "does not record"},
		// A build that only knows its build clock names that basis honestly.
		{"older than a build-clock moment", "2026-03-01T09:00:00Z", buildMoment{Commit: "b", Time: at("2026-03-01T10:00:00Z")}, false, "built 2026-03-01T10:00:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := refuseUpdateDowngrade(manifest(tt.published), tt.installed, tt.override)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("newer release refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "--"+allowDowngradeFlag) {
				t.Fatalf("refusal does not name the manual override: %v", err)
			}
		})
	}
}

func TestResolveBuildMoment(t *testing.T) {
	commit := strings.Repeat("b", 40)
	const embedded, stamped, built = "2026-03-01T10:00:00Z", "2026-03-01T11:00:00Z", "2026-03-01T12:00:00Z"
	for _, tt := range []struct {
		name                                  string
		embedded, vcsRevision, vcsTime, built string
		want                                  string
		exact                                 bool
	}{
		{"workflow commit time wins", embedded, commit, stamped, built, embedded, true},
		{"toolchain stamp for the same commit", "", commit, stamped, built, stamped, true},
		// A stamp describing another commit says nothing about this build.
		{"toolchain stamp for another commit is ignored", "", strings.Repeat("c", 40), stamped, built, built, false},
		{"build clock is the last resort", "", "", "", built, built, false},
		{"nothing known", "", "", "", "unknown", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveBuildMoment(commit, tt.embedded, tt.vcsRevision, tt.vcsTime, tt.built)
			want, _ := parseUpdateTime(tt.want)
			if !got.Time.Equal(want) || got.Exact != tt.exact || got.Commit != commit {
				t.Fatalf("moment = %+v, want time %s exact %v", got, tt.want, tt.exact)
			}
		})
	}
}

func TestStripUpdateOverrideArgs(t *testing.T) {
	got := stripUpdateOverrideArgs([]string{"--config", `C:\x\installed.toml`, "--allow-update-downgrade", "-allow-update-downgrade=true",
		"--no-browser", "--", "--allow-update-downgrade"})
	want := []string{"--config", `C:\x\installed.toml`, "--no-browser", "--", "--allow-update-downgrade"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("relaunch arguments = %q, want %q", got, want)
	}
}

// Each case below breaks exactly one property of an otherwise valid release.
// The "guard" comment names the statement in updater.go whose removal makes
// that case fail; every one of them was checked by mutation.
func TestDownloadVerifiedUpdateRefusals(t *testing.T) {
	padded := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		// Trailing whitespace keeps the document valid JSON, so only the size
		// guard, never the decoder, can be what refuses it.
		return append(raw, []byte(strings.Repeat(" ", maxUpdateMetadataSize+6*1024))...)
	}
	for _, tt := range []struct {
		name    string
		arrange func(t *testing.T, f *updateFixture)
		want    string
		// wantNoAssets asserts the refusal happened before any asset download.
		wantNoAssets bool
		// wantNoInstaller asserts the installer itself was never requested.
		wantNoInstaller bool
	}{
		{
			// guard: the refuseUpdateDowngrade call in downloadVerifiedUpdate, and
			// published.After(installed.Time) inside it. This is the rolling tag
			// moved BACKWARDS by an out-of-order or re-run publish.
			name:    "release older than the installed build",
			arrange: func(_ *testing.T, f *updateFixture) { f.manifest.CommitTime = "2026-02-27T09:00:00Z" },
			want:    "refusing to downgrade", wantNoInstaller: true,
		},
		{
			// guard: After is strict. Mutating it to !Before accepts this case.
			name:    "release committed at the same instant as the installed build",
			arrange: func(_ *testing.T, f *updateFixture) { f.manifest.CommitTime = installedCommitTime },
			want:    "refusing to downgrade", wantNoInstaller: true,
		},
		{
			// guard: the !ok branch for an absent commit_time. A release published
			// before this field existed is by definition older than this build.
			name:    "release without a commit time",
			arrange: func(_ *testing.T, f *updateFixture) { f.manifest.CommitTime = "" },
			want:    "does not state when its source was committed", wantNoInstaller: true,
		},
		{
			name:    "release with an unparseable commit time",
			arrange: func(_ *testing.T, f *updateFixture) { f.manifest.CommitTime = "yesterday" },
			want:    "does not state when its source was committed", wantNoInstaller: true,
		},
		{
			// guard: installed.Time.IsZero(). Without it a build that knows nothing
			// about its own age would accept every release.
			name:    "installed build with no recorded time",
			arrange: func(_ *testing.T, _ *updateFixture) { buildCommitTime = "" },
			want:    "does not record when it was built", wantNoInstaller: true,
		},
		{
			// guard: actualHash != publishedHash
			name:    "tampered download",
			arrange: func(_ *testing.T, f *updateFixture) { f.served[0] ^= 0xff },
			want:    "SHA-256",
		},
		{
			// guard: !sameHTTPOrigin(asset.URL, rt.updateURL) in releaseAssets
			name: "asset URL on another origin",
			arrange: func(t *testing.T, f *updateFixture) {
				foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(f.served) }))
				t.Cleanup(foreign.Close)
				f.release = func(_ string, valid githubRelease) githubRelease {
					valid.Assets[0].URL = foreign.URL + "/assets/installer"
					return valid
				}
			},
			want: "points outside", wantNoAssets: true,
		},
		{
			// guard: afterCommit != releaseCommit after staging
			name: "rolling tag moved during download",
			arrange: func(_ *testing.T, f *updateFixture) {
				f.refCommits = []string{f.commit, strings.Repeat("c", 40)}
			},
			want: "newer Windows release appeared",
		},
		{
			// guard: written != manifest.Installer.Size. The served bytes carry a
			// matching published hash, so only the size binding can refuse them.
			name: "download longer than the manifest size",
			arrange: func(_ *testing.T, f *updateFixture) {
				f.served = append(f.served, []byte(" plus appended payload")...)
				sum := sha256.Sum256(f.served)
				f.manifest.Installer.SHA256 = hex.EncodeToString(sum[:])
			},
			want: "downloaded installer size",
		},
		{
			name: "download shorter than the manifest size",
			arrange: func(_ *testing.T, f *updateFixture) {
				f.served = f.served[:len(f.served)-4]
				sum := sha256.Sum256(f.served)
				f.manifest.Installer.SHA256 = hex.EncodeToString(sum[:])
			},
			want: "downloaded installer size",
		},
		{
			// guard: duplicate asset name in releaseAssets
			name: "duplicate asset name",
			arrange: func(_ *testing.T, f *updateFixture) {
				f.release = func(apiURL string, valid githubRelease) githubRelease {
					valid.Assets = append(valid.Assets, githubReleaseAsset{Name: updateInstallerName, URL: apiURL + "/assets/installer"})
					return valid
				}
			},
			want: "duplicate asset", wantNoAssets: true,
		},
		{
			// guard: release.TagName != updateTag
			name: "release metadata for another tag",
			arrange: func(_ *testing.T, f *updateFixture) {
				f.release = func(_ string, valid githubRelease) githubRelease {
					valid.TagName = "v0.0.1"
					return valid
				}
			},
			want: "unexpected release tag", wantNoAssets: true,
		},
		{
			// guard: len(raw) > maxUpdateMetadataSize in decodeUpdateJSON
			name: "oversized release metadata",
			arrange: func(_ *testing.T, f *updateFixture) {
				f.releaseRaw = padded(githubRelease{TagName: updateTag})
			},
			want: "safety limit", wantNoAssets: true,
		},
		{
			// guard: the same decodeUpdateJSON limit, reached through the tag ref
			name: "oversized release revision",
			arrange: func(_ *testing.T, f *updateFixture) {
				f.refRaw = padded(map[string]any{"object": map[string]string{"sha": f.commit}})
			},
			want: "safety limit", wantNoAssets: true,
		},
		{
			// guard: int64(len(data)) > limit in readAsset
			name:    "oversized update manifest",
			arrange: func(_ *testing.T, f *updateFixture) { f.manifestRaw = padded(f.manifest) },
			want:    "safety limit",
		},
		{
			// guard: sameCommit(buildCommit, releaseCommit)
			name:    "already on the published revision",
			arrange: func(t *testing.T, f *updateFixture) { setInstalledBuild(t, f.commit) },
			want:    "already on the latest", wantNoAssets: true,
		},
		{
			// guard: buildCommit == "development"
			name:    "development build",
			arrange: func(t *testing.T, _ *updateFixture) { setInstalledBuild(t, "development") },
			want:    "development builds cannot replace themselves", wantNoAssets: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setInstalledBuild(t, strings.Repeat("b", 40))
			f := newUpdateFixture(t)
			tt.arrange(t, f)
			rt := f.start(t)
			path, _, err := rt.downloadVerifiedUpdate(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v (staged %q), want one containing %q", err, path, tt.want)
			}
			if tt.wantNoAssets && f.assetRequests != 0 {
				t.Errorf("%d asset requests were made before the refusal", f.assetRequests)
			}
			if tt.wantNoInstaller && f.installerRequests != 0 {
				t.Errorf("the installer was downloaded %d times before the refusal", f.installerRequests)
			}
			assertNothingStaged(t, rt.updateDir)
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
