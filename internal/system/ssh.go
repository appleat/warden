package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHTarget 描述一次 SSH 连接。
type SSHTarget struct {
	Host       string
	Port       int
	User       string
	AuthType   string // password | key
	Secret     string
	Passphrase string
}

func DialSSH(t SSHTarget, timeout time.Duration) (*ssh.Client, error) {
	if t.Host == "" {
		return nil, errors.New("主机地址不能为空")
	}
	if t.Port <= 0 {
		t.Port = 22
	}
	var auths []ssh.AuthMethod
	switch t.AuthType {
	case "key":
		var signer ssh.Signer
		var err error
		if t.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(t.Secret), []byte(t.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(t.Secret))
		}
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	default:
		auths = append(auths, ssh.Password(t.Secret))
	}

	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: auths,
		// 管理面板面向内网运维，首次连接不强制校验主机指纹；
		// 如需严格校验可在此处接入 known_hosts。
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	return ssh.Dial("tcp", addr, cfg)
}

// TestSSH 建立连接后立即关闭，用于「测试连接」按钮。
func TestSSH(t SSHTarget) error {
	cli, err := DialSSH(t, 8*time.Second)
	if err != nil {
		return err
	}
	defer cli.Close()
	sess, err := cli.NewSession()
	if err != nil {
		return err
	}
	return sess.Close()
}

// RunSSHCommand 在远端执行一条命令并返回输出。
func RunSSHCommand(t SSHTarget, cmd string, timeout time.Duration) (string, error) {
	cli, err := DialSSH(t, timeout)
	if err != nil {
		return "", err
	}
	defer cli.Close()
	sess, err := cli.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	var buf bytes.Buffer
	sess.Stdout = &buf
	sess.Stderr = &buf
	if err := sess.Start(cmd); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(timeout):
		_ = sess.Signal(ssh.SIGKILL)
		return buf.String(), errors.New("命令执行超时")
	}
}

type sshTerm struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	ctx     context.Context
}

// StartSSHShell 打开一个远程交互式 shell。
func StartSSHShell(ctx context.Context, t SSHTarget, cols, rows uint16, shell string) (Term, error) {
	client, err := DialSSH(t, 10*time.Second)
	if err != nil {
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	if cols == 0 {
		cols = 100
	}
	if rows == 0 {
		rows = 30
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", int(rows), int(cols), modes); err != nil {
		sess.Close()
		client.Close()
		return nil, fmt.Errorf("申请终端失败: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	if shell != "" {
		err = sess.Start(shell)
	} else {
		err = sess.Shell()
	}
	if err != nil {
		sess.Close()
		client.Close()
		return nil, fmt.Errorf("启动 shell 失败: %w", err)
	}
	return &sshTerm{client: client, session: sess, stdin: stdin, stdout: stdout, ctx: ctx}, nil
}

func (s *sshTerm) Read(b []byte) (int, error)  { return s.stdout.Read(b) }
func (s *sshTerm) Write(b []byte) (int, error) { return s.stdin.Write(b) }

func (s *sshTerm) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	return s.session.WindowChange(int(rows), int(cols))
}

func (s *sshTerm) Close() error {
	_ = s.stdin.Close()
	_ = s.session.Close()
	return s.client.Close()
}
