package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errCancelled = errors.New("취소")

var notifyTaskFailure = sendEmail

func reportTaskError(choice string, err error) {
	if err == nil || errors.Is(err, errCancelled) {
		return
	}
	fmt.Fprintln(os.Stderr, "✘", err)
	task := map[string]string{"0": "GPTH Neo 업데이트", "1": "Google Takeout 다운로드", "2": "GPTH Neo 사진 정리", "3": "TAR + SHA256 생성", "4": "Google Drive 아카이브 업로드", "5": "Archive Retrieve 다운로드", "6": "Archive SHA256 검증"}[strings.ToLower(choice)]
	if task != "" {
		notifyTaskFailure(task, "실패", err.Error())
	}
}

func warnCleanup(path string, recursive bool) {
	var err error
	if recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "⚠ 임시 파일 정리 %s: %v\n", path, err)
	}
}

func remoteExists(list, name string) bool {
	for _, line := range strings.Split(list, "\n") {
		if strings.TrimSpace(line) == name+":" {
			return true
		}
	}
	return false
}

func selectedFilter(names []string) (string, error) {
	f, err := os.CreateTemp("", "rclone-filter-*")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			warnCleanup(f.Name(), false)
		}
	}()
	for _, name := range names {
		if strings.ContainsAny(name, "\r\n") {
			return "", errors.New("줄바꿈이 포함된 파일명은 지원하지 않습니다")
		}
		if _, err := fmt.Fprintf(f, "+ /%s\n", name); err != nil {
			return "", err
		}
	}
	if _, err := fmt.Fprintln(f, "- *"); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	success = true
	return f.Name(), nil
}

type checksumEntry struct{ Hash, Name string }

func parseChecksums(data string) ([]checksumEntry, error) {
	var entries []checksumEntry
	for i, line := range strings.Split(data, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return nil, fmt.Errorf("SHA256 %d행: 올바르지 않은 형식", i+1)
		}
		hash, name := line[:64], line[66:]
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("SHA256 %d행: 해시 형식 오류", i+1)
		}
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\\\x00") {
			return nil, fmt.Errorf("SHA256 %d행: 올바르지 않은 파일명", i+1)
		}
		entries = append(entries, checksumEntry{strings.ToLower(hash), name})
	}
	if len(entries) == 0 {
		return nil, errors.New("SHA256 파일에 검증할 항목이 없습니다")
	}
	return entries, nil
}

func safeArchiveName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\*?[]") {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
