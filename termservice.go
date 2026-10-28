package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/aymanbagabas/go-pty"
	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Bookmark struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"` // ponytail: plaintext in config dir like electerm's default; use OS keyring if needed
	KeyPath  string `json:"keyPath"`
}

type session struct {
	w      io.Writer
	resize func(cols, rows int) error
	close  func() error
}

type TermService struct {
	mu       sync.Mutex
	sessions map[string]*session
	next     atomic.Int64
}

func NewTermService() *TermService { return &TermService{sessions: map[string]*session{}} }

func configDir() string {
	d, _ := os.UserConfigDir()
	d = filepath.Join(d, "wailsterm")
	os.MkdirAll(d, 0o700)
	return d
}

// pump streams output to the frontend as base64 so multibyte runes split across reads survive.
func (t *TermService) pump(id string, r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			application.Get().Event.Emit("term:data", map[string]string{"id": id, "data": base64.StdEncoding.EncodeToString(buf[:n])})
		}
		if err != nil {
			break
		}
	}
	t.Close(id)
	application.Get().Event.Emit("term:exit", id)
}

func (t *TermService) add(s *session) string {
	id := strconv.FormatInt(t.next.Add(1), 10)
	t.mu.Lock()
	t.sessions[id] = s
	t.mu.Unlock()
	return id
}

func (t *TermService) get(id string) *session {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[id]
}

func (t *TermService) OpenLocal(cols, rows int) (string, error) {
	p, err := pty.New()
	if err != nil {
		return "", err
	}
	shell := os.Getenv("SHELL")
	if runtime.GOOS == "windows" {
		shell = "powershell.exe"
	} else if shell == "" {
		shell = "/bin/sh"
	}
	p.Resize(cols, rows)
	cmd := p.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if err := cmd.Start(); err != nil {
		p.Close()
		return "", err
	}
	go cmd.Wait()
	id := t.add(&session{w: p, resize: p.Resize, close: p.Close})
	go t.pump(id, p)
	return id, nil
}

// hostKeyCallback trusts on first use and rejects changed keys, like OpenSSH's accept-new.
func hostKeyCallback() (ssh.HostKeyCallback, error) {
	file := filepath.Join(configDir(), "known_hosts")
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
		return err
	}, nil
}

func (t *TermService) OpenSSH(b Bookmark, cols, rows int) (string, error) {
	var auth []ssh.AuthMethod
	if b.KeyPath != "" {
		pem, err := os.ReadFile(b.KeyPath)
		if err != nil {
			return "", err
		}
		var signer ssh.Signer
		if b.Password != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(b.Password))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return "", err
		}
		auth = append(auth, ssh.PublicKeys(signer))
	} else {
		auth = append(auth, ssh.Password(b.Password), ssh.KeyboardInteractive(
			func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = b.Password
				}
				return ans, nil
			}))
	}
	hk, err := hostKeyCallback()
	if err != nil {
		return "", err
	}
	if b.Port == 0 {
		b.Port = 22
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(b.Host, strconv.Itoa(b.Port)), &ssh.ClientConfig{
		User: b.User, Auth: auth, HostKeyCallback: hk,
	})
	if err != nil {
		return "", err
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return "", err
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	sess.Stderr = sess.Stdout
	if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		client.Close()
		return "", err
	}
	if err := sess.Shell(); err != nil {
		client.Close()
		return "", err
	}
	id := t.add(&session{
		w:      stdin,
		resize: func(c, r int) error { return sess.WindowChange(r, c) },
		close:  client.Close,
	})
	go t.pump(id, stdout)
	return id, nil
}

func (t *TermService) Write(id, data string) error {
	s := t.get(id)
	if s == nil {
		return errors.New("no session " + id)
	}
	_, err := io.WriteString(s.w, data)
	return err
}

func (t *TermService) Resize(id string, cols, rows int) error {
	if s := t.get(id); s != nil {
		return s.resize(cols, rows)
	}
	return nil
}

func (t *TermService) Close(id string) {
	t.mu.Lock()
	s := t.sessions[id]
	delete(t.sessions, id)
	t.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func bookmarksFile() string { return filepath.Join(configDir(), "bookmarks.json") }

func (t *TermService) Bookmarks() ([]Bookmark, error) {
	data, err := os.ReadFile(bookmarksFile())
	if errors.Is(err, os.ErrNotExist) {
		return []Bookmark{}, nil
	}
	if err != nil {
		return nil, err
	}
	var bs []Bookmark
	return bs, json.Unmarshal(data, &bs)
}

func (t *TermService) SaveBookmarks(bs []Bookmark) error {
	data, err := json.MarshalIndent(bs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(bookmarksFile(), data, 0o600)
}
