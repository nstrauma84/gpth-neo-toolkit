package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func initRemote() {
	if remote != "" {
		return
	}
	fmt.Println("기본 rclone remote가 없습니다. 등록된 목록에서 선택하세요.")
	if err := configureRemote(); err != nil {
		fmt.Fprintln(os.Stderr, "⚠ remote 설정:", err)
	}
}

func registeredRemotes() ([]string, error) {
	if !existsCmd("rclone") {
		return nil, errors.New("rclone이 없습니다. 설치 후 r 메뉴에서 설정하세요")
	}
	out, err := output("rclone", "listremotes")
	if err != nil {
		return nil, err
	}
	var names []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, ":") {
			continue
		}
		name := strings.TrimSuffix(line, ":")
		if name != "" && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("등록된 remote가 없습니다. rclone config로 등록한 뒤 r 메뉴에서 선택하세요")
	}
	return names, nil
}

func configureRemote() error {
	names, err := registeredRemotes()
	if err != nil {
		return err
	}
	fmt.Println("현재 기본 remote:", remoteLabel())
	for i, name := range names {
		fmt.Printf("%d) %s\n", i+1, name)
	}
	fmt.Println("0) 변경 없이 돌아가기")
	for {
		fmt.Print("기본 remote 선택 [0]: ")
		line, err := in.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" || line == "0" {
			return nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(names) {
			fmt.Println("목록의 번호를 입력하세요.")
			continue
		}
		if err := saveDefaultRemote(names[n-1]); err != nil {
			return err
		}
		fmt.Println("✔ 기본 remote 저장:", remote)
		return nil
	}
}

func remoteLabel() string {
	if remote == "" {
		return "미설정"
	}
	return remote
}

func saveDefaultRemote(name string) error {
	// Update only this key, preserving omitted (derived) paths and other settings.
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("서버 설정은 JSON 객체여야 합니다")
	}
	fields["rclone_remote"], err = json.Marshal(name)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	if _, err := decodeSettings(data); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(settingsPath), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), settingsPath); err != nil {
		return err
	}
	remote, activeSettings.Remote = name, name
	return nil
}
