package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
)

type memFTP struct{ fs afero.Fs }

func (d memFTP) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{ListenAddr: "127.0.0.1:0"}, nil
}
func (memFTP) ClientConnected(ftpserver.ClientContext) (string, error) { return "test", nil }
func (memFTP) ClientDisconnected(ftpserver.ClientContext)              {}
func (d memFTP) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	if user != "user" || pass != "pass" {
		return nil, errors.New("bad login")
	}
	return d.fs, nil
}
func (memFTP) GetTLSConfig() (*tls.Config, error) { return nil, errors.New("no tls") }

func TestFTP(t *testing.T) {
	srv := ftpserver.NewFtpServer(memFTP{afero.NewMemMapFs()})
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Stop()
	host, port, _ := net.SplitHostPort(srv.Addr())
	pn, _ := strconv.Atoi(port)
	o := map[string]any{"host": host, "port": float64(pn), "user": "user", "password": "pass"}

	s := &Server{terms: map[string]*Term{}}
	sess := &sftpSess{kind: "ftp"}
	if r, err := s.sftpFunc(sess, "connect", []any{o}); err != nil || r != "ok" {
		t.Fatalf("connect = %v, %v", r, err)
	}
	defer sess.closer.Close()
	call := func(fn string, args ...any) any {
		t.Helper()
		r, err := s.sftpFunc(sess, fn, args)
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		return r
	}
	call("mkdir", "/a/b")
	call("writeFile", "/a/b/f.txt", "hello ftp")
	if got := call("readFile", "/a/b/f.txt"); got != "hello ftp" {
		t.Fatalf("readFile = %v", got)
	}
	if st := call("stat", "/a/b").(map[string]any); st["isDirectory"] != true {
		t.Fatalf("stat dir = %v", st)
	}
	call("rename", "/a/b/f.txt", "/a/g.txt")
	call("cp", "/a/g.txt", "/a/h.txt")
	names := map[string]string{}
	for _, e := range call("list", "/a").([]map[string]any) {
		names[e["name"].(string)] = e["type"].(string)
	}
	if names["b"] != "d" || names["g.txt"] != "-" || names["h.txt"] != "-" {
		t.Fatalf("list = %v", names)
	}

	// transfers use their own connection
	sftpMu.Lock()
	sftps["ftp-test"] = sess
	sftpMu.Unlock()
	defer func() { sftpMu.Lock(); delete(sftps, "ftp-test"); sftpMu.Unlock() }()
	remote, closer, err := transferFS("ftp-test")
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	local := t.TempDir()
	p := &progress{ctx: context.Background(), emit: func(bool) {}}
	if err := copyTree(remote, localFS{}, "/a", filepath.Join(local, "a"), p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(local, "a", "h.txt")); string(b) != "hello ftp" {
		t.Fatalf("downloaded = %q", b)
	}
	os.WriteFile(filepath.Join(local, "up.txt"), []byte("up"), 0o644)
	if err := copyTree(localFS{}, remote, filepath.Join(local, "up.txt"), "/a/up.txt", p); err != nil {
		t.Fatal(err)
	}
	if got := call("readFile", "/a/up.txt"); got != "up" {
		t.Fatalf("uploaded = %v", got)
	}
	call("rmdir", "/a")
	if _, err := s.sftpFunc(sess, "stat", []any{"/a"}); err == nil {
		t.Fatal("rmdir left /a")
	}
}
