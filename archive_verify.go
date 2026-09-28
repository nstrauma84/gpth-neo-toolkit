package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type archiveSet struct{ Base, SHA, PAR string }

func verificationSets() ([]archiveSet, error) {
	files, err := archiveFiles()
	if err != nil {
		return nil, err
	}
	sets := map[string]*archiveSet{}
	add := func(base string) *archiveSet {
		if sets[base] == nil {
			sets[base] = &archiveSet{Base: base}
		}
		return sets[base]
	}
	for _, file := range files {
		switch {
		case strings.HasSuffix(file, ".sha256.txt"):
			add(strings.TrimSuffix(file, ".sha256.txt")).SHA = file
		case strings.HasSuffix(strings.ToLower(file), ".par2"):
			base := parityBase(file)
			set := add(base)
			if set.PAR == "" || !volumeSuffix.MatchString(file) {
				set.PAR = file
			}
		case strings.HasSuffix(file, ".tar"):
			add(strings.TrimSuffix(file, ".tar"))
		case strings.Contains(file, ".tar.part."):
			add(file[:strings.LastIndex(file, ".tar.part.")])
		}
	}
	var result []archiveSet
	for _, set := range sets {
		result = append(result, *set)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Base < result[j].Base })
	return result, nil
}

func checkSHA(set archiveSet) error {
	if set.SHA == "" {
		fmt.Println("SHA256: 파일 없음 (PAR2만 검사 가능)")
		return nil
	}
	data, err := os.ReadFile(filepath.Join(archiveDir, set.SHA))
	if err != nil {
		return err
	}
	entries, err := parseChecksums(string(data))
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		hash, err := shaFileWithProgress(filepath.Join(archiveDir, entry.Name))
		if err != nil {
			label := "검사 오류"
			if os.IsNotExist(err) {
				label = "파일 누락"
			}
			fmt.Printf("SHA256: %s — %s\n", entry.Name, label)
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name, err))
			continue
		}
		if hash != entry.Hash {
			fmt.Println("SHA256:", entry.Name, "불일치")
			failures = append(failures, fmt.Errorf("%s: SHA256 불일치", entry.Name))
		} else {
			fmt.Println("SHA256:", entry.Name, "일치")
		}
	}
	return errors.Join(failures...)
}

func parityStatus(code int) string {
	switch code {
	case 0:
		return "정상 · 복구 불필요"
	case 1:
		return "손상 · 복구 가능"
	case 2:
		return "손상 · 복구 데이터 부족"
	default:
		return "검사 오류"
	}
}

func verifyArchive() error {
	fmt.Println("===== Archive SHA256 / PAR2 검사 및 복구 =====")
	sets, err := verificationSets()
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		return errors.New("검사할 Archive/SHA256/PAR2 파일이 없습니다")
	}
	var labels []string
	for _, set := range sets {
		labels = append(labels, set.Base)
	}
	set := sets[selectOne(labels, "검사할 Archive 선택: ")]
	return verifySet(set)
}

func verifySet(set archiveSet) error {
	shaErr := checkSHA(set)
	if shaErr != nil {
		fmt.Fprintln(os.Stderr, "SHA256 검사 결과:", shaErr)
	}
	if set.PAR == "" {
		fmt.Println("PAR2: 없음")
		if set.SHA == "" {
			return errors.New("SHA256과 PAR2가 모두 없어 검증할 수 없습니다")
		}
		return shaErr
	}
	if !existsCmd("par2") {
		return errors.Join(shaErr, errors.New("PAR2: 검사 오류 — par2가 설치되지 않았습니다 (apt-get install par2)"))
	}
	index := filepath.Join(archiveDir, set.PAR)
	files, metaErr := paritySources(index)
	if metaErr == nil {
		metaErr = validateParityTargets(files, set.Base)
	}
	if metaErr != nil {
		return errors.Join(shaErr, fmt.Errorf("PAR2: 검사 오류 — %w", metaErr))
	}
	// SHA may fail or be missing; PAR2 verification still runs independently.
	code, err := runParity("verify", index)
	if err != nil {
		return errors.Join(shaErr, fmt.Errorf("PAR2: 검사 오류 — %w", err))
	}
	fmt.Println("PAR2:", parityStatus(code))
	switch code {
	case 0:
		if shaErr != nil {
			fmt.Println("PAR2는 정상이지만 SHA256은 실패했습니다. 다른 버전의 검사 파일인지 확인하세요.")
		}
		return shaErr
	case 2:
		fmt.Println("추가 PAR2 복구 볼륨을 5번 가져오기로 받은 뒤 다시 검사하세요. 필요한 블록 수는 위 par2 출력에서 확인할 수 있습니다.")
		_, spaceErr := repairSpace(files)
		return errors.Join(shaErr, spaceErr, errors.New("PAR2 복구 데이터 부족"))
	case 1:
		if _, err := repairSpace(files); err != nil {
			return errors.Join(shaErr, err)
		}
		fmt.Println("복구 시 손상 파일이 변경되며 par2가 만든 백업은 자동 삭제하지 않습니다.")
		if !confirm("PAR2로 실제 복구를 실행할까요?", false) {
			return errors.Join(shaErr, errors.New("PAR2 복구 가능: 복구를 실행하지 않았습니다"))
		}
	default:
		return errors.Join(shaErr, fmt.Errorf("PAR2 검사 오류 (종료 코드 %d)", code))
	}
	code, err = runParity("repair", index)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("PAR2 복구 실패 (종료 코드 %d)", code)
	}
	fmt.Println("복구 후 PAR2와 SHA256을 다시 검사합니다.")
	code, err = runParity("verify", index)
	// Recheck SHA even if PAR2 fails after the repair.
	shaErr = checkSHA(set)
	if err != nil {
		return errors.Join(err, shaErr)
	}
	if code != 0 {
		return errors.Join(fmt.Errorf("복구 후 PAR2: %s (코드 %d)", parityStatus(code), code), shaErr)
	}
	if shaErr != nil {
		return shaErr
	}
	if set.SHA == "" {
		fmt.Println("✔ PAR2 기준 복구/검증 완료 (SHA256 파일 없음)")
	} else {
		fmt.Println("✔ 복구 후 PAR2 정상 / SHA256 일치")
	}
	sendEmail("Archive PAR2 복구", "성공", "- Archive: "+set.Base+"\n- 복구 후 PAR2 검사 통과\n- SHA256 파일: "+set.SHA)
	return nil
}
