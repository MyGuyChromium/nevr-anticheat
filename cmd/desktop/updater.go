package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	updateTag             = "windows-latest"
	updateInstallerName   = "NEVR-Anticheat-Setup.exe"
	updateChecksumName    = updateInstallerName + ".sha256"
	updateManifestName    = "NEVR-Anticheat-Update.json"
	maxUpdateMetadataSize = 64 << 10
	maxUpdateInstaller    = 512 << 20
)

type updateManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Tag           string `json:"tag"`
	Commit        string `json:"commit"`
	Version       string `json:"version"`
	Installer     struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"installer"`
}

type githubReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type updateLaunchRequest struct {
	InstallerPath string
	UpdateDir     string
	LatestCommit  string
	ExpectedHash  string
}

type updateInstallResult struct {
	LatestCommit string `json:"latest_commit"`
	Message      string `json:"message"`
}

func (rt *desktopRuntime) githubGet(ctx context.Context, client *http.Client, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "NEVR-Anticheat/"+appVersion)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// Release metadata supplies API asset URLs. Only send the private-repository
	// token back to the configured GitHub API origin, never to a redirected or
	// forged third-party URL.
	if token := strings.TrimSpace(os.Getenv("NEVR_GITHUB_TOKEN")); token != "" && sameHTTPOrigin(url, rt.updateURL) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

func sameHTTPOrigin(rawA, rawB string) bool {
	a, errA := url.Parse(rawA)
	b, errB := url.Parse(rawB)
	if errA != nil || errB != nil || (a.Scheme != "http" && a.Scheme != "https") || (b.Scheme != "http" && b.Scheme != "https") {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func responseError(resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("the release is private; set a read-only NEVR_GITHUB_TOKEN once to enable one-click updates")
	}
	return fmt.Errorf("GitHub returned %s", resp.Status)
}

func decodeUpdateJSON(body io.Reader, label string, out any) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxUpdateMetadataSize+1))
	if err != nil {
		return err
	}
	if len(raw) > maxUpdateMetadataSize {
		return fmt.Errorf("%s exceeds the %d-byte safety limit", label, maxUpdateMetadataSize)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	return nil
}

func (rt *desktopRuntime) latestReleaseCommit(ctx context.Context) (string, error) {
	resp, err := rt.githubGet(ctx, rt.httpClient, rt.updateURL+"/git/ref/tags/"+updateTag, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", responseError(resp)
	}
	var body struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := decodeUpdateJSON(resp.Body, "release revision", &body); err != nil {
		return "", fmt.Errorf("decode release revision: %w", err)
	}
	commit := strings.ToLower(strings.TrimSpace(body.Object.SHA))
	if !validCommitID(commit) {
		return "", errors.New("GitHub returned an invalid release revision")
	}
	return commit, nil
}

func validCommitID(value string) bool {
	if len(value) < 40 || len(value) > 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sameCommit(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	return a != "" && b != "" && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a))
}

func (rt *desktopRuntime) releaseAssets(ctx context.Context) (map[string]githubReleaseAsset, error) {
	resp, err := rt.githubGet(ctx, rt.httpClient, rt.updateURL+"/releases/tags/"+updateTag, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	var release githubRelease
	if err := decodeUpdateJSON(resp.Body, "release metadata", &release); err != nil {
		return nil, fmt.Errorf("decode release assets: %w", err)
	}
	if release.TagName != updateTag {
		return nil, fmt.Errorf("unexpected release tag %q", release.TagName)
	}
	assets := make(map[string]githubReleaseAsset, len(release.Assets))
	for _, asset := range release.Assets {
		if _, duplicate := assets[asset.Name]; duplicate {
			return nil, fmt.Errorf("release contains duplicate asset %q", asset.Name)
		}
		assets[asset.Name] = asset
	}
	for _, name := range []string{updateInstallerName, updateChecksumName, updateManifestName} {
		asset, ok := assets[name]
		if !ok || strings.TrimSpace(asset.URL) == "" {
			return nil, fmt.Errorf("release is missing %s", name)
		}
		if !sameHTTPOrigin(asset.URL, rt.updateURL) {
			return nil, fmt.Errorf("release asset %s points outside the GitHub API", name)
		}
	}
	return assets, nil
}

func (rt *desktopRuntime) readAsset(ctx context.Context, asset githubReleaseAsset, limit int64) ([]byte, error) {
	resp, err := rt.githubGet(ctx, rt.updateClient, asset.URL, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte safety limit", asset.Name, limit)
	}
	return data, nil
}

func parsePublishedChecksum(raw []byte) (string, error) {
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != updateInstallerName {
		return "", errors.New("installer checksum file has an unexpected format")
	}
	digest := strings.ToLower(fields[0])
	if len(digest) != sha256.Size*2 {
		return "", errors.New("installer checksum is not SHA-256")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", errors.New("installer checksum is not valid hexadecimal")
	}
	return digest, nil
}

func validateUpdateManifest(manifest updateManifest, releaseCommit, publishedHash string, installerAsset githubReleaseAsset) error {
	manifest.Commit = strings.ToLower(strings.TrimSpace(manifest.Commit))
	manifest.Installer.SHA256 = strings.ToLower(strings.TrimSpace(manifest.Installer.SHA256))
	if manifest.SchemaVersion != 1 || manifest.Tag != updateTag || strings.TrimSpace(manifest.Version) == "" || manifest.Installer.Name != updateInstallerName {
		return errors.New("release update manifest is incompatible")
	}
	if !validCommitID(manifest.Commit) || manifest.Commit != strings.ToLower(strings.TrimSpace(releaseCommit)) {
		return errors.New("release manifest does not match the rolling release revision")
	}
	if manifest.Installer.SHA256 != publishedHash {
		return errors.New("release manifest and published installer checksum disagree")
	}
	if manifest.Installer.Size <= 0 || manifest.Installer.Size > maxUpdateInstaller {
		return errors.New("release manifest contains an invalid installer size")
	}
	if installerAsset.Size > 0 && installerAsset.Size != manifest.Installer.Size {
		return errors.New("release metadata and manifest installer sizes disagree")
	}
	return nil
}

func (rt *desktopRuntime) downloadVerifiedUpdate(ctx context.Context) (string, string, error) {
	releaseCommit, err := rt.latestReleaseCommit(ctx)
	if err != nil {
		return "", "", err
	}
	if buildCommit == "" || buildCommit == "development" {
		return "", "", errors.New("development builds cannot replace themselves; install a packaged release first")
	}
	if sameCommit(buildCommit, releaseCommit) {
		return "", "", errors.New("NEVR is already on the latest packaged revision")
	}
	assets, err := rt.releaseAssets(ctx)
	if err != nil {
		return "", "", err
	}
	manifestRaw, err := rt.readAsset(ctx, assets[updateManifestName], maxUpdateMetadataSize)
	if err != nil {
		return "", "", fmt.Errorf("download update manifest: %w", err)
	}
	var manifest updateManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return "", "", fmt.Errorf("decode update manifest: %w", err)
	}
	checksumRaw, err := rt.readAsset(ctx, assets[updateChecksumName], maxUpdateMetadataSize)
	if err != nil {
		return "", "", fmt.Errorf("download published checksum: %w", err)
	}
	publishedHash, err := parsePublishedChecksum(checksumRaw)
	if err != nil {
		return "", "", err
	}
	if err := validateUpdateManifest(manifest, releaseCommit, publishedHash, assets[updateInstallerName]); err != nil {
		return "", "", err
	}

	if err := os.MkdirAll(rt.updateDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create update staging directory: %w", err)
	}
	tmp, err := os.CreateTemp(rt.updateDir, "installer-*.part")
	if err != nil {
		return "", "", fmt.Errorf("create staged installer: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	resp, err := rt.githubGet(ctx, rt.updateClient, assets[updateInstallerName].URL, "application/octet-stream")
	if err != nil {
		return "", "", fmt.Errorf("download installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", responseError(resp)
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(resp.Body, maxUpdateInstaller+1))
	if err != nil {
		return "", "", fmt.Errorf("download installer: %w", err)
	}
	if written > maxUpdateInstaller || written != manifest.Installer.Size {
		return "", "", fmt.Errorf("downloaded installer size is %d bytes; expected %d", written, manifest.Installer.Size)
	}
	actualHash := hex.EncodeToString(digest.Sum(nil))
	if actualHash != publishedHash {
		return "", "", errors.New("downloaded installer failed SHA-256 verification")
	}
	if err := tmp.Sync(); err != nil {
		return "", "", fmt.Errorf("sync staged installer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", "", fmt.Errorf("close staged installer: %w", err)
	}
	target := filepath.Join(rt.updateDir, "NEVR-Anticheat-Setup-"+publishedHash+".exe")
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("replace previously staged installer: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return "", "", fmt.Errorf("commit staged installer: %w", err)
	}
	// The rolling tag must not move while its assets are being downloaded. A
	// changed tag means a newer release won the race, so discard and retry.
	afterCommit, err := rt.latestReleaseCommit(ctx)
	if err != nil || afterCommit != releaseCommit {
		_ = os.Remove(target)
		if err != nil {
			return "", "", fmt.Errorf("recheck release revision: %w", err)
		}
		return "", "", errors.New("a newer Windows release appeared during download; click update again")
	}
	return target, releaseCommit, nil
}

func stagedInstallerSHA256(path string) (string, error) {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "NEVR-Anticheat-Setup-") || !strings.HasSuffix(name, ".exe") {
		return "", errors.New("staged installer name does not contain its SHA-256")
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, "NEVR-Anticheat-Setup-"), ".exe")
	if len(digest) != sha256.Size*2 {
		return "", errors.New("staged installer name does not contain its SHA-256")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", errors.New("staged installer name contains an invalid SHA-256")
	}
	return strings.ToLower(digest), nil
}

func (rt *desktopRuntime) lastUpdateError() string {
	f, err := os.Open(filepath.Join(rt.updateDir, "last-update-error.txt"))
	if err != nil {
		return ""
	}
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, 4096))
	return strings.TrimSpace(string(raw))
}

func cleanupOldUpdateArtifacts(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "installer-") && !strings.HasPrefix(name, "NEVR-Anticheat-Setup-") && !strings.HasPrefix(name, "nevr-update-helper-") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
