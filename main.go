package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/text/unicode/norm"
)

var (
	remote             = ""
	containerIP        = "192.168.10.115"
	workRoot           = "/work"
	takeoutDir         = "/work/takeout"
	processedDir       = "/work/processed"
	archiveDir         = "/work/archive"
	normalizedInputDir = "/work/takeout-nfc"

	rcloneTransfers = "4"
	rcloneCheckers  = "8"
	rcloneStreams   = "4"
	rcloneCutoff    = "250M"
)

const (
	gpthRepo    = "Xentraxx/GooglePhotosTakeoutHelper_Neo"
	gpthBin     = "/usr/local/bin/gpth"
	tmuxSession = "gpth-session"
)

var in = bufio.NewReader(os.Stdin)

func main() {
	if err := tmuxGuard(); err != nil {
		fmt.Fprintln(os.Stderr, "tmux:", err)
		os.Exit(1)
	}
	if err := initSettings(); err != nil {
		fmt.Fprintln(os.Stderr, "설정:", err)
		os.Exit(1)
	}
	initRemote()
	initSMTP()
	if err := ensureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "작업 디렉터리:", err)
		sendEmail("작업 디렉터리 준비", "실패", err.Error())
		os.Exit(1)
	}
	mainMenu()
}

func tmuxGuard() error {
	if os.Getenv("TMUX") != "" || os.Getenv("GPTH_NO_TMUX") == "1" {
		return nil
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Println("tmux가 설치되어 있지 않습니다. 설치를 진행합니다...")
		if err := run("apt-get", "update"); err != nil {
			return err
		}
		if err := run("apt-get", "install", "-y", "tmux"); err != nil {
			return err
		}
	}
	if exec.Command("tmux", "has-session", "-t", tmuxSession).Run() == nil {
		fmt.Printf("기존에 실행 중인 tmux 세션(%s)으로 연결합니다...\n", tmuxSession)
		return syscall.Exec("/usr/bin/tmux", []string{"tmux", "attach-session", "-t", tmuxSession}, os.Environ())
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Printf("tmux 세션(%s)을 생성하고 프로그램을 보호 모드로 실행합니다...\n", tmuxSession)
	return syscall.Exec("/usr/bin/tmux", []string{"tmux", "new-session", "-s", tmuxSession, self}, os.Environ())
}

func mainMenu() {
	for {
		clear()
		fmt.Println("========================================")
		fmt.Println(" Google Photos Archive Toolkit (Go)")
		fmt.Println("========================================")
		fmt.Println()
		statusLine()
		fmt.Print("\n----------------------------------------\n\n")
		fmt.Println("0) GPTH Neo 버전 확인 / 업데이트")
		fmt.Println()
		fmt.Println("1) Google Takeout ZIP 다운로드")
		fmt.Println("2) GPTH Neo 실행")
		fmt.Println("3) GPTH 결과 TAR + SHA256 / PAR2 생성")
		fmt.Println("4) Google Drive로 Archive")
		fmt.Println("5) Archive Retrieve")
		fmt.Println("6) Archive SHA256 / PAR2 검사 및 복구")
		fmt.Println()
		fmt.Printf("s) %s 디스크 및 파일 현황\n", workRoot)
		fmt.Println("c) 서버 설정 확인 / 파일 위치")
		fmt.Println("r) 기본 rclone remote 변경")
		fmt.Println("m) Mac으로 Archive 복사 명령어 보기")
		fmt.Println("e) SMTP 이메일 알림 설정")
		fmt.Println("7) 저장공간 용량 정리")
		fmt.Println("8) 종료")
		fmt.Print("번호 선택: ")
		c := readLine()
		if c == "8" {
			fmt.Println("종료합니다.")
			return
		}

		valid := map[string]bool{"0": true, "1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true, "s": true, "m": true}
		if valid[strings.ToLower(c)] {
			if err := showStorageStatus(); err != nil {
				reportTaskError(c, err)
				pause()
				continue
			}
		}

		var err error
		switch strings.ToLower(c) {
		case "0":
			err = updateGPTH()
		case "1":
			err = downloadTakeout()
		case "2":
			err = runGPTH()
		case "3":
			err = createArchive()
		case "4":
			err = uploadArchive()
		case "5":
			err = retrieveArchive()
		case "6":
			err = verifyArchive()
		case "7":
			err = cleanupSpace()
		case "s":
			err = showWorkStatus()
		case "m":
			showMacRsync()
		case "c":
			showSettings()
		case "e":
			err = smtpMenu()
		case "r":
			err = configureRemote()
		default:
			fmt.Println("잘못된 번호입니다.")
		}
		if err != nil {
			reportTaskError(c, err)
		}
		pause()
	}
}

func readLine() string {
	s, _ := in.ReadString('\n')
	return strings.TrimSpace(s)
}
func pause() { fmt.Print("\nEnter를 누르세요..."); _, _ = in.ReadString('\n') }
func clear() { fmt.Print("\033[H\033[2J") }
func ensureDirs() error {
	for _, d := range []string{takeoutDir, processedDir, archiveDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("디렉터리 생성 %s: %w", d, err)
		}
	}
	return nil
}
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s 실행 실패: %w", name, err)
	}
	return nil
}
func output(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%s 조회 실패: %w: %s", name, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("%s 조회 실패: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}
func existsCmd(name string) bool { _, err := exec.LookPath(name); return err == nil }
func formatDuration(d time.Duration) string {
	s := int(d.Seconds())
	h, m := s/3600, (s%3600)/60
	if h > 0 {
		return fmt.Sprintf("%d시간 %02d분 %02d초", h, m, s%60)
	}
	if m > 0 {
		return fmt.Sprintf("%d분 %02d초", m, s%60)
	}
	return fmt.Sprintf("%d초", s)
}
func confirm(prompt string, defaultYes bool) bool {
	for {
		if defaultYes {
			fmt.Printf("%s [Y/n]: ", prompt)
		} else {
			fmt.Printf("%s [y/N]: ", prompt)
		}
		a := strings.ToLower(readLine())
		if a == "" {
			return defaultYes
		}
		if a == "y" || a == "yes" {
			return true
		}
		if a == "n" || a == "no" {
			return false
		}
		fmt.Println("y 또는 n을 입력하세요.")
	}
}
func humanBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := unit, 0
	for q := n / unit; q >= unit; q /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "⚠ 경로 조회 실패 (집계가 불완전할 수 있음): %v\n", err)
			}
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
func fileCount(root string) int {
	n := 0
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "⚠ 경로 조회 실패 (집계가 불완전할 수 있음): %v\n", err)
			}
			return nil
		}
		if info.Mode().IsRegular() {
			n++
		}
		return nil
	})
	return n
}
func freeBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 여유 공간 조회 %s: %v\n", path, err)
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
func filesMatching(dir string, pred func(string) bool) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("파일 목록 조회 %s: %w", dir, err)
	}
	var a []string
	for _, e := range ents {
		if !e.IsDir() && pred(e.Name()) {
			a = append(a, e.Name())
		}
	}
	sort.Strings(a)
	return a, nil
}
func zipFiles() ([]string, error) {
	return filesMatching(takeoutDir, func(s string) bool { return strings.HasSuffix(strings.ToLower(s), ".zip") })
}
func sumFiles(dir string, names []string) int64 {
	var n int64
	for _, f := range names {
		if st, err := os.Stat(filepath.Join(dir, f)); err == nil {
			n += st.Size()
		} else {
			fmt.Fprintf(os.Stderr, "⚠ 파일 크기 조회 %s: %v\n", f, err)
		}
	}
	return n
}
func showStorageStatus() error {
	if err := ensureDirs(); err != nil {
		return err
	}
	z, err := zipFiles()
	if err != nil {
		return err
	}
	fmt.Println("\n===== 현재 저장공간 / 작업 용량 =====")
	fmt.Printf("현재 여유 공간       : %s\n", humanBytes(freeBytes(workRoot)))
	fmt.Printf("%s 전체 사용량 : %s\n", workRoot, humanBytes(dirSize(workRoot)))
	fmt.Printf("Takeout              : %s (ZIP %d개 / %s)\n", humanBytes(dirSize(takeoutDir)), len(z), humanBytes(sumFiles(takeoutDir, z)))
	fmt.Printf("임시 NFC 입력        : %s\n", humanBytes(dirSize(normalizedInputDir)))
	fmt.Printf("GPTH 처리 결과       : %s\n", humanBytes(dirSize(processedDir)))
	fmt.Printf("Archive              : %s\n\n", humanBytes(dirSize(archiveDir)))
	return nil
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func githubRelease(url string) (release, error) {
	var r release
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return r, err
	}
	req.Header.Set("User-Agent", "gpth-toolkit-go")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return r, fmt.Errorf("GitHub HTTP %s", resp.Status)
	}
	err = json.NewDecoder(resp.Body).Decode(&r)
	return r, err
}
func latestTag() string {
	r, err := githubRelease("https://api.github.com/repos/" + gpthRepo + "/releases/latest")
	if err != nil {
		return ""
	}
	return r.TagName
}
func installedVersion() string {
	bin := gpthBin
	if _, err := os.Stat(bin); err != nil {
		if p, e := exec.LookPath("gpth"); e == nil {
			bin = p
		} else {
			return ""
		}
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`GooglePhotosTakeoutHelper v([0-9]+(?:\.[0-9]+)+)`)
	m := re.FindSubmatch(b)
	if len(m) > 1 {
		return "v" + string(m[1])
	}
	return ""
}
func find7zip() string {
	if p, e := exec.LookPath("7zz"); e == nil {
		return p
	}
	if p, e := exec.LookPath("7z"); e == nil {
		return p
	}
	return ""
}
func statusLine() {
	fmt.Printf("컨테이너 IP : %s\n", containerIP)
	fmt.Printf("기본 remote : %s\n", remoteLabel())
	i, l := installedVersion(), latestTag()
	if i == "" {
		fmt.Println("GPTH Neo    : ✘ NOT FOUND")
	} else if l == "" {
		fmt.Printf("GPTH Neo    : %s  ? latest 확인 실패\n", i)
	} else if i == l {
		fmt.Printf("GPTH Neo    : %s  ✔ Latest\n", i)
	} else {
		fmt.Printf("GPTH Neo    : %s  ⚠ Latest: %s\n", i, l)
	}
	for _, x := range []struct{ n, l string }{{"rclone", "rclone"}, {"rsync", "rsync"}, {"exiftool", "ExifTool"}, {"par2", "PAR2"}} {
		if existsCmd(x.n) {
			fmt.Printf("%-12s: installed\n", x.l)
		} else {
			fmt.Printf("%-12s: ✘ NOT FOUND\n", x.l)
		}
	}
	if find7zip() != "" {
		fmt.Println("7-Zip       : installed")
	} else {
		fmt.Println("7-Zip       : ✘ NOT FOUND")
	}
	if emailConfig != nil {
		fmt.Printf("SMTP 알림   : 활성화 (%s) ✔\n", emailConfig.To)
	} else {
		fmt.Println("SMTP 알림   : 미설정 / 건너뜀 (e 메뉴에서 설정)")
	}
}
func updateGPTH() error {
	fmt.Println("===== GPTH Neo 버전 확인 / 업데이트 =====")
	r, err := githubRelease("https://api.github.com/repos/" + gpthRepo + "/releases/latest")
	if err != nil {
		return err
	}
	fmt.Println("설치 버전 :", installedVersion())
	fmt.Println("최신 버전 :", r.TagName)
	if installedVersion() == r.TagName {
		fmt.Println("✔ 최신 버전입니다.")
		return nil
	}
	if !confirm("GPTH Neo "+r.TagName+"를 설치/업데이트할까요?", false) {
		return nil
	}
	var url string
	for _, a := range r.Assets {
		n := strings.ToLower(a.Name)
		if strings.Contains(n, "linux") && (strings.Contains(n, "x86_64") || strings.Contains(n, "amd64") || strings.Contains(n, "x64")) {
			url = a.BrowserDownloadURL
			break
		}
	}
	if url == "" {
		return errors.New("Linux x86_64 release 파일을 찾지 못했습니다")
	}
	tmp, err := os.MkdirTemp("", "gpth-update-*")
	if err != nil {
		return err
	}
	defer warnCleanup(tmp, true)
	dst := filepath.Join(tmp, "asset")
	if err := downloadURL(url, dst); err != nil {
		return err
	}
	candidate := dst
	if strings.HasSuffix(strings.ToLower(url), ".zip") {
		if !existsCmd("unzip") {
			return errors.New("unzip이 필요합니다")
		}
		unp := filepath.Join(tmp, "unpacked")
		if err := os.MkdirAll(unp, 0755); err != nil {
			return err
		}
		if err := run("unzip", "-q", dst, "-d", unp); err != nil {
			return err
		}
		err = filepath.Walk(unp, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if candidate == dst && !info.IsDir() && strings.HasPrefix(strings.ToLower(info.Name()), "gpth") {
				candidate = p
			}
			return nil
		})
		if err != nil {
			return err
		}
		if candidate == dst {
			return errors.New("압축 파일에서 GPTH 실행 파일을 찾지 못했습니다")
		}
	}
	b, err := os.ReadFile(candidate)
	if err != nil {
		return err
	}
	if err = os.WriteFile(gpthBin, b, 0755); err != nil {
		return err
	}
	fmt.Println("✔ 설치 완료:", gpthBin)
	return nil
}
func downloadURL(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download HTTP %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	closeErr := f.Close()
	return errors.Join(err, closeErr)
}

func ensureRemote() error {
	if remote == "" {
		return errors.New("기본 remote가 없습니다. r 메뉴에서 설정하세요")
	}
	if !existsCmd("rclone") {
		return errors.New("rclone 없음")
	}
	out, err := output("rclone", "listremotes")
	if err != nil {
		return err
	}
	if !remoteExists(out, remote) {
		return fmt.Errorf("remote %s: 없음", remote)
	}
	return nil
}
func selectMany(options []string, prompt string) []int {
	for {
		for i, s := range options {
			fmt.Printf("%d) %s\n", i+1, s)
		}
		fmt.Print(prompt)
		f := strings.Fields(readLine())
		var out []int
		ok := len(f) > 0
		for _, x := range f {
			n, e := strconv.Atoi(x)
			if e != nil || n < 1 || n > len(options) {
				ok = false
				break
			}
			out = append(out, n-1)
		}
		if ok {
			return out
		}
		fmt.Println("잘못된 입력입니다. 예: 1 2")
	}
}
func selectOne(options []string, prompt string) int { return selectMany(options, prompt)[0] }

func downloadTakeout() error {
	fmt.Println("===== Google Takeout ZIP 다운로드 =====")
	if err := ensureRemote(); err != nil {
		return err
	}
	if err := ensureDirs(); err != nil {
		return err
	}
	out, err := output("rclone", "lsf", remote+":Takeout", "--files-only")
	if err != nil {
		return err
	}
	var files []string
	for _, s := range strings.Split(out, "\n") {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(s)), ".zip") {
			files = append(files, strings.TrimSpace(s))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return errors.New("Takeout 폴더에 ZIP 파일이 없습니다")
	}
	menu := append(append([]string{}, files...), "전체 다운로드")
	picks := selectMany(menu, "다운로드 파일 선택 (공백 구분): ")
	var sel []string
	for _, i := range picks {
		if i == len(files) {
			sel = append([]string{}, files...)
			break
		}
		sel = append(sel, files[i])
	}
	existing, err := zipFiles()
	if err != nil {
		return err
	}
	if len(existing) > 0 && !confirm("기존 파일을 유지한 채 다운로드를 계속할까요?", false) {
		return nil
	}
	filterPath, err := selectedFilter(sel)
	if err != nil {
		return err
	}
	defer warnCleanup(filterPath, false)
	start := time.Now()
	err = run("rclone", "copy", remote+":Takeout", takeoutDir, "--filter-from", filterPath, "--progress", "--transfers="+rcloneTransfers, "--checkers="+rcloneCheckers, "--multi-thread-streams="+rcloneStreams, "--multi-thread-cutoff="+rcloneCutoff)
	if err != nil {
		return err
	}
	fmt.Println("✔ 다운로드 완료:", takeoutDir)
	sendEmail("Google Takeout 다운로드", "성공", fmt.Sprintf("- 저장 경로: %s\n- 소요 시간: %s\n- 다운로드 파일 수: %d개", takeoutDir, formatDuration(time.Since(start)), len(sel)))
	return nil
}

func normalizeNFC(root string) (int, int, error) {
	type renamePair struct{ src, dst string }
	var changes []renamePair

	// WalkDir is lexical/top-down, so collect then sort deepest paths first.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		name := d.Name()
		newName := norm.NFC.String(name)
		if newName == name {
			return nil
		}
		dst := filepath.Join(filepath.Dir(path), newName)
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("NFC 이름 충돌: %s => %s", path, dst)
		} else if !os.IsNotExist(err) {
			return err
		}
		changes = append(changes, renamePair{path, dst})
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	sort.Slice(changes, func(i, j int) bool {
		return strings.Count(changes[i].src, string(os.PathSeparator)) >
			strings.Count(changes[j].src, string(os.PathSeparator))
	})
	for _, p := range changes {
		if err := os.Rename(p.src, p.dst); err != nil {
			return 0, 0, fmt.Errorf("NFC rename 실패: %s => %s: %w", p.src, p.dst, err)
		}
	}

	remain := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && d.Name() != norm.NFC.String(d.Name()) {
			remain++
		}
		return nil
	})
	return len(changes), remain, err
}

func runGPTH() error {
	fmt.Println("===== GPTH Neo 실행 =====")
	if err := ensureDirs(); err != nil {
		return err
	}
	bin := gpthBin
	if _, e := os.Stat(bin); e != nil {
		p, e := exec.LookPath("gpth")
		if e != nil {
			return errors.New("GPTH Neo가 설치되어 있지 않습니다. 먼저 0번을 실행하세요")
		}
		bin = p
	}
	if !existsCmd("exiftool") {
		return errors.New("ExifTool이 없습니다")
	}
	seven := find7zip()
	if seven == "" {
		return errors.New("7-Zip이 없습니다")
	}
	zips, zipErr := zipFiles()
	if zipErr != nil {
		return zipErr
	}
	if len(zips) == 0 {
		return errors.New("Takeout ZIP 파일이 없습니다")
	}
	reuse := false
	if dirSize(normalizedInputDir) > 0 {
		fmt.Println("⚠ 기존 작업 디렉터리가 있습니다:", normalizedInputDir)
		fmt.Println("1) 기존 디렉터리 재사용 [기본값]\n2) 삭제 후 ZIP에서 다시 만들기\n3) 취소")
		fmt.Print("선택 [1]: ")
		c := readLine()
		if c == "" {
			c = "1"
		}
		if c == "1" {
			reuse = true
		} else if c == "2" {
			if err := os.RemoveAll(normalizedInputDir); err != nil {
				return fmt.Errorf("임시 입력 삭제: %w", err)
			}
		} else {
			return nil
		}
	}
	if !reuse {
		if err := os.MkdirAll(normalizedInputDir, 0755); err != nil {
			return err
		}
		fmt.Println("\n===== Takeout 압축해제 준비 =====")
		fmt.Printf("ZIP 파일       : %d개\nZIP 총 용량    : %s\n압축해제 방식  : 7-Zip (%s)\n임시 입력 경로 : %s\n여유 공간      : %s\n", len(zips), humanBytes(sumFiles(takeoutDir, zips)), filepath.Base(seven), normalizedInputDir, humanBytes(freeBytes(workRoot)))
		fmt.Println("\n압축해제 후: NFC 정규화 → NFC 검증 → GPTH 실행\n1) 압축해제 시작 [기본값]\n2) 취소")
		fmt.Print("선택 [1]: ")
		c := readLine()
		if c == "2" {
			return nil
		}
		start := time.Now()
		for _, z := range zips {
			fmt.Println("▶", z)
			if err := run(seven, "x", "-y", "-aoa", "-o"+normalizedInputDir, filepath.Join(takeoutDir, z)); err != nil {
				return fmt.Errorf("압축 해제 실패: %s: %w", z, err)
			}
		}
		sendEmail("Takeout ZIP 압축해제", "성공", fmt.Sprintf("- ZIP 파일 수: %d개\n- 원본 ZIP 총 용량: %s\n- 압축해제 후 용량: %s\n- 압축해제 경로: %s\n- 소요 시간: %s", len(zips), humanBytes(sumFiles(takeoutDir, zips)), humanBytes(dirSize(normalizedInputDir)), normalizedInputDir, formatDuration(time.Since(start))))
	}
	fmt.Println("\n===== Unicode 파일명 NFC 검사 / 정규화 =====")
	changed, remain, err := normalizeNFC(normalizedInputDir)
	if err != nil {
		return err
	}
	fmt.Printf("NFC 변환 완료: %d개\n", changed)
	fmt.Printf("NFC 검증: 비-NFC 이름 %d개\n", remain)
	if remain != 0 {
		return errors.New("비-NFC 이름이 남아 있습니다")
	}
	runOutDir := filepath.Join(processedDir, time.Now().Format("20060102_150405"))
	if err := os.MkdirAll(runOutDir, 0755); err != nil {
		return fmt.Errorf("GPTH 결과 폴더 생성 실패: %w", err)
	}

	albums, divide := "json", "1"
	writeExif, keepDup, keepInput, resume := true, false, false, true
	fmt.Println("\n1) 평소 설정으로 실행 [기본값]\n2) 옵션 직접 선택\n3) 취소")
	fmt.Print("선택 [1]: ")
	mode := readLine()
	if mode == "" {
		mode = "1"
	}
	if mode == "3" {
		return nil
	}
	if mode == "2" {
		albums = []string{"json", "shortcut", "reverse-shortcut", "duplicate-copy", "nothing", "ignore"}[selectOne([]string{"JSON [평소 설정]", "Shortcut", "Reverse shortcut", "Duplicate copy", "Nothing", "Ignore"}, "앨범 처리 방식 선택: ")]
		divide = []string{"1", "2", "3", "0"}[selectOne([]string{"연도별 [평소 설정]", "연도/월", "연도/월/일", "분류 안 함"}, "날짜별 폴더 구조 선택: ")]
		writeExif = selectOne([]string{"ON [평소 설정]", "OFF"}, "EXIF 기록 선택: ") == 0
		keepDup = selectOne([]string{"OFF [평소 설정]", "ON"}, "중복 파일 보존 선택: ") == 1
		keepInput = selectOne([]string{"OFF [평소 설정]", "ON"}, "Input 원본 보존 선택: ") == 1
		resume = selectOne([]string{"ON [평소 설정]", "OFF"}, "Resume 선택: ") == 0
	}
	dateDesc := map[string]string{"0": "분류 안 함", "1": "연도별", "2": "연도/월", "3": "연도/월/일"}[divide]
	fmt.Println("\n===== GPTH Neo 실행 설정 =====")
	fmt.Printf("Input            : %s\nOutput           : %s\nAlbums           : %s\nDate folders     : %s\nWrite EXIF       : %t\nKeep duplicates  : %t\nKeep input       : %t\nResume           : %t\nLocal timezone   : 미사용\n", normalizedInputDir, runOutDir, albums, dateDesc, writeExif, keepDup, keepInput, resume)
	if !confirm("이 설정으로 실행할까요?", true) {
		return nil
	}
	args := []string{"--input", normalizedInputDir, "--output", runOutDir, "--albums", albums, "--divide-to-dates", divide}
	if writeExif {
		args = append(args, "--write-exif")
	} else {
		args = append(args, "--no-write-exif")
	}
	if keepDup {
		args = append(args, "--keep-duplicates")
	} else {
		args = append(args, "--no-keep-duplicates")
	}
	if keepInput {
		args = append(args, "--keep-input")
	} else {
		args = append(args, "--no-keep-input")
	}
	if resume {
		args = append(args, "--resume")
	} else {
		args = append(args, "--no-resume")
	}
	start := time.Now()
	fmt.Print("\nGPTH Neo 처리를 시작합니다...\n\n")
	if err := run(bin, args...); err != nil {
		return err
	}
	fmt.Println("\n✔ GPTH Neo 작업 완료\n소요 시간:", formatDuration(time.Since(start)))
	fmt.Println("임시 NFC 입력 디렉터리 정리:", normalizedInputDir)
	if err := os.RemoveAll(normalizedInputDir); err != nil {
		return fmt.Errorf("GPTH 처리 완료 후 임시 입력 삭제 실패: %w", err)
	}
	sendEmail("GPTH Neo 사진 정리", "성공", fmt.Sprintf("- 출력 경로: %s\n- Albums: %s\n- Date folders: %s\n- Write EXIF: %t\n- Keep duplicates: %t\n- Keep input: %t\n- Resume: %t\n- 소요 시간: %s", runOutDir, albums, dateDesc, writeExif, keepDup, keepInput, resume, formatDuration(time.Since(start))))
	return nil
}

func createArchive() error {
	fmt.Println("===== GPTH 결과 TAR + SHA256 생성 =====")
	if err := ensureDirs(); err != nil {
		return err
	}

	dirs, err := os.ReadDir(processedDir)
	if err != nil {
		return err
	}

	var processedFolders []string
	hasFiles := false
	for _, d := range dirs {
		if d.IsDir() {
			processedFolders = append(processedFolders, d.Name())
		} else {
			hasFiles = true
		}
	}

	if len(processedFolders) == 0 && !hasFiles {
		return errors.New("처리 결과가 없습니다")
	}

	var targetDir string
	if len(processedFolders) > 0 {
		var menu []string
		for _, f := range processedFolders {
			menu = append(menu, f+fmt.Sprintf(" (%s)", humanBytes(dirSize(filepath.Join(processedDir, f)))))
		}
		if hasFiles {
			menu = append(menu, "processed 루트 (이전 방식 결과물)")
		}
		selIdx := selectOne(menu, "Archive할 결과 폴더를 선택하세요: ")
		if selIdx < len(processedFolders) {
			targetDir = filepath.Join(processedDir, processedFolders[selIdx])
		} else {
			targetDir = processedDir
		}
	} else {
		targetDir = processedDir
	}

	zips, zipErr := zipFiles()
	if zipErr != nil {
		return zipErr
	}
	proc := dirSize(targetDir)
	free := freeBytes(archiveDir)
	if len(zips) > 0 {
		zb := sumFiles(takeoutDir, zips)
		fmt.Println("\n===== Archive 생성 전 디스크 확인 =====")
		fmt.Printf("현재 여유 공간       : %s\nGPTH 처리 결과 용량  : %s\n예상 Archive 용량    : 약 %s\n\n원본 Takeout ZIP     : %d개 / %s\nZIP 유지 시 예상 여유: %s\nZIP 삭제 시 예상 여유: %s\n", humanBytes(free), humanBytes(proc), humanBytes(proc), len(zips), humanBytes(zb), humanBytes(max64(0, free-proc)), humanBytes(max64(0, free+zb-proc)))
		fmt.Println("※ 예상 Archive 용량은 TAR(무압축) 기준이며 실제 크기는 약간 달라질 수 있습니다.")
		if confirm("Archive 생성 전에 기존 Takeout ZIP 파일을 삭제할까요?", false) {
			for _, z := range zips {
				if err := os.Remove(filepath.Join(takeoutDir, z)); err != nil {
					return fmt.Errorf("ZIP 삭제 %s: %w", z, err)
				}
			}
			fmt.Println("✔ 기존 Takeout ZIP 파일을 삭제했습니다.")
		} else {
			fmt.Println("✔ 기존 Takeout ZIP 파일을 유지합니다.")
		}
	}
	def := time.Now().Format("20060102")
	fmt.Printf("Archive 이름 [기본값: %s]: ", def)
	name := readLine()
	if name == "" {
		name = def
	}
	name = strings.TrimSuffix(name, ".tar")
	if !safeArchiveName(name) {
		return errors.New("Archive 이름에는 경로 구분자, 제어문자, 와일드카드를 사용할 수 없습니다")
	}
	mode := selectOne([]string{"단일 TAR", "50GB 분할 TAR"}, "방식 선택: ")
	parityPercent, err := chooseParity()
	if err != nil {
		return err
	}
	if err := archiveSpacePlan(proc, parityPercent); err != nil {
		return err
	}
	if !confirm("위 예상 용량으로 Archive 생성을 진행할까요?", true) {
		return nil
	}
	start := time.Now()
	tarName := name + ".tar"
	shaPath := filepath.Join(archiveDir, name+".sha256.txt")
	if mode == 0 {
		dst := filepath.Join(archiveDir, tarName)
		if _, e := os.Stat(dst); e == nil && !confirm(dst+" 가 존재합니다. 덮어쓸까요?", false) {
			return nil
		}
		if err := removeOldParity(name); err != nil {
			if errors.Is(err, errCancelled) {
				return nil
			}
			return err
		}
		fmt.Println("[1/2] TAR 생성...")
		if err := pipeTar(dst, false, targetDir); err != nil {
			return err
		}
		fmt.Println("[2/2] SHA256 생성...")
		h, err := shaFileWithProgress(dst)
		if err != nil {
			return err
		}
		if err = os.WriteFile(shaPath, []byte(h+"  "+tarName+"\n"), 0644); err != nil {
			return err
		}
	} else {
		prefix := filepath.Join(archiveDir, tarName+".part.")
		old, err := filepath.Glob(prefix + "*")
		if err != nil {
			return err
		}
		if len(old) > 0 {
			if !confirm("기존 분할 파일을 삭제하고 다시 만들까요?", false) {
				return nil
			}
		}
		if err := removeOldParity(name); err != nil {
			if errors.Is(err, errCancelled) {
				return nil
			}
			return err
		}
		if len(old) > 0 {
			for _, p := range old {
				if err := os.Remove(p); err != nil {
					return fmt.Errorf("분할 파일 삭제 %s: %w", p, err)
				}
			}
		}
		fmt.Println("[1/2] 50GB 분할 TAR 생성...")
		if err := pipeTar(prefix, true, targetDir); err != nil {
			return err
		}
		fmt.Println("[2/2] SHA256 생성...")
		parts, err := filepath.Glob(prefix + "*")
		if err != nil {
			return err
		}
		if len(parts) == 0 {
			return errors.New("분할 TAR 파일이 생성되지 않았습니다")
		}
		sort.Strings(parts)
		var b strings.Builder
		for _, p := range parts {
			h, e := shaFileWithProgress(p)
			if e != nil {
				return e
			}
			fmt.Fprintf(&b, "%s  %s\n", h, filepath.Base(p))
		}
		if err := os.WriteFile(shaPath, []byte(b.String()), 0644); err != nil {
			return err
		}
	}
	fmt.Println("✔ Archive 생성 완료:", archiveDir)
	fmt.Println("✔ SHA256:", shaPath)
	if parityPercent > 0 {
		if err := createParity(name, shaPath, parityPercent); err != nil {
			return err
		}
	}
	sendEmail("TAR + SHA256 생성", "성공", fmt.Sprintf("- 아카이브 파일명: %s\n- 소요 시간: %s\n- 보관 경로: %s", tarName, formatDuration(time.Since(start)), archiveDir))
	return nil
}
func pipeTar(dest string, split bool, targetDir string) error {
	tar := exec.Command("tar", "-cf", "-", "-C", filepath.Dir(targetDir), filepath.Base(targetDir))
	tar.Stderr = os.Stderr
	if !split {
		f, err := os.Create(dest)
		if err != nil {
			return err
		}
		tar.Stdout = f
		return errors.Join(tar.Run(), f.Close())
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	sp := exec.Command("split", "-b", "50G", "-", dest)
	tar.Stdout = w
	sp.Stdin = r
	sp.Stdout, sp.Stderr = os.Stdout, os.Stderr
	if err := sp.Start(); err != nil {
		return err
	}
	if err := tar.Start(); err != nil {
		r.Close()
		w.Close()
		return errors.Join(err, sp.Wait())
	}
	// Children own the pipe ends now; closing both parent copies lets failures propagate.
	r.Close()
	w.Close()
	return errors.Join(tar.Wait(), sp.Wait())
}
func shaFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	if e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func shaFileWithProgress(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	total := info.Size()
	h := sha256.New()
	buf := make([]byte, 8*1024*1024)

	var done int64
	start := time.Now()
	last := time.Time{}

	fmt.Printf("\n대상: %s\n", filepath.Base(path))
	fmt.Printf("크기: %s\n", humanBytes(total))
	fmt.Println("SHA256 검증 중...")

	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, err := h.Write(buf[:n]); err != nil {
				return "", err
			}
			done += int64(n)

			now := time.Now()
			if last.IsZero() || now.Sub(last) >= time.Second || done == total {
				elapsed := now.Sub(start)
				speed := float64(done) / elapsed.Seconds()
				pct := 100.0
				if total > 0 {
					pct = float64(done) * 100 / float64(total)
				}

				eta := "--"
				if speed > 0 && done < total {
					eta = formatDuration(time.Duration(float64(total-done)/speed) * time.Second)
				}

				fmt.Printf("\r진행: %6.2f%% | %s / %s | %s/s | 경과 %s | 남은 시간 %s",
					pct, humanBytes(done), humanBytes(total), humanBytes(int64(speed)),
					formatDuration(elapsed), eta)
				last = now
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			fmt.Println()
			return "", rerr
		}
	}
	fmt.Println()
	return hex.EncodeToString(h.Sum(nil)), nil
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func navigateRemote() (string, error) {
	cur := ""
	var stack []string
	for {
		fmt.Println("\n===== Google Drive 폴더 탐색 =====")
		fmt.Println("현재 위치:", remote+":/"+cur)
		out, err := output("rclone", "lsf", remote+":"+cur, "--dirs-only")
		if err != nil {
			return "", err
		}
		var dirs []string
		for _, s := range strings.Split(out, "\n") {
			s = strings.TrimSuffix(strings.TrimSpace(s), "/")
			if s != "" {
				dirs = append(dirs, s)
			}
		}
		sort.Strings(dirs)
		for i, d := range dirs {
			fmt.Printf("%d) %s/\n", i+1, d)
		}
		n := len(dirs)
		fmt.Printf("%d) 여기 선택\n%d) 새 폴더 만들기\n", n+1, n+2)
		if len(stack) > 0 {
			fmt.Printf("%d) 한 단계 위로\n", n+3)
			fmt.Printf("%d) 취소\n", n+4)
		} else {
			fmt.Printf("%d) 취소\n", n+3)
		}
		fmt.Print("번호 선택: ")
		x, _ := strconv.Atoi(readLine())
		if x >= 1 && x <= n {
			stack = append(stack, cur)
			if cur == "" {
				cur = dirs[x-1]
			} else {
				cur += "/" + dirs[x-1]
			}
			continue
		}
		if x == n+1 {
			return cur, nil
		}
		if x == n+2 {
			fmt.Print("새 폴더명: ")
			d := readLine()
			if d != "" {
				p := d
				if cur != "" {
					p = cur + "/" + d
				}
				if err := run("rclone", "mkdir", remote+":"+p); err != nil {
					return "", err
				}
				stack = append(stack, cur)
				cur = p
			}
			continue
		}
		if len(stack) > 0 && x == n+3 {
			cur = stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			continue
		}
		return "", errCancelled
	}
}
func archiveFiles() ([]string, error) {
	return filesMatching(archiveDir, func(s string) bool {
		return isArchiveFile(s)
	})
}
func rcloneSelected(src, dst string, selected []string) error {
	filterPath, err := selectedFilter(selected)
	if err != nil {
		return err
	}
	defer warnCleanup(filterPath, false)
	return run("rclone", "copy", src, dst, "--filter-from", filterPath, "--progress", "--transfers="+rcloneTransfers, "--checkers="+rcloneCheckers, "--multi-thread-streams="+rcloneStreams, "--multi-thread-cutoff="+rcloneCutoff)
}
func chooseFiles(files []string, prompt string) []string {
	menu := append(append([]string{}, files...), "전체 선택")
	p := selectMany(menu, prompt)
	var s []string
	for _, i := range p {
		if i == len(files) {
			return append([]string{}, files...)
		}
		s = append(s, files[i])
	}
	return s
}
func uploadArchive() error {
	fmt.Println("===== Google Drive로 Archive =====")
	if e := ensureRemote(); e != nil {
		return e
	}
	files, listErr := archiveFiles()
	if listErr != nil {
		return listErr
	}
	if len(files) == 0 {
		return errors.New("Archive 파일이 없습니다")
	}
	sel := chooseFiles(files, "업로드 파일 선택 (공백 구분): ")
	dest, e := navigateRemote()
	if errors.Is(e, errCancelled) {
		return nil
	}
	if e != nil {
		return e
	}
	start := time.Now()
	if e = rcloneSelected(archiveDir, remote+":"+dest, sel); e != nil {
		return e
	}
	fmt.Println("✔ 업로드 완료")
	sendEmail("Google Drive 아카이브 업로드", "성공", fmt.Sprintf("- 업로드 대상: %s:%s\n- 파일 목록: %s\n- 소요 시간: %s", remote, dest, strings.Join(sel, " "), formatDuration(time.Since(start))))
	return nil
}
func retrieveArchive() error {
	fmt.Println("===== Archive Retrieve =====")
	if e := ensureRemote(); e != nil {
		return e
	}
	p, e := navigateRemote()
	if errors.Is(e, errCancelled) {
		return nil
	}
	if e != nil {
		return e
	}
	rp := remote + ":" + p
	out, e := output("rclone", "lsf", rp, "--files-only")
	if e != nil {
		return e
	}
	var files []string
	for _, s := range strings.Split(out, "\n") {
		if isArchiveFile(s) {
			files = append(files, s)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return errors.New("Archive 파일이 없습니다")
	}
	sel := chooseFiles(files, "가져올 파일 선택 (공백 구분): ")
	start := time.Now()
	if e = rcloneSelected(rp, archiveDir, sel); e != nil {
		return e
	}
	fmt.Println("✔ 가져오기 완료:", archiveDir)
	sendEmail("Archive Retrieve 다운로드", "성공", fmt.Sprintf("- 원본 위치: %s\n- 가져온 파일: %s\n- 소요 시간: %s", rp, strings.Join(sel, " "), formatDuration(time.Since(start))))
	return nil
}
func showMacRsync() {
	clear()
	fmt.Print("==========================================================\n  Mac으로 Archive 파일 전송 (rsync 명령어)\n==========================================================\n\n")
	fmt.Printf("rsync -avP %s .\n\n전송 완료 후:\nshasum -a 256 -c *.sha256.txt\n", shellQuote("root@"+containerIP+":"+shellQuote(archiveDir+"/")))
}
func showWorkStatus() error {
	if err := ensureDirs(); err != nil {
		return err
	}
	clear()
	fmt.Printf("==========================================================\n  %s 스토리지 및 전체 디렉터리 현황\n==========================================================\n", workRoot)
	fmt.Printf("\n여유 공간: %s\nprocessed: %s (파일 %d개)\ntakeout: %s\narchive: %s\n임시 NFC: %s\n", humanBytes(freeBytes(workRoot)), humanBytes(dirSize(processedDir)), fileCount(processedDir), humanBytes(dirSize(takeoutDir)), humanBytes(dirSize(archiveDir)), humanBytes(dirSize(normalizedInputDir)))
	fmt.Println("\n▶ Takeout ZIP")
	zips, err := zipFiles()
	if err != nil {
		return err
	}
	for _, f := range zips {
		st, err := os.Stat(filepath.Join(takeoutDir, f))
		if err != nil {
			return err
		}
		fmt.Printf("  %-10s %s\n", humanBytes(st.Size()), f)
	}
	fmt.Println("▶ Archive")
	archives, err := archiveFiles()
	if err != nil {
		return err
	}
	for _, f := range archives {
		st, err := os.Stat(filepath.Join(archiveDir, f))
		if err != nil {
			return err
		}
		fmt.Printf("  %-10s %s\n", humanBytes(st.Size()), f)
	}
	return nil
}

func cleanupSpace() error {
	for {
		clear()
		fmt.Println("===== 저장공간 용량 정리 =====")
		showStorageStatus()
		fmt.Println("\n1) Takeout ZIP 그룹 삭제")
		fmt.Println("2) Processed 결과 폴더 삭제")
		fmt.Println("3) Archive 그룹 삭제")
		fmt.Println("0) 돌아가기")
		fmt.Print("번호 선택: ")
		c := readLine()

		var err error
		switch c {
		case "0", "":
			return nil
		case "1":
			err = cleanupTakeout()
		case "2":
			err = cleanupProcessed()
		case "3":
			err = cleanupArchive()
		default:
			fmt.Println("잘못된 번호입니다.")
			pause()
			continue
		}

		if err != nil {
			fmt.Fprintln(os.Stderr, "⚠ 정리 중 오류 발생:", err)
		}
		pause()
	}
}

func cleanupTakeout() error {
	zips, err := zipFiles()
	if err != nil {
		return err
	}
	if len(zips) == 0 {
		fmt.Println("삭제할 ZIP 파일이 없습니다.")
		return nil
	}

	groups := make(map[string][]string)
	re := regexp.MustCompile(`(?i)-[0-9]+\.zip$`)
	for _, z := range zips {
		prefix := z
		if re.MatchString(z) {
			prefix = re.ReplaceAllString(z, "")
		}
		groups[prefix] = append(groups[prefix], z)
	}

	var prefixes []string
	for p := range groups {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	var menu []string
	for _, p := range prefixes {
		files := groups[p]
		size := sumFiles(takeoutDir, files)
		menu = append(menu, fmt.Sprintf("%s (ZIP %d개, %s)", p, len(files), humanBytes(size)))
	}

	fmt.Println("\n===== Takeout ZIP 그룹 삭제 =====")
	selIdxs := selectMany(append(menu, "취소"), "삭제할 그룹 선택 (공백 구분, 취소는 마지막 번호): ")

	var toDelete []string
	for _, i := range selIdxs {
		if i == len(menu) {
			return nil
		}
		toDelete = append(toDelete, groups[prefixes[i]]...)
	}

	if len(toDelete) == 0 {
		return nil
	}

	fmt.Printf("\n총 %d개의 ZIP 파일을 삭제합니다. (예상 확보 공간: %s)\n", len(toDelete), humanBytes(sumFiles(takeoutDir, toDelete)))
	if !confirm("정말 삭제하시겠습니까?", false) {
		return nil
	}

	for _, f := range toDelete {
		if err := os.Remove(filepath.Join(takeoutDir, f)); err != nil {
			fmt.Fprintln(os.Stderr, "⚠ 삭제 실패:", f, err)
		} else {
			fmt.Println("✔ 삭제 완료:", f)
		}
	}
	return nil
}

func cleanupProcessed() error {
	dirs, err := os.ReadDir(processedDir)
	if err != nil {
		return err
	}

	var folders []string
	hasRootFiles := false
	for _, d := range dirs {
		if d.IsDir() {
			folders = append(folders, d.Name())
		} else {
			hasRootFiles = true
		}
	}

	if len(folders) == 0 && !hasRootFiles {
		fmt.Println("삭제할 Processed 결과가 없습니다.")
		return nil
	}

	var menu []string
	var targets []string
	for _, f := range folders {
		targets = append(targets, f)
		size := dirSize(filepath.Join(processedDir, f))
		menu = append(menu, fmt.Sprintf("%s (%s)", f, humanBytes(size)))
	}
	if hasRootFiles {
		targets = append(targets, ".")
		size := dirSize(processedDir) // Rough estimate including folders, but user will get the idea
		menu = append(menu, fmt.Sprintf("processed 루트의 파일들 (폴더 제외) (루트 전체 크기: %s)", humanBytes(size)))
	}

	fmt.Println("\n===== Processed 결과 삭제 =====")
	selIdxs := selectMany(append(menu, "취소"), "삭제할 폴더 선택 (공백 구분, 취소는 마지막 번호): ")

	var toDelete []string
	for _, i := range selIdxs {
		if i == len(menu) {
			return nil
		}
		toDelete = append(toDelete, targets[i])
	}

	if len(toDelete) == 0 {
		return nil
	}

	fmt.Println("\n다음 항목을 삭제합니다:")
	for _, t := range toDelete {
		fmt.Println("-", t)
	}
	if !confirm("정말 삭제하시겠습니까?", false) {
		return nil
	}

	for _, t := range toDelete {
		if t == "." {
			// Delete files only in root
			for _, d := range dirs {
				if !d.IsDir() {
					os.Remove(filepath.Join(processedDir, d.Name()))
				}
			}
			fmt.Println("✔ 삭제 완료: processed 루트 파일들")
		} else {
			if err := os.RemoveAll(filepath.Join(processedDir, t)); err != nil {
				fmt.Fprintln(os.Stderr, "⚠ 삭제 실패:", t, err)
			} else {
				fmt.Println("✔ 삭제 완료:", t)
			}
		}
	}
	return nil
}

func cleanupArchive() error {
	sets, err := verificationSets()
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		fmt.Println("삭제할 Archive가 없습니다.")
		return nil
	}

	var menu []string
	for _, set := range sets {
		var size int64
		var files []string

		// Find all related files
		archives, _ := archiveFiles()
		for _, f := range archives {
			if strings.HasPrefix(f, set.Base+".tar") || f == set.SHA || strings.HasPrefix(f, set.Base+".vol") || f == set.Base+".par2" {
				st, _ := os.Stat(filepath.Join(archiveDir, f))
				if st != nil {
					size += st.Size()
					files = append(files, f)
				}
			}
		}

		menu = append(menu, fmt.Sprintf("%s (파일 %d개, %s)", set.Base, len(files), humanBytes(size)))
	}

	fmt.Println("\n===== Archive 그룹 삭제 =====")
	selIdxs := selectMany(append(menu, "취소"), "삭제할 그룹 선택 (공백 구분, 취소는 마지막 번호): ")

	var toDeleteSets []archiveSet
	for _, i := range selIdxs {
		if i == len(menu) {
			return nil
		}
		toDeleteSets = append(toDeleteSets, sets[i])
	}

	if len(toDeleteSets) == 0 {
		return nil
	}

	fmt.Println("\n다음 Archive 그룹에 관련된 모든 파일을 삭제합니다:")
	for _, set := range toDeleteSets {
		fmt.Println("-", set.Base)
	}
	if !confirm("정말 삭제하시겠습니까?", false) {
		return nil
	}

	for _, set := range toDeleteSets {
		archives, _ := archiveFiles()
		for _, f := range archives {
			if strings.HasPrefix(f, set.Base+".tar") || f == set.SHA || strings.HasPrefix(f, set.Base+".vol") || f == set.Base+".par2" {
				if err := os.Remove(filepath.Join(archiveDir, f)); err != nil {
					fmt.Fprintln(os.Stderr, "⚠ 삭제 실패:", f, err)
				} else {
					fmt.Println("✔ 삭제 완료:", f)
				}
			}
		}
	}
	return nil
}
