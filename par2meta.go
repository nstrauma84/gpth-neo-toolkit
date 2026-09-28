package main

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type protectedFile struct {
	Name string
	Size int64
}

// Read only bounded metadata packets, skipping recovery payloads with Seek.
// Packet layout: https://github.com/Parchive/par2cmdline/blob/master/src/par2fileformat.h
func paritySources(path string) ([]protectedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	descriptions := map[string]protectedFile{}
	var ids []string
	var setID []byte
	var offset int64
	for offset < st.Size() {
		header := make([]byte, 64)
		if _, err := io.ReadFull(f, header); err != nil {
			return nil, err
		}
		if string(header[:8]) != "PAR2\x00PKT" {
			return nil, errors.New("PAR2 패킷 헤더 손상")
		}
		length := binary.LittleEndian.Uint64(header[8:16])
		if length < 64 || length%4 != 0 || length > uint64(st.Size()-offset) {
			return nil, errors.New("PAR2 패킷 길이 오류")
		}
		kind := string(bytes.TrimRight(header[48:64], "\x00"))
		if kind != "PAR 2.0\x00Main" && kind != "PAR 2.0\x00FileDesc" {
			if _, err := f.Seek(int64(length)-64, io.SeekCurrent); err != nil {
				return nil, err
			}
			offset += int64(length)
			continue
		}
		if length > 1024*1024 {
			return nil, errors.New("PAR2 메타데이터가 너무 큽니다")
		}
		body := make([]byte, int(length)-64)
		if _, err := io.ReadFull(f, body); err != nil {
			return nil, err
		}
		h := md5.New()
		h.Write(header[32:])
		h.Write(body)
		if !bytes.Equal(h.Sum(nil), header[16:32]) {
			return nil, errors.New("PAR2 메타데이터 체크섬 오류")
		}
		if setID == nil {
			setID = append([]byte(nil), header[32:48]...)
		} else if !bytes.Equal(setID, header[32:48]) {
			return nil, errors.New("여러 PAR2 세트가 섞여 있습니다")
		}
		if kind == "PAR 2.0\x00Main" {
			if len(body) < 12 || (len(body)-12)%16 != 0 {
				return nil, errors.New("PAR2 Main 패킷 오류")
			}
			sum := md5.Sum(body)
			if !bytes.Equal(sum[:], setID) {
				return nil, errors.New("PAR2 세트 ID 오류")
			}
			count := int(binary.LittleEndian.Uint32(body[8:12]))
			if count < 1 || count > (len(body)-12)/16 {
				return nil, errors.New("PAR2 보호 파일 수 오류")
			}
			ids = nil
			for i := 0; i < count; i++ {
				ids = append(ids, string(body[12+i*16:28+i*16]))
			}
		} else {
			if len(body) < 60 {
				return nil, errors.New("PAR2 FileDesc 패킷 오류")
			}
			size := binary.LittleEndian.Uint64(body[48:56])
			name := string(bytes.TrimRight(body[56:], "\x00"))
			if size > math.MaxInt64 || !safeArchiveName(name) || filepath.Base(name) != name {
				return nil, errors.New("PAR2 보호 파일 경로/크기 오류")
			}
			descriptions[string(body[:16])] = protectedFile{name, int64(size)}
		}
		offset += int64(length)
	}
	if len(ids) == 0 {
		return nil, errors.New("PAR2 보호 파일 정보를 찾지 못했습니다")
	}
	files := make([]protectedFile, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		file, ok := descriptions[id]
		if !ok {
			return nil, errors.New("PAR2 파일 설명이 부족합니다")
		}
		if seen[file.Name] {
			return nil, errors.New("PAR2 보호 파일명이 중복됩니다")
		}
		seen[file.Name] = true
		files = append(files, file)
	}
	return files, nil
}

func protectedSize(files []protectedFile) (int64, error) {
	var total int64
	for _, f := range files {
		if f.Size < 0 || total > math.MaxInt64-f.Size {
			return 0, errors.New("용량 범위 초과")
		}
		total += f.Size
	}
	return total, nil
}

func validateParityTargets(files []protectedFile, base string) error {
	for _, f := range files {
		if f.Name != base+".tar" && !strings.HasPrefix(f.Name, base+".tar.part.") {
			return fmt.Errorf("PAR2 대상 %s가 선택한 Archive %s와 다릅니다", f.Name, base)
		}
		st, err := os.Lstat(filepath.Join(archiveDir, f.Name))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !st.Mode().IsRegular() {
			return fmt.Errorf("일반 파일이 아닌 복구 대상: %s", f.Name)
		}
	}
	return nil
}
