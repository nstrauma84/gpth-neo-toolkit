package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type settings struct {
	WorkRoot           string `json:"work_root"`
	TakeoutDir         string `json:"takeout_dir,omitempty"`
	ProcessedDir       string `json:"processed_dir,omitempty"`
	ArchiveDir         string `json:"archive_dir,omitempty"`
	NormalizedInputDir string `json:"normalized_input_dir,omitempty"`
	Remote             string `json:"rclone_remote"`
	ServerIP           string `json:"server_ip"`
	Transfers          int    `json:"rclone_transfers"`
	Checkers           int    `json:"rclone_checkers"`
	Streams            int    `json:"rclone_streams"`
	Cutoff             string `json:"rclone_cutoff"`
}

func defaultSettings() settings {
	return settings{WorkRoot: "/work", Remote: "gdrive", ServerIP: "192.168.10.115", Transfers: 4, Checkers: 8, Streams: 4, Cutoff: "250M"}
}

var settingsPath string
var activeSettings settings

func decodeSettings(data []byte) (settings, error) {
	c := defaultSettings()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return c, errors.New("설정 파일에는 JSON 객체 하나만 허용됩니다")
	}
	if !filepath.IsAbs(c.WorkRoot) || filepath.Clean(c.WorkRoot) == "/" {
		return c, errors.New("work_root는 루트(/) 이외의 절대 경로여야 합니다")
	}
	c.WorkRoot = filepath.Clean(c.WorkRoot)
	paths := []*string{&c.TakeoutDir, &c.ProcessedDir, &c.ArchiveDir, &c.NormalizedInputDir}
	names := []string{"takeout", "processed", "archive", "takeout-nfc"}
	for i, p := range paths {
		if *p == "" {
			*p = filepath.Join(c.WorkRoot, names[i])
		}
		if !filepath.IsAbs(*p) || strings.ContainsAny(*p, "\r\n\x00") {
			return c, errors.New("작업 경로는 유효한 절대 경로여야 합니다")
		}
		*p = filepath.Clean(*p)
		rel, err := filepath.Rel(c.WorkRoot, *p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return c, errors.New("작업 디렉터리는 work_root의 하위 경로여야 합니다")
		}
		// Recursive cleanup must never include another working directory.
		for _, prev := range paths[:i] {
			if *p == *prev || strings.HasPrefix(*p, *prev+string(os.PathSeparator)) || strings.HasPrefix(*prev, *p+string(os.PathSeparator)) {
				return c, errors.New("작업 디렉터리는 같거나 서로 포함될 수 없습니다")
			}
		}
	}
	if strings.TrimSpace(c.Remote) == "" || strings.ContainsAny(c.Remote, ":/\\\r\n\t") {
		return c, errors.New("rclone_remote에는 remote 이름만 입력하세요")
	}
	if net.ParseIP(c.ServerIP) == nil {
		return c, errors.New("server_ip는 유효한 IP 주소여야 합니다")
	}
	if c.Transfers < 1 || c.Checkers < 1 || c.Streams < 1 {
		return c, errors.New("전송 병렬도는 1 이상이어야 합니다")
	}
	if !regexp.MustCompile(`^[1-9][0-9]*(?:\.[0-9]+)?[kKMGTPE]?$`).MatchString(c.Cutoff) {
		return c, errors.New("rclone_cutoff 예: 250M, 1G")
	}
	return c, nil
}

func initSettings() error {
	settingsPath = os.Getenv("GPTH_CONFIG")
	if settingsPath == "" {
		dir, err := smtpDir()
		if err != nil {
			return err
		}
		settingsPath = filepath.Join(dir, "settings.json")
	}
	data, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
			return err
		}
		data, err = json.MarshalIndent(defaultSettings(), "", "  ")
		if err != nil {
			return err
		}
		f, err := os.OpenFile(settingsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(append(data, '\n'))
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	}
	c, err := decodeSettings(data)
	if err != nil {
		return fmt.Errorf("%s: %w", settingsPath, err)
	}
	activeSettings = c
	workRoot, takeoutDir, processedDir, archiveDir, normalizedInputDir = c.WorkRoot, c.TakeoutDir, c.ProcessedDir, c.ArchiveDir, c.NormalizedInputDir
	remote, containerIP = c.Remote, c.ServerIP
	rcloneTransfers, rcloneCheckers, rcloneStreams, rcloneCutoff = strconv.Itoa(c.Transfers), strconv.Itoa(c.Checkers), strconv.Itoa(c.Streams), c.Cutoff
	return nil
}

func showSettings() {
	fmt.Println("서버 설정 파일:", settingsPath)
	b, _ := json.MarshalIndent(activeSettings, "", "  ")
	fmt.Println(string(b))
	fmt.Println("설정 파일 수정 후 프로그램을 다시 시작하면 적용됩니다. SMTP 설정은 e 메뉴에서 변경하세요.")
}
