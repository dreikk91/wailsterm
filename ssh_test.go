package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testSSHServer accepts user/pass, echoes the shell, and serves real SFTP on the host filesystem.
func testSSHServer(t *testing.T) (host string, port int) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if c.User() == "user" && string(pass) == "pass" {
			return nil, nil
		}
		return nil, io.EOF
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, reqs, _ := nch.Accept()
					go func() {
						for req := range reqs {
							switch req.Type {
							case "subsystem":
								req.Reply(true, nil)
								go func() { srv, _ := sftp.NewServer(ch); srv.Serve(); ch.Close() }()
							case "shell":
								req.Reply(true, nil)
								go io.Copy(ch, ch)
							case "exec":
								req.Reply(true, nil)
								ch.Write([]byte("exec-ok"))
								ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{0}))
								ch.Close()
							default:
								req.Reply(req.Type == "pty-req", nil)
							}
						}
					}()
				}
			}()
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(p)
	return h, port
}

func TestSSHSessionAndSftp(t *testing.T) {
	t.Setenv("WAILSTERM_DATA", t.TempDir())
	host, port := testSSHServer(t)
	noAsk := func(string, string, []string, []bool) ([]string, error) { return nil, io.EOF }

	o := opts{"termType": "ssh", "type": "remote", "host": host, "port": float64(port),
		"username": "user", "password": "pass", "cols": float64(80), "rows": float64(24)}
	term, err := startSession(o, noAsk)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	// shell: input comes back through the echo server
	term.in.Write([]byte("ping"))
	buf := make([]byte, 4)
	term.out.(io.Reader).Read(buf)
	if string(buf) != "ping" {
		t.Fatalf("shell echo = %q", buf)
	}

	// run-cmd over a separate exec channel
	s := &Server{terms: map[string]*Term{"t1": term}}
	if out, err := s.runCmd("t1", "true"); err != nil || out != "exec-ok" {
		t.Fatalf("runCmd = %q, %v", out, err)
	}

	// SFTP functions in electerm's shapes
	sess := &sftpSess{terminalID: "t1"}
	if r, err := s.sftpFunc(sess, "connect", nil); err != nil || r != "ok" {
		t.Fatalf("connect = %v, %v", r, err)
	}
	dir := filepath.ToSlash(t.TempDir())
	if _, err := s.sftpFunc(sess, "writeFile", []any{dir + "/a.txt", "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sftpFunc(sess, "mkdir", []any{dir + "/sub"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.sftpFunc(sess, "list", []any{dir})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, e := range list.([]map[string]any) {
		types[e["name"].(string)] = e["type"].(string)
	}
	if types["a.txt"] != "-" || types["sub"] != "d" {
		t.Fatalf("list types = %v", types)
	}
	if got, _ := s.sftpFunc(sess, "readFile", []any{dir + "/a.txt"}); got != "hello" {
		t.Fatalf("readFile = %v", got)
	}

	// download through the transfer copier
	local := t.TempDir()
	p := &progress{ctx: t.Context(), emit: func(bool) {}}
	if err := copyTree(remoteFS{sess.c}, localFS{}, dir, filepath.Join(local, "got"), p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(local, "got", "a.txt")); string(b) != "hello" {
		t.Fatalf("downloaded = %q", b)
	}

	// host key was pinned on first use
	kh, _ := os.ReadFile(filepath.Join(dataDir(), "known_hosts"))
	if !strings.Contains(string(kh), "ssh-ed25519") {
		t.Fatal("host key not recorded")
	}
}
