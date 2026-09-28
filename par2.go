package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

var volumeSuffix = regexp.MustCompile(`(?i)\.vol[0-9]+\+[0-9]+\.par2$`)

func parityBase(name string) string {
	if volumeSuffix.MatchString(name) {
		return volumeSuffix.ReplaceAllString(name, "")
	}
	return name[:len(name)-len(".par2")]
}
func isArchiveFile(s string) bool {
	return strings.HasSuffix(s, ".tar") || strings.Contains(s, ".tar.part.") || strings.HasSuffix(s, ".sha256.txt") || strings.HasSuffix(strings.ToLower(s), ".par2")
}
func parityFiles(base string) ([]string, error) {
	return filesMatching(archiveDir, func(s string) bool { return strings.HasSuffix(strings.ToLower(s), ".par2") && parityBase(s) == base })
}
func chooseParity() (int, error) {
	if !confirm("PAR2 복구 데이터도 생성할까요?", false) {
		return 0, nil
	}
	if !existsCmd("par2") {
		return 0, errors.New("par2가 없습니다. Debian/Ubuntu: apt-get install par2")
	}
	for {
		fmt.Print("복구 데이터 비율 (1–100%, 기본 5): ")
		line, err := in.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return 5, nil
		}
		n, err := strconv.Atoi(line)
		if err == nil && n >= 1 && n <= 100 {
			return n, nil
		}
		fmt.Println("1부터 100 사이의 정수를 입력하세요.")
	}
}
func spaceAvailable() (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(archiveDir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
func parityEstimate(size int64, percent int) int64 {
	if percent == 0 {
		return 0
	}
	// Budget block rounding and packet/index overhead; this is not an exact size.
	if size < 0 || percent < 0 || percent > 100 {
		return math.MaxInt64
	}
	data := size/100*int64(percent) + (size%100*int64(percent)+99)/100
	overhead := size/100 + 16*1024*1024
	if data > math.MaxInt64-overhead {
		return math.MaxInt64
	}
	return data + overhead
}
func archiveSpacePlan(size int64, percent int) error {
	// TAR headers/padding depend on the source tree; retain a conservative margin.
	margin := max64(size/100, 64*1024*1024)
	if size < 0 || size > math.MaxInt64-margin {
		return errors.New("Archive 예상 용량 범위 초과")
	}
	tarSize := size + margin
	parity := parityEstimate(tarSize, percent)
	if parity > math.MaxInt64-tarSize {
		return errors.New("Archive/PAR2 예상 용량 범위 초과")
	}
	available, err := spaceAvailable()
	if err != nil {
		return err
	}
	fmt.Printf("\n예상 TAR 용량: 약 %s\n예상 PAR2 용량(여유분 포함): 약 %s\n추가 필요 공간 합계: 약 %s\n현재 여유 공간: %s\n", humanBytes(tarSize), humanBytes(parity), humanBytes(tarSize+parity), humanBytes(available))
	fmt.Println("실제 크기는 파일 수·블록 크기에 따라 달라집니다. 기존 Archive 삭제로 확보할 공간은 계산에서 제외합니다.")
	if available < tarSize+parity {
		return fmt.Errorf("예상 생성 공간이 %s 부족합니다", humanBytes(tarSize+parity-available))
	}
	return nil
}
func removeOldParity(base string) error {
	files, err := parityFiles(base)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	fmt.Println("같은 이름의 기존 PAR2는 새 Archive와 호환되지 않습니다:", strings.Join(files, ", "))
	if !confirm("기존 PAR2를 삭제하고 새 Archive를 만들까요?", false) {
		return errCancelled
	}
	for _, name := range files {
		if err := os.Remove(filepath.Join(archiveDir, name)); err != nil {
			return err
		}
	}
	return nil
}
func runParity(action, index string, extra ...string) (int, error) {
	dir, err := filepath.EvalSymlinks(archiveDir)
	if err != nil {
		return -1, err
	}
	args := []string{action, "-m256", "-B" + dir}
	args = append(args, extra...)
	args = append(args, "--", index)
	cmd := exec.Command("par2", args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return -1, err
}
func createParity(base, shaPath string, percent int) error {
	data, err := os.ReadFile(shaPath)
	if err != nil {
		return err
	}
	entries, err := parseChecksums(string(data))
	if err != nil {
		return err
	}
	var files []protectedFile
	var sources []string
	for _, e := range entries {
		st, err := os.Stat(filepath.Join(archiveDir, e.Name))
		if err != nil {
			return err
		}
		files = append(files, protectedFile{e.Name, st.Size()})
		sources = append(sources, e.Name)
	}
	total, err := protectedSize(files)
	if err != nil {
		return err
	}
	need := parityEstimate(total, percent)
	available, err := spaceAvailable()
	if err != nil {
		return err
	}
	fmt.Printf("\nPAR2 생성: %d%%\n보호 대상: %s\n추가 필요 공간(예상): %s\n현재 여유 공간: %s\n", percent, humanBytes(total), humanBytes(need), humanBytes(available))
	if available < need {
		return fmt.Errorf("TAR/SHA256은 생성됐지만 PAR2 공간이 %s 부족합니다", humanBytes(need-available))
	}
	dir, err := filepath.EvalSymlinks(archiveDir)
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp(dir, ".par2-create-*")
	if err != nil {
		return err
	}
	defer warnCleanup(temp, true)
	index := filepath.Join(temp, base+".par2")
	args := []string{"create", "-m256", "-r" + strconv.Itoa(percent), "-B" + dir, "--", index}
	args = append(args, sources...)
	cmd := exec.Command("par2", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("TAR/SHA256 생성 완료, PAR2 생성 실패: %w", err)
	}
	generated, err := os.ReadDir(temp)
	if err != nil {
		return err
	}
	if _, err := os.Stat(index); err != nil {
		return fmt.Errorf("PAR2 인덱스 생성 실패: %w", err)
	}
	// Publish the index last; never silently overwrite another recovery set.
	sort.Slice(generated, func(i, j int) bool {
		return generated[i].Name() != base+".par2" && (generated[j].Name() == base+".par2" || generated[i].Name() < generated[j].Name())
	})
	for _, e := range generated {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".par2") {
			continue
		}
		dest := filepath.Join(archiveDir, e.Name())
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("동일한 PAR2가 이미 있습니다: %s", e.Name())
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(filepath.Join(temp, e.Name()), dest); err != nil {
			return err
		}
	}
	fmt.Println("✔ PAR2 생성 완료:", base+".par2")
	return nil
}

func repairSpace(files []protectedFile) (int64, error) {
	total, err := protectedSize(files)
	if err != nil {
		return 0, err
	}
	// Budget a complete rebuilt copy; damaged backups may remain on disk.
	margin := max64(total/100, 16*1024*1024)
	if total > math.MaxInt64-margin {
		return 0, errors.New("복구 예상 용량 범위 초과")
	}
	need := total + margin
	available, err := spaceAvailable()
	if err != nil {
		return 0, err
	}
	fmt.Printf("\nPAR2에 기록된 원본 전체 크기: %s\n복구 추가 필요 공간(보수적 예상): %s\n현재 여유 공간: %s\n", humanBytes(total), humanBytes(need), humanBytes(available))
	fmt.Println("손상본을 보존한 채 원본 전체를 다시 만들 공간과 여유분을 계산합니다. 실제 필요량은 복구 대상에 따라 더 적을 수 있습니다.")
	if available < need {
		return need, fmt.Errorf("복구 예상 공간이 %s 부족합니다. 공간 확보 후 다시 검사하세요", humanBytes(need-available))
	}
	return need, nil
}
