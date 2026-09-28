package main

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseAssetsDecode(t *testing.T) {
	old := http.DefaultClient
	defer func() { http.DefaultClient = old }()
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.2.3","assets":[{"name":"gpth-linux-amd64.zip","browser_download_url":"https://example.com/gpth.zip"}]}`)), Header: make(http.Header)}, nil
	})}
	got, err := githubRelease("https://example.com/releases/latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Assets) != 1 || got.Assets[0].Name != "gpth-linux-amd64.zip" || got.Assets[0].BrowserDownloadURL != "https://example.com/gpth.zip" {
		t.Fatalf("asset decode failed: %+v", got)
	}
}

func TestArchiveVerificationWithSpaces(t *testing.T) {
	oldDir, oldIn := archiveDir, in
	defer func() { archiveDir, in = oldDir, oldIn }()
	archiveDir = t.TempDir()
	name := "사진 보관 2026.tar.part.aa"
	if err := os.WriteFile(filepath.Join(archiveDir, name), []byte("archive fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	hash, err := shaFile(filepath.Join(archiveDir, name))
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(archiveDir, "사진 보관.sha256.txt")
	if err := os.WriteFile(manifest, []byte(hash+"  "+name+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	in = bufio.NewReader(strings.NewReader("1\n"))
	if err := verifyArchive(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(archiveDir, name), []byte("tampered"), 0600)
	in = bufio.NewReader(strings.NewReader("1\n"))
	if err := verifyArchive(); err == nil {
		t.Fatal("mismatched checksum accepted")
	}
}

func TestChecksumFormats(t *testing.T) {
	h := strings.Repeat("a", 64)
	for _, bad := range []string{"", "garbage", h + "  ../outside.tar", h + "  /tmp/outside.tar", strings.Repeat("z", 64) + "  file.tar"} {
		if _, err := parseChecksums(bad); err == nil {
			t.Fatalf("invalid checksum accepted: %q", bad)
		}
	}
	got, err := parseChecksums(strings.ToUpper(h) + " *a b.tar\r\n")
	if err != nil || len(got) != 1 || got[0].Name != "a b.tar" || got[0].Hash != h {
		t.Fatalf("binary/CRLF format: %v", err)
	}
}

func TestCommandFailureIsNotEmptyResult(t *testing.T) {
	_, err := output("sh", "-c", "echo denied >&2; exit 4")
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("lost command error: %v", err)
	}
	if remoteExists("othergdrive:\n", "gdrive") || !remoteExists("gdrive:\n", "gdrive") {
		t.Fatal("remote name matching")
	}
	if _, err := filesMatching(filepath.Join(t.TempDir(), "missing"), func(string) bool { return true }); err == nil {
		t.Fatal("file listing error treated as empty directory")
	}
}

func TestSplitFailureDoesNotHang(t *testing.T) {
	bin := t.TempDir()
	for name, body := range map[string]string{
		"tar":   "#!/bin/sh\nexec /bin/dd if=/dev/zero bs=65536 count=32 2>/dev/null\n",
		"split": "#!/bin/sh\nexit 1\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	done := make(chan error, 1)
	go func() { done <- pipeTar(filepath.Join(t.TempDir(), "parts"), true) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("split failure ignored")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("split failure blocked TAR pipeline")
	}
}

func TestEnsureDirsFailure(t *testing.T) {
	old := takeoutDir
	defer func() { takeoutDir = old }()
	block := filepath.Join(t.TempDir(), "file")
	os.WriteFile(block, []byte("not a directory"), 0600)
	takeoutDir = filepath.Join(block, "child")
	if err := ensureDirs(); err == nil {
		t.Fatal("directory failure ignored")
	}
}

func TestFailureNotificationRouting(t *testing.T) {
	old := notifyTaskFailure
	defer func() { notifyTaskFailure = old }()
	count := 0
	notifyTaskFailure = func(task, status, details string) {
		count++
		if task == "" || status != "실패" || details != "test failure" {
			t.Fatal("incorrect failure notification")
		}
	}
	for _, choice := range []string{"0", "1", "2", "3", "4", "5", "6"} {
		reportTaskError(choice, errors.New("test failure"))
	}
	if count != 7 {
		t.Fatal("missing notification")
	}
	reportTaskError("1", errCancelled)
	reportTaskError("1", nil)
	reportTaskError("t", errors.New("SMTP failure"))
	if count != 7 {
		t.Fatal("cancel/test failure caused extra mail")
	}
}

func TestSettingsValidation(t *testing.T) {
	c, err := decodeSettings([]byte(`{"work_root":"/srv/photos", "rclone_transfers":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.TakeoutDir != "/srv/photos/takeout" || c.Remote != "gdrive" || c.Transfers != 2 {
		t.Fatal("defaults/derived paths")
	}
	cases := []string{
		`{"work_root":"relative"}`, `{"work_root":"/"}`, `{"archive_dir":"/etc"}`,
		`{"processed_dir":"/work/same", "normalized_input_dir":"/work/same/sub"}`,
		`{"rclone_transfers":0}`, `{"rclone_remote":"gdrive:"}`, `{"server_ip":"bad"}`, `{"rclone_cutoff":"bad"}`,
		`{"typo":1}`, `{} {}`,
	}
	for _, data := range cases {
		if _, err := decodeSettings([]byte(data)); err == nil {
			t.Fatalf("invalid config accepted: %s", data)
		}
	}
}

func TestSettingsMissingFileCreation(t *testing.T) {
	// Save globals because initialization applies the configuration.
	oldPath, oldActive := settingsPath, activeSettings
	oldRoot, oldTakeout, oldProcessed, oldArchive, oldNFC := workRoot, takeoutDir, processedDir, archiveDir, normalizedInputDir
	oldRemote, oldIP, oldTransfers, oldCheckers, oldStreams, oldCutoff := remote, containerIP, rcloneTransfers, rcloneCheckers, rcloneStreams, rcloneCutoff
	defer func() {
		settingsPath, activeSettings = oldPath, oldActive
		workRoot, takeoutDir, processedDir, archiveDir, normalizedInputDir = oldRoot, oldTakeout, oldProcessed, oldArchive, oldNFC
		remote, containerIP, rcloneTransfers, rcloneCheckers, rcloneStreams, rcloneCutoff = oldRemote, oldIP, oldTransfers, oldCheckers, oldStreams, oldCutoff
	}()
	path := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("GPTH_CONFIG", path)
	if err := initSettings(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("settings file permissions")
	}
	if workRoot != "/work" || rcloneTransfers != "4" {
		t.Fatal("default behavior changed")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
