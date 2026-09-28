package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func remoteFixture(t *testing.T, response string) string {
	t.Helper()
	oldRemote, oldSettings, oldPath, oldIn := remote, activeSettings, settingsPath, in
	t.Cleanup(func() { remote, activeSettings, settingsPath, in = oldRemote, oldSettings, oldPath, oldIn })
	dir := t.TempDir()
	script := "#!/bin/sh\n" + response + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rclone"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	settingsPath = filepath.Join(dir, "settings.json")
	data := []byte(`{"work_root":"/srv/photos", "rclone_remote":"", "rclone_transfers":2}`)
	if err := os.WriteFile(settingsPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	activeSettings, err = decodeSettings(data)
	if err != nil {
		t.Fatal(err)
	}
	remote = ""
	return settingsPath
}

func TestRemoteFirstRunSelectionAndPersistence(t *testing.T) {
	path := remoteFixture(t, "printf 'drive-b:\\ndrive-a:\\ndrive-b:\\n'")
	in = bufio.NewReader(strings.NewReader("9\n2\n"))
	initRemote()
	if remote != "drive-b" || activeSettings.Remote != "drive-b" {
		t.Fatal("selected remote not applied")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := decodeSettings(data)
	if err != nil || c.Remote != "drive-b" || c.WorkRoot != "/srv/photos" || c.Transfers != 2 {
		t.Fatalf("save changed other settings: %v", err)
	}
	if bytes.Contains(data, []byte("takeout_dir")) {
		t.Fatal("derived paths materialized during remote save")
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("settings permissions")
	}
	// A stored default must bypass both rclone and user input on startup.
	in = bufio.NewReader(strings.NewReader("0\n"))
	initRemote()
	line, _ := in.ReadString('\n')
	if line != "0\n" {
		t.Fatal("startup prompted again with saved default")
	}
}

func TestRemoteChangeAndCancel(t *testing.T) {
	path := remoteFixture(t, "printf 'drive-a:\\ndrive-b:\\n'")
	if err := saveDefaultRemote("drive-a"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	in = bufio.NewReader(strings.NewReader("0\n"))
	if err := configureRemote(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if remote != "drive-a" || !bytes.Equal(before, after) {
		t.Fatal("cancel changed saved remote")
	}
	in = bufio.NewReader(strings.NewReader("2\n"))
	if err := configureRemote(); err != nil {
		t.Fatal(err)
	}
	if remote != "drive-b" {
		t.Fatal("menu did not change remote")
	}
}

func TestRemoteFailureAndEmptyList(t *testing.T) {
	remoteFixture(t, "exit 0")
	if _, err := registeredRemotes(); err == nil {
		t.Fatal("empty remotes accepted")
	}
	if err := ensureRemote(); err == nil {
		t.Fatal("unset default accepted")
	}
	settingsPath = filepath.Join(t.TempDir(), "missing", "settings.json")
	remote = "previous"
	if err := saveDefaultRemote("next"); err == nil || remote != "previous" {
		t.Fatal("failed save changed active remote")
	}
}

func TestRemoteMissingAndLegacySettings(t *testing.T) {
	for _, data := range []string{`{}`, `{"rclone_remote":""}`} {
		c, err := decodeSettings([]byte(data))
		if err != nil || c.Remote != "" {
			t.Fatalf("unset remote rejected: %v", err)
		}
	}
	c, err := decodeSettings([]byte(`{"rclone_remote":"gdrive"}`))
	if err != nil || c.Remote != "gdrive" {
		t.Fatal("legacy remote lost")
	}
}

func TestSMTPSettingsDisplayHidesPassword(t *testing.T) {
	old := emailConfig
	t.Cleanup(func() { emailConfig = old })
	emailConfig = &smtpConfig{Server: "smtp.example.com:465", User: "sender@example.com", To: "receiver@example.com", Password: "secret-never-display"}
	var b bytes.Buffer
	showSMTPSettings(&b)
	for _, value := range []string{emailConfig.Server, emailConfig.User, emailConfig.To} {
		if !strings.Contains(b.String(), value) {
			t.Fatal("missing current setting")
		}
	}
	if strings.Contains(b.String(), emailConfig.Password) {
		t.Fatal("password displayed")
	}
}

func TestSMTPMenuWithoutSettingsAndEOF(t *testing.T) {
	oldConfig, oldIn := emailConfig, in
	t.Cleanup(func() { emailConfig, in = oldConfig, oldIn })
	emailConfig = nil
	for _, input := range []string{"2\n0\n", "1\nn\n0\n", ""} {
		in = bufio.NewReader(strings.NewReader(input))
		if err := smtpMenu(); err != nil {
			t.Fatal(err)
		}
		if emailConfig != nil {
			t.Fatal("menu enabled email without configuration")
		}
	}
}
