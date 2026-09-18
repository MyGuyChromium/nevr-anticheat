//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are the refusals downloadVerifiedUpdate makes before any network
// traffic. The check must report them up front instead of after the click.
func TestUpdateCheckReportsInstallRefusalsBeforeTheClick(t *testing.T) {
	t.Run("installed copy with the trusted staging directory is supported", func(t *testing.T) {
		setInstalledBuild(t, strings.Repeat("b", 40))
		status, _ := getUpdateStatus(t, updateCheckServer(t, newUpdateFixture(t)))
		if !status.InstallSupported || status.InstallUnsupportedReason != "" || status.InstallReason != "" {
			t.Fatalf("install support = %v %q", status.InstallSupported, status.InstallUnsupportedReason)
		}
	})
	t.Run("relocated database path", func(t *testing.T) {
		setInstalledBuild(t, strings.Repeat("b", 40))
		f := newUpdateFixture(t)
		s, ts := newTestServer(t)
		rt := f.start(t)
		s.runtime.updateURL = rt.updateURL
		s.runtime.updateReady = func() (bool, string) { return true, "" }
		// newTestServer keeps its database, and therefore its updates folder,
		// outside the (test) profile: exactly a relocated db_path.
		if samePath(s.runtime.updateDir, rt.updateDir) {
			t.Fatal("test setup: the server's update directory is already the trusted one")
		}
		status, _ := getUpdateStatus(t, ts.URL+"/"+testToken+"/api/update")
		if status.InstallSupported || !strings.Contains(status.InstallUnsupportedReason, "NEVR-Anticheat-Setup.exe") ||
			!strings.Contains(status.InstallUnsupportedReason, "this installation keeps its data in") {
			t.Fatalf("install support = %v %q", status.InstallSupported, status.InstallUnsupportedReason)
		}
		if !status.Available {
			t.Fatalf("the newer release must still be reported for manual download: %+v", status)
		}
	})
	t.Run("another NEVR program runs from the installation folder", func(t *testing.T) {
		setInstalledBuild(t, strings.Repeat("b", 40))
		url := updateCheckServer(t, newUpdateFixture(t))
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		stubRunningPrograms(t, []runningProgram{{PID: 7, Name: "nevr-server.exe", Image: filepath.Join(filepath.Dir(self), "nevr-server.exe")}})
		status, _ := getUpdateStatus(t, url)
		if status.InstallSupported || !strings.HasPrefix(status.InstallUnsupportedReason, "Close nevr-server.exe") {
			t.Fatalf("install support = %v %q", status.InstallSupported, status.InstallUnsupportedReason)
		}
	})
}
