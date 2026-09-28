# Google Photos Archive Toolkit

Go 기반 Google Takeout 다운로드·GPTH Neo 실행·아카이브 관리 도구입니다.

## 빌드

Go 1.24 이상이 필요합니다. 저장소 루트에서 실행합니다.

```sh
go build -o gpth-toolkit .
```

여러 Go 파일로 구성되어 있으므로 `go build main.go`가 아닌 `go build .`를 사용합니다. Python은 빌드·실행 의존성이 아닙니다. 서버의 기존 바이너리 교체 경로는 설치 환경에 맞춰 지정하세요.

## 서버 설정

처음 실행하면 `~/.gpth-toolkit/settings.json`을 생성합니다. 기본값은 기존 서버 설정과 동일합니다.

```json
{
  "work_root": "/work",
  "rclone_remote": "gdrive",
  "server_ip": "192.168.10.115",
  "rclone_transfers": 4,
  "rclone_checkers": 8,
  "rclone_streams": 4,
  "rclone_cutoff": "250M"
}
```

`c` 메뉴에서 적용된 설정과 파일 위치를 확인합니다. 파일을 수정하고 프로그램을 다시 시작하면 적용됩니다. `GPTH_CONFIG` 환경변수로 다른 설정 파일 경로를 지정할 수 있습니다.

기본 작업 디렉터리는 `work_root` 아래의 `takeout`, `processed`, `archive`, `takeout-nfc`입니다. 필요하면 `takeout_dir`, `processed_dir`, `archive_dir`, `normalized_input_dir`에 절대 경로를 지정하세요. 각 디렉터리는 `work_root` 아래에 있어야 하고 서로 같거나 포함 관계이면 안 됩니다. 파일을 자동 이동하지 않으므로 기존 데이터 위치에 맞춰 설정해야 합니다.

## 이메일

설정이 없으면 시작 시 입력하거나 건너뛸 수 있습니다. `e` 메뉴에서 설정을 입력·변경하고 `t` 메뉴에서 발송을 테스트합니다. 상세한 암호화 방식과 기존 설정 전환은 [SMTP.md](SMTP.md)를 참고하세요.

이메일을 설정하면 기존 주요 작업의 성공 알림과 함께 0–6번 메뉴의 작업 실패도 알립니다. 사용자 취소나 SMTP 테스트 실패는 실패 알림을 추가 발송하지 않습니다. SMTP 장애는 화면에 표시하며 원래 작업 오류를 덮어쓰지 않습니다. 프로세스 강제 종료·전원 장애는 이 알림 범위에 포함되지 않습니다.

## 아카이브 검증과 오류

공백·한글이 포함된 아카이브 이름을 지원합니다. 체크섬 파일이 비어 있거나 형식이 잘못되면 성공으로 표시하지 않습니다. 디렉터리 생성·삭제, 파일 목록 조회 및 rclone 조회 오류는 작업 오류로 전달하며, 디스크 용량 집계 오류는 화면에 경고합니다.

GPTH 성공 후 임시 NFC 입력 디렉터리를 정리하는 기존 동작은 유지합니다. 삭제에 실패하면 실패 원인을 표시합니다.

## 개발 검증

```sh
gofmt -w *.go
go test ./...
go vet ./...
go build -o gpth-toolkit .
```

테스트는 임시 파일과 모의 응답을 사용하며 실제 이메일이나 Drive 전송을 수행하지 않습니다.
