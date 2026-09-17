package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// updateCheckServer wires a test server to a fake rolling release. The install
// support answer is pinned to "supported" unless a test replaces it, so the
// update-availability cases below do not depend on where the test binary runs.
func updateCheckServer(t *testing.T, f *updateFixture) string {
	t.Helper()
	s, ts := newTestServer(t)
	rt := f.start(t)
	s.runtime.updateURL, s.runtime.updateDir = rt.updateURL, rt.updateDir
	s.runtime.httpClient, s.runtime.updateClient = rt.httpClient, rt.updateClient
	s.runtime.updateReady = func() (bool, string) { return true, "" }
	return ts.URL + "/" + testToken + "/api/update"
}

// getUpdateStatus decodes the response twice: into the struct, and into a map so
// a test can tell an absent field from a false one.
func getUpdateStatus(t *testing.T, url string) (updateStatus, map[string]any) {
	t.Helper()
	resp, err := http.Get(url) // #nosec G107 -- loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET api/update = %d %s", resp.StatusCode, raw)
	}
	var status updateStatus
	var fields map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return status, fields
}

func TestUpdateCheckOffersOnlyANewerRelease(t *testing.T) {
	setInstalledBuild(t, strings.Repeat("b", 40))
	f := newUpdateFixture(t)
	status, fields := getUpdateStatus(t, updateCheckServer(t, f))
	if !status.Available || status.Downgrade || status.DowngradeReason != "" || status.Error != "" {
		t.Fatalf("a newer release was not offered: %+v", status)
	}
	if status.LatestCommit != f.commit || status.LatestCommitTime != publishedCommitTime ||
		status.CurrentCommitTime != installedCommitTime || !status.CurrentCommitTimeExact {
		t.Fatalf("release ordering facts are wrong: %+v", status)
	}
	if value, present := fields["downgrade"]; !present || value != false {
		t.Fatalf("downgrade must always be present as a boolean, got %v (present=%v)", value, present)
	}
	if f.installerRequests != 0 {
		t.Fatal("an update CHECK downloaded the installer")
	}
}

// The rolling tag can be moved backwards (an out-of-order or re-run publish).
// A check that only compares commit ids then offers a downgrade as an update.
func TestUpdateCheckReportsADowngradeInsteadOfAnUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, commitTime, want string
	}{
		{"older release", "2026-02-27T09:00:00Z", "is not newer than this build"},
		{"same commit time", installedCommitTime, "is not newer than this build"},
		{"release without a commit time", "", "does not state when its source was committed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setInstalledBuild(t, strings.Repeat("b", 40))
			f := newUpdateFixture(t)
			f.manifest.CommitTime = tc.commitTime
			status, _ := getUpdateStatus(t, updateCheckServer(t, f))
			if status.Available {
				t.Fatalf("a release the install path refuses was offered as an update: %+v", status)
			}
			if !status.Downgrade || !strings.Contains(status.DowngradeReason, tc.want) {
				t.Fatalf("downgrade=%v reason=%q, want a reason containing %q", status.Downgrade, status.DowngradeReason, tc.want)
			}
			if status.Error != "" {
				t.Fatalf("a completed check reported an error: %q", status.Error)
			}
			if first := status.DowngradeReason[:1]; first != strings.ToUpper(first) || !strings.HasSuffix(status.DowngradeReason, ".") {
				t.Fatalf("downgrade_reason is not a plain sentence: %q", status.DowngradeReason)
			}
			// Whether the page should advertise the command-line override is the
			// owner's call; the check reports the reason only.
			if strings.Contains(status.DowngradeReason, allowDowngradeFlag) {
				t.Fatalf("downgrade_reason advertises the override flag: %q", status.DowngradeReason)
			}
			if f.installerRequests != 0 {
				t.Fatal("an update CHECK downloaded the installer")
			}
		})
	}
}

// The check and the install path must agree, including about the one override.
func TestUpdateCheckFollowsTheInstallPathOverride(t *testing.T) {
	setInstalledBuild(t, strings.Repeat("b", 40))
	*allowUpdateDowngrade = true
	f := newUpdateFixture(t)
	f.manifest.CommitTime = "2026-02-27T09:00:00Z"
	status, _ := getUpdateStatus(t, updateCheckServer(t, f))
	if !status.Available || status.Downgrade {
		t.Fatalf("with --%s the install path accepts this release, but the check says %+v", allowDowngradeFlag, status)
	}
}

func TestUpdateCheckSameRevisionAsksNothingMore(t *testing.T) {
	f := newUpdateFixture(t)
	setInstalledBuild(t, f.commit)
	status, _ := getUpdateStatus(t, updateCheckServer(t, f))
	if status.Available || status.Downgrade || status.Error != "" || status.LatestCommit != f.commit {
		t.Fatalf("an installed build on the published revision is not simply up to date: %+v", status)
	}
	if f.releaseRequests+f.assetRequests != 0 {
		t.Fatalf("release assets were read although the revision already matches (%d release, %d asset requests)", f.releaseRequests, f.assetRequests)
	}
}

// A manifest that is not bound to the tagged revision says nothing about that
// revision's age, so it must neither offer an update nor call it a downgrade.
func TestUpdateCheckDoesNotTrustAnUnboundManifest(t *testing.T) {
	setInstalledBuild(t, strings.Repeat("b", 40))
	f := newUpdateFixture(t)
	f.manifest.Commit = strings.Repeat("c", 40)
	status, _ := getUpdateStatus(t, updateCheckServer(t, f))
	if status.Available || status.Downgrade {
		t.Fatalf("an unbound manifest decided the answer: %+v", status)
	}
	if !strings.Contains(status.Error, "does not match the rolling release revision") {
		t.Fatalf("error = %q", status.Error)
	}
}

func TestUpdateCheckExplainsWhyInstallIsUnsupported(t *testing.T) {
	t.Run("portable copy", func(t *testing.T) {
		setInstalledBuild(t, strings.Repeat("b", 40))
		f := newUpdateFixture(t)
		s, ts := newTestServer(t)
		rt := f.start(t)
		s.runtime.updateURL, s.runtime.updateDir = rt.updateURL, rt.updateDir
		s.runtime.updateReady = func() (bool, string) { return false, "one-click updates require the installed app" }
		status, fields := getUpdateStatus(t, ts.URL+"/"+testToken+"/api/update")
		if status.InstallSupported || status.InstallUnsupportedReason != "One-click updates require the installed app." {
			t.Fatalf("install support = %v %q", status.InstallSupported, status.InstallUnsupportedReason)
		}
		if fields["install_reason"] != fields["install_unsupported_reason"] {
			t.Fatalf("install_reason %q and install_unsupported_reason %q disagree", fields["install_reason"], fields["install_unsupported_reason"])
		}
		if !status.Available {
			t.Fatalf("an unsupported one-click install hid the newer release from manual download: %+v", status)
		}
	})
	t.Run("development build", func(t *testing.T) {
		setInstalledBuild(t, "development")
		f := newUpdateFixture(t)
		status, _ := getUpdateStatus(t, updateCheckServer(t, f))
		if status.InstallSupported || !strings.Contains(status.InstallUnsupportedReason, "development build") {
			t.Fatalf("install support = %v %q", status.InstallSupported, status.InstallUnsupportedReason)
		}
		if status.Available || status.Downgrade {
			t.Fatalf("a development build was offered a release: %+v", status)
		}
	})
	t.Run("unsupported always carries a reason", func(t *testing.T) {
		setInstalledBuild(t, strings.Repeat("b", 40))
		f := newUpdateFixture(t)
		s, ts := newTestServer(t)
		rt := f.start(t)
		s.runtime.updateURL, s.runtime.updateDir = rt.updateURL, rt.updateDir
		s.runtime.updateReady = func() (bool, string) { return false, "" }
		status, _ := getUpdateStatus(t, ts.URL+"/"+testToken+"/api/update")
		if status.InstallSupported || strings.TrimSpace(status.InstallUnsupportedReason) == "" {
			t.Fatalf("install_supported=false without a reason: %+v", status)
		}
	})
}
