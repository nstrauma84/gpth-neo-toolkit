package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSMTPEncryptedStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "settings")
	c := smtpConfig{"smtp.example.com:465", "sender@example.com", "receiver@example.com", "test-secret-only"}
	if err := saveSMTP(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := loadSMTP(dir)
	if err != nil || *got != c {
		t.Fatalf("round trip failed: %v", err)
	}
	path := filepath.Join(dir, "smtp.enc")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{c.Server, c.User, c.To, c.Password} {
		if bytes.Contains(first, []byte(value)) {
			t.Fatal("plaintext leaked to encrypted config")
		}
	}
	for _, name := range []string{"smtp.enc", "smtp.key"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("incorrect permissions: %s", name)
		}
	}
	st, _ := os.Stat(dir)
	if st.Mode().Perm() != 0700 {
		t.Fatal("incorrect directory permissions")
	}
	if err := saveSMTP(dir, c); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if bytes.Equal(first, second) {
		t.Fatal("nonce reused")
	}
	second[len(second)-1] ^= 1
	if err := os.WriteFile(path, second, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSMTP(dir); err == nil {
		t.Fatal("tampering accepted")
	}
	if err := saveSMTP(dir, c); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "smtp.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSMTP(dir); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("config lost after missing key")
	}
}

func TestSMTPValidation(t *testing.T) {
	good := smtpConfig{"smtp.example.com:587", "sender@example.com", "receiver@example.com", "test-password"}
	cases := []smtpConfig{good, good, good, good, good}
	cases[0].Server = "smtp.example.com"
	cases[1].Server = "smtp.example.com:65536"
	cases[2].User = "bad\r\nBcc: evil@example.com"
	cases[3].To = "not-an-address"
	cases[4].Password = ""
	for _, c := range cases {
		if c.validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPMissingConfigCanBeSkipped(t *testing.T) {
	oldIn, oldConfig := in, emailConfig
	defer func() { in, emailConfig = oldIn, oldConfig }()
	emailConfig = nil
	in = bufio.NewReader(strings.NewReader("\n"))
	if err := configureSMTP(); err != nil {
		t.Fatal(err)
	}
	if emailConfig != nil {
		t.Fatal("skip enabled email")
	}
	sendEmail("test", "success", "must not send")
	in = bufio.NewReader(strings.NewReader("n\n"))
	if err := testSMTP(); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPFailedSaveKeepsConfig(t *testing.T) {
	dir := t.TempDir()
	c := smtpConfig{"smtp.example.com:465", "sender@example.com", "receiver@example.com", "test-password"}
	if err := saveSMTP(dir, c); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "smtp.enc"))
	c.Password = ""
	if err := saveSMTP(dir, c); err == nil {
		t.Fatal("invalid save succeeded")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "smtp.enc"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed config")
	}
}
