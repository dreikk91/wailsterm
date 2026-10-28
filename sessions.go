package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"go.bug.st/serial"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Term is one terminal session: what electerm's session-*.js classes provide to session-server.js.
type Term struct {
	pid    string
	out    io.Reader
	in     io.Writer
	resize func(cols, rows int) error
	close  func() error
	client *ssh.Client // set for SSH sessions; SFTP rides on it
	once   sync.Once
}

func (t *Term) Close() {
	t.once.Do(func() { t.close() })
}

// opts is the initOptions object electerm's client sends with create-terminal.
type opts map[string]any

func (o opts) str(k string) string {
	if s, ok := o[k].(string); ok {
		return s
	}
	return ""
}

func (o opts) num(k string, def int) int {
	switch v := o[k].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func (o opts) strs(k string) []string {
	a, _ := o[k].([]any)
	out := make([]string, 0, len(a))
	for _, v := range a {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// asker sends a session-interactive prompt to the client and waits for its answers.
type asker func(name, instruction string, prompts []string, echos []bool) ([]string, error)

func startSession(o opts, ask asker) (*Term, error) {
	typ := o.str("termType")
	if typ == "" {
		typ = o.str("type")
	}
	switch {
	case typ == "local" || (o.str("host") == "" && typ != "serial"):
		return startLocal(o)
	case typ == "telnet":
		return startTelnet(o)
	case typ == "serial":
		return startSerial(o)
	case typ == "ssh" || typ == "remote" || typ == "":
		return startSSH(o, ask)
	}
	return nil, errors.New("session type not supported yet: " + typ)
}

func startLocal(o opts) (*Term, error) {
	var exe string
	var args []string
	switch runtime.GOOS {
	case "windows":
		exe, args = o.str("execWindows"), o.strs("execWindowsArgs")
		if strings.Contains(exe, "..") {
			return nil, errors.New(`execWindows should not contain ".."`)
		}
		if exe == "" {
			exe = "System32/WindowsPowerShell/v1.0/powershell.exe"
		}
		if !filepath.IsAbs(exe) {
			exe = filepath.Join(os.Getenv("windir"), exe)
		}
	case "darwin":
		exe, args = o.str("execMac"), append([]string{"--login"}, o.strs("execMacArgs")...)
	default:
		exe, args = o.str("execLinux"), o.strs("execLinuxArgs")
	}
	if exe == "" {
		exe = os.Getenv("SHELL")
	}
	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	p.Resize(o.num("cols", 80), o.num("rows", 24))
	cmd := p.Command(exe, args...)
	cmd.Dir, _ = os.UserHomeDir()
	if d := o.str("startDirectoryLocal"); d != "" {
		cmd.Dir = d
	}
	term := o.str("term")
	if term == "" {
		term = "xterm-256color"
	}
	cmd.Env = append(os.Environ(), "TERM="+term)
	if err := cmd.Start(); err != nil {
		p.Close()
		return nil, err
	}
	go cmd.Wait()
	return &Term{out: p, in: p, resize: p.Resize, close: p.Close}, nil
}

// hostKeyCallback trusts a host on first use and rejects a changed key, like OpenSSH's accept-new.
// ponytail: unknown hosts are accepted silently; electerm asks via a confirm prompt first.
func hostKeyCallback() (ssh.HostKeyCallback, error) {
	file := filepath.Join(dataDir(), "known_hosts")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	check, err := knownhosts.New(file)
	if err != nil {
		return nil, err
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := check(host, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(host)}, key))
			return err
		}
		if errors.As(err, &ke) {
			return fmt.Errorf("host key for %s changed (possible MITM); remove its line from %s to trust the new key", host, file)
		}
		return err
	}, nil
}

// defaultKeys loads unencrypted keys from ~/.ssh, which electerm tries when a bookmark has no credentials.
func defaultKeys() []ssh.Signer {
	home, _ := os.UserHomeDir()
	var out []ssh.Signer
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		if pem, err := os.ReadFile(filepath.Join(home, ".ssh", n)); err == nil {
			if s, err := ssh.ParsePrivateKey(pem); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

func sshClient(o opts, ask asker) (*ssh.Client, error) {
	password := o.str("password")
	var auth []ssh.AuthMethod
	if key := o.str("privateKey"); key != "" {
		var signer ssh.Signer
		var err error
		if pass := o.str("passphrase"); pass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(key))
		}
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	} else if password == "" {
		if keys := defaultKeys(); len(keys) > 0 {
			auth = append(auth, ssh.PublicKeys(keys...))
		}
	}
	if password != "" {
		auth = append(auth, ssh.Password(password))
	} else {
		auth = append(auth, ssh.PasswordCallback(func() (string, error) {
			a, err := ask("", "", []string{"password:"}, []bool{false})
			if err != nil || len(a) == 0 {
				return "", errors.New("user cancel")
			}
			return a[0], nil
		}))
	}
	auth = append(auth, ssh.KeyboardInteractive(func(name, instruction string, qs []string, echos []bool) ([]string, error) {
		if len(qs) == 0 {
			return nil, nil
		}
		if len(qs) == 1 && !echos[0] && password != "" && strings.Contains(strings.ToLower(qs[0]), "password") {
			return []string{password}, nil
		}
		return ask(name, instruction, qs, echos)
	}))
	hk, err := hostKeyCallback()
	if err != nil {
		return nil, err
	}
	port := o.num("port", 22)
	return ssh.Dial("tcp", net.JoinHostPort(o.str("host"), strconv.Itoa(port)), &ssh.ClientConfig{
		User:            o.str("username"),
		Auth:            auth,
		HostKeyCallback: hk,
		Timeout:         time.Duration(o.num("readyTimeout", 50000)) * time.Millisecond,
	})
}

func startSSH(o opts, ask asker) (*Term, error) {
	client, err := sshClient(o, ask)
	if err != nil {
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	in, _ := sess.StdinPipe()
	out, _ := sess.StdoutPipe()
	sess.Stderr = sess.Stdout
	term := o.str("term")
	if term == "" {
		term = "xterm-256color"
	}
	if err := sess.RequestPty(term, o.num("rows", 24), o.num("cols", 80), ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		client.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		client.Close()
		return nil, err
	}
	if iv := o.num("keepaliveInterval", 0); iv > 0 {
		go func() {
			for range time.Tick(time.Duration(iv) * time.Millisecond) {
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					return
				}
			}
		}()
	}
	return &Term{
		out:    out,
		in:     in,
		client: client,
		resize: func(c, r int) error { return sess.WindowChange(r, c) },
		close:  client.Close,
	}, nil
}

func noResize(int, int) error { return nil }

// lineEndingWriter turns the Enter key's \r into the session's txLineEnding (electerm line-ending.js).
type lineEndingWriter struct {
	w   io.Writer
	end string
}

func (l lineEndingWriter) Write(p []byte) (int, error) {
	if l.end == "" || l.end == "\r" {
		return l.w.Write(p)
	}
	_, err := l.w.Write([]byte(strings.ReplaceAll(string(p), "\r", l.end)))
	return len(p), err
}

func lineEnding(o opts) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\n").Replace(o.str("txLineEnding"))
}

func startTelnet(o opts) (*Term, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(o.str("host"), strconv.Itoa(o.num("port", 23))),
		time.Duration(o.num("readyTimeout", 50000))*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &Term{
		out:    &telnetReader{bufio.NewReader(c), c},
		in:     lineEndingWriter{telnetWriter{c}, lineEnding(o)},
		resize: noResize,
		close:  c.Close,
	}, nil
}

func startSerial(o opts) (*Term, error) {
	mode := &serial.Mode{BaudRate: o.num("baudRate", 9600), DataBits: o.num("dataBits", 8)}
	switch o.num("stopBits", 1) {
	case 2:
		mode.StopBits = serial.TwoStopBits
	default:
		mode.StopBits = serial.OneStopBit
	}
	mode.Parity = map[string]serial.Parity{
		"even": serial.EvenParity, "odd": serial.OddParity, "mark": serial.MarkParity, "space": serial.SpaceParity,
	}[o.str("parity")]
	p, err := serial.Open(o.str("path"), mode)
	if err != nil {
		return nil, err
	}
	return &Term{out: p, in: lineEndingWriter{p, lineEnding(o)}, resize: noResize, close: p.Close}, nil
}

func listSerialPorts() []map[string]any {
	ports, _ := serial.GetPortsList()
	out := make([]map[string]any, 0, len(ports))
	for _, p := range ports {
		out = append(out, map[string]any{"path": p})
	}
	return out
}
