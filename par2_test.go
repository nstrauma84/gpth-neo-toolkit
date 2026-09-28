package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parityFixture(t *testing.T) (archiveSet, []byte) {
	t.Helper()
	if !existsCmd("par2") {
		t.Skip("par2 executable required for integration test")
	}
	oldDir, oldConfig, oldIn := archiveDir, emailConfig, in
	t.Cleanup(func() { archiveDir, emailConfig, in = oldDir, oldConfig, oldIn })
	archiveDir = t.TempDir()
	emailConfig = nil
	base := "사진 보관"
	data := make([]byte, 64*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	name := base + ".tar"
	if err := os.WriteFile(filepath.Join(archiveDir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	hash, err := shaFile(filepath.Join(archiveDir, name))
	if err != nil {
		t.Fatal(err)
	}
	set := archiveSet{base, base + ".sha256.txt", base + ".par2"}
	sha := filepath.Join(archiveDir, set.SHA)
	if err := os.WriteFile(sha, []byte(hash+"  "+name+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := createParity(base, sha, 5); err != nil {
		t.Fatal(err)
	}
	return set, data
}

func TestPAR2ActualCreateVerifyRepair(t *testing.T) {
	set, data := parityFixture(t)
	files, err := paritySources(filepath.Join(archiveDir, set.PAR))
	if err != nil || len(files) != 1 || files[0].Size != int64(len(data)) {
		t.Fatalf("metadata read: %v %+v", err, files)
	}
	if err := verifySet(set); err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte(nil), data...)
	damaged[100] ^= 255
	if err := os.WriteFile(filepath.Join(archiveDir, set.Base+".tar"), damaged, 0600); err != nil {
		t.Fatal(err)
	}
	in = bufio.NewReader(strings.NewReader("n\n"))
	if err := verifySet(set); err == nil {
		t.Fatal("damaged archive accepted without repair")
	}
	got, _ := os.ReadFile(filepath.Join(archiveDir, set.Base+".tar"))
	if !bytes.Equal(got, damaged) {
		t.Fatal("declined repair changed data")
	}
	in = bufio.NewReader(strings.NewReader("y\n"))
	if err := verifySet(set); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(archiveDir, set.Base+".tar"))
	if !bytes.Equal(got, data) {
		t.Fatal("repair did not restore original")
	}
	if err := os.Remove(filepath.Join(archiveDir, set.SHA)); err != nil {
		t.Fatal(err)
	}
	set.SHA = ""
	if err := verifySet(set); err != nil {
		t.Fatal("PAR2-only verification failed:", err)
	}
}

func TestPAR2MissingDataAndInsufficientParity(t *testing.T) {
	set, _ := parityFixture(t)
	if err := os.Remove(filepath.Join(archiveDir, set.Base+".tar")); err != nil {
		t.Fatal(err)
	}
	code, err := runParity("verify", filepath.Join(archiveDir, set.PAR))
	if err != nil || code != 2 {
		t.Fatalf("expected insufficient recovery: %d %v", code, err)
	}
	in = bufio.NewReader(strings.NewReader("y\n"))
	if err := verifySet(set); err == nil {
		t.Fatal("insufficient parity reported success")
	}
	line, _ := in.ReadString('\n')
	if line != "y\n" {
		t.Fatal("offered repair despite insufficient parity")
	}
}

func TestPAR2VolumeOnlyDiscoveryAndCorruption(t *testing.T) {
	set, _ := parityFixture(t)
	if err := os.Remove(filepath.Join(archiveDir, set.PAR)); err != nil {
		t.Fatal(err)
	}
	sets, err := verificationSets()
	if err != nil || len(sets) != 1 || !volumeSuffix.MatchString(sets[0].PAR) {
		t.Fatalf("volume fallback: %v %+v", err, sets)
	}
	if err := verifySet(sets[0]); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(archiveDir, sets[0].PAR)
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := paritySources(path); err == nil {
		t.Fatal("broken metadata accepted")
	}
}

func TestPAR2SpaceAndTransferNames(t *testing.T) {
	if parityEstimate(1000000, 0) != 0 || parityEstimate(1000000, 5) < 50000 {
		t.Fatal("incorrect parity estimate")
	}
	for _, name := range []string{"a.par2", "a.vol00+01.par2", "a.tar", "a.tar.part.aa", "a.sha256.txt"} {
		if !isArchiveFile(name) {
			t.Fatal("missing transfer file:", name)
		}
	}
	for code, want := range map[int]string{0: "정상", 1: "복구 가능", 2: "데이터 부족", 4: "검사 오류", 6: "검사 오류"} {
		if !strings.Contains(parityStatus(code), want) {
			t.Fatal("incorrect status:", code)
		}
	}
	old := archiveDir
	archiveDir = t.TempDir()
	defer func() { archiveDir = old }()
	if err := validateParityTargets([]protectedFile{{"unrelated.tar", 10}}, "chosen"); err == nil {
		t.Fatal("unrelated repair target accepted")
	}
	if err := archiveSpacePlan(1<<60, 100); err == nil {
		t.Fatal("insufficient space accepted")
	}
}

func TestPAR2SplitMissingPartRepair(t *testing.T) {
	if !existsCmd("par2") {
		t.Skip("par2 required")
	}
	oldDir, oldConfig, oldIn := archiveDir, emailConfig, in
	t.Cleanup(func() { archiveDir, emailConfig, in = oldDir, oldConfig, oldIn })
	archiveDir = t.TempDir()
	emailConfig = nil
	base := "분할 복구"
	pieces := map[string][]byte{base + ".tar.part.aa": make([]byte, 64*1024), base + ".tar.part.ab": make([]byte, 128)}
	var manifest strings.Builder
	for name, data := range pieces {
		rand.Read(data)
		if err := os.WriteFile(filepath.Join(archiveDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		hash, err := shaFile(filepath.Join(archiveDir, name))
		if err != nil {
			t.Fatal(err)
		}
		manifest.WriteString(hash + "  " + name + "\n")
	}
	set := archiveSet{base, base + ".sha256.txt", base + ".par2"}
	sha := filepath.Join(archiveDir, set.SHA)
	if err := os.WriteFile(sha, []byte(manifest.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := createParity(base, sha, 5); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(archiveDir, base+".tar.part.ab")); err != nil {
		t.Fatal(err)
	}
	in = bufio.NewReader(strings.NewReader("y\n"))
	if err := verifySet(set); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(archiveDir, base+".tar.part.ab"))
	if err != nil || !bytes.Equal(got, pieces[base+".tar.part.ab"]) {
		t.Fatal("missing part not restored")
	}
}

func TestPAR2SHADisagreementStillChecksPAR2(t *testing.T) {
	set, _ := parityFixture(t)
	// SHA failure must not prevent launching par2; make the executable leave a marker.
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	script := "#!/bin/sh\nprintf called > '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "par2"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(archiveDir, set.SHA), []byte("bad manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySet(set); err == nil {
		t.Fatal("SHA failure hidden by PAR2 success")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("PAR2 was not checked independently")
	}
}
