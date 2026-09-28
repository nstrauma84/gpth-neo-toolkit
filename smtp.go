package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

type smtpConfig struct {
	Server   string `json:"server"`
	User     string `json:"user"`
	To       string `json:"to"`
	Password string `json:"password"`
}

var emailConfig *smtpConfig

func smtpDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gpth-toolkit"), nil
}

func (c smtpConfig) validate() error {
	host, port, err := net.SplitHostPort(c.Server)
	if err != nil || host == "" || strings.ContainsAny(host, " /\r\n\t") {
		return errors.New("SMTP 서버는 host:port 형식으로 입력하세요")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("SMTP 포트가 올바르지 않습니다")
	}
	for _, address := range []string{c.User, c.To} {
		a, err := mail.ParseAddress(address)
		if err != nil || a.Address != address || strings.ContainsAny(address, "\r\n") {
			return errors.New("발신 계정과 수신 주소는 이메일 주소로 입력하세요")
		}
	}
	if c.Password == "" {
		return errors.New("SMTP 암호가 비어 있습니다")
	}
	return nil
}

func smtpAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("SMTP 암호화 키가 올바르지 않습니다")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func loadSMTP(dir string) (*smtpConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, "smtp.enc"))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(filepath.Join(dir, "smtp.key"))
	if err != nil {
		return nil, fmt.Errorf("SMTP 키 읽기 실패: %w", err)
	}
	aead, err := smtpAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("SMTP 설정 파일이 손상되었습니다")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("gpth-smtp-v1"))
	if err != nil {
		return nil, errors.New("SMTP 설정 복호화 실패: 키 또는 파일을 확인하세요")
	}
	var c smtpConfig
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, errors.New("SMTP 설정 형식이 올바르지 않습니다")
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveSMTP(dir string, c smtpConfig) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	keyPath := filepath.Join(dir, "smtp.key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(key)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		return err
	}
	aead, err := smtpAEAD(key)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	data := aead.Seal(nonce, nonce, plain, []byte("gpth-smtp-v1"))
	f, err := os.CreateTemp(dir, ".smtp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
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
	return os.Rename(f.Name(), filepath.Join(dir, "smtp.enc"))
}

func initSMTP() {
	dir, err := smtpDir()
	if err == nil {
		emailConfig, err = loadSMTP(dir)
		if err == nil {
			return
		}
		// Only an absent config is normal; a missing key must remain visible.
		if _, statErr := os.Stat(filepath.Join(dir, "smtp.enc")); !os.IsNotExist(statErr) {
			fmt.Fprintln(os.Stderr, "⚠", err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "⚠", err)
	}
	fmt.Println("SMTP 설정이 없습니다. 알림 없이 계속할 수 있습니다.")
	if err := configureSMTP(); err != nil {
		fmt.Fprintln(os.Stderr, "⚠ SMTP 설정:", err)
	}
}

func configureSMTP() error {
	if !confirm("SMTP 알림 설정을 입력/변경할까요? (건너뛰면 현재 상태 유지)", false) {
		return nil
	}
	c := smtpConfig{Server: "smtp.gmail.com:465"}
	if emailConfig != nil {
		c = *emailConfig
	}
	prompt := func(label, current string) string {
		fmt.Printf("%s [%s]: ", label, current)
		if value := readLine(); value != "" {
			return value
		}
		return current
	}
	c.Server = prompt("SMTP 서버 (465: TLS, 그 외: STARTTLS)", c.Server)
	c.User = prompt("발신 계정 이메일", c.User)
	c.To = prompt("수신 이메일", c.To)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("암호는 터미널에서 직접 입력해야 합니다")
	}
	fmt.Print("SMTP 암호/앱 비밀번호 (기존 암호 유지: Enter): ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return err
	}
	if len(password) > 0 {
		c.Password = string(password)
	}
	if err := c.validate(); err != nil {
		return err
	}
	dir, err := smtpDir()
	if err != nil {
		return err
	}
	if err := saveSMTP(dir, c); err != nil {
		return err
	}
	emailConfig = &c
	fmt.Println("✔ SMTP 설정을 암호화해 저장했습니다.")
	return nil
}

func showSMTPSettings(w io.Writer) {
	fmt.Fprintln(w, "===== 이메일 알림 설정 =====")
	if emailConfig == nil {
		fmt.Fprintln(w, "현재 상태: 미설정 / 알림 비활성화")
		return
	}
	fmt.Fprintf(w, "SMTP 서버: %s\n발신 계정: %s\n수신 주소: %s\n암호: 저장됨 (표시하지 않음)\n", emailConfig.Server, emailConfig.User, emailConfig.To)
}

func smtpMenu() error {
	for {
		showSMTPSettings(os.Stdout)
		fmt.Println("1) 설정 입력 / 수정\n2) 테스트 이메일 발송\n0) 돌아가기")
		fmt.Print("선택 [0]: ")
		line, err := in.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch strings.TrimSpace(line) {
		case "", "0":
			return nil
		case "1":
			err = configureSMTP()
		case "2":
			if emailConfig == nil {
				fmt.Println("먼저 1번에서 이메일 설정을 입력하세요.")
				continue
			}
			err = testSMTP()
		default:
			fmt.Println("잘못된 번호입니다.")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "⚠ 이메일 설정:", err)
		}
	}
}

func sendEmail(task, status, details string) {
	if emailConfig == nil {
		return
	}
	if err := deliverEmail(*emailConfig, task, status, details); err != nil {
		fmt.Fprintln(os.Stderr, "⚠ 이메일 실패:", err)
	}
}

func deliverEmail(c smtpConfig, task, status, details string) error {
	if err := c.validate(); err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(c.Server)
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", c.Server, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", c.Server)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if port != "465" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if err := client.Auth(smtp.PlainAuth("", c.User, c.Password, host)); err != nil {
		return err
	}
	if err := client.Mail(c.User); err != nil {
		return err
	}
	if err := client.Rcpt(c.To); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	subject := mime.BEncoding.Encode("UTF-8", "[GPTH Server] "+task+" 작업 "+status)
	body := fmt.Sprintf("From: <%s>\r\nTo: <%s>\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n[작업 결과 보고]\r\n- 작업 항목 : %s\r\n- 상태      : %s\r\n- 발생 시각 : %s\r\n\r\n%s\r\n", c.User, c.To, subject, task, status, time.Now().Format("2006-01-02 15:04:05"), details)
	if _, err := io.WriteString(w, body); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func testSMTP() error {
	if emailConfig == nil {
		if err := configureSMTP(); err != nil {
			return err
		}
		if emailConfig == nil {
			return nil
		}
	}
	fmt.Println("발신 계정 :", emailConfig.User, "\n수신 대상 :", emailConfig.To, "\n전송 시도 중...")
	if err := deliverEmail(*emailConfig, "SMTP 연결 테스트", "성공", "이 메일은 Google Photos Archive Toolkit Go 버전에서 보낸 테스트 메일입니다."); err != nil {
		return err
	}
	fmt.Println("✔ SMTP 서버가 메일을 수락했습니다.")
	return nil
}
