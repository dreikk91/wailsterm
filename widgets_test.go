package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestWidgetFileServer(t *testing.T) {
	t.Setenv("WAILSTERM_DATA", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("served"), 0o644)
	os.WriteFile(filepath.Join(dir, ".secret"), []byte("hidden"), 0o644)
	var r widgetRuntime
	port := freePort(t)
	res, err := r.run("local-file-server", map[string]any{"port": float64(port), "directory": dir, "dotfiles": "deny"})
	if err != nil {
		t.Fatal(err)
	}
	inst := asMap(res)
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	if asMap(inst["serverInfo"])["url"] != base || inst["success"] != true {
		t.Fatalf("run = %v", inst)
	}
	get := func(p string) (int, string) {
		resp, err := http.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/a.txt"); code != 200 || body != "served" {
		t.Fatalf("GET a.txt = %d %q", code, body)
	}
	if code, _ := get("/.secret"); code != 403 {
		t.Fatalf("dotfile deny = %d", code)
	}
	id := inst["instanceId"].(string)
	if st, _ := r.stop(id); asMap(st)["status"] != "stopped" {
		t.Fatalf("stop = %v", st)
	}
	logText, _ := os.ReadFile(filepath.Join(widgetLogDir(), id+".log"))
	lines := strings.Split(strings.TrimSpace(string(logText)), "\n")
	var conn string
	for _, l := range lines {
		if strings.Contains(l, "|conn|ok|GET|") {
			conn = l
		}
	}
	// instance-log.js line: <iso>|conn|<ok|fail|info>|<type>|<from>|<msg>
	if f := strings.Split(conn, "|"); len(f) != 6 || f[4] != "127.0.0.1" || !strings.HasPrefix(f[5], "/a.txt -> 200") {
		t.Fatalf("log lines = %q", lines)
	}
}

func TestWidgetFTPServer(t *testing.T) {
	t.Setenv("WAILSTERM_DATA", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("via ftp"), 0o644)
	var r widgetRuntime
	port := freePort(t)
	res, err := r.run("local-ftp-server", map[string]any{"host": "127.0.0.1", "port": float64(port), "directory": dir})
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop(asMap(res)["instanceId"].(string))
	o := opts{"host": "127.0.0.1", "port": float64(port), "user": "ftpuser", "password": "ftppass"}
	f, err := dialFTP(o)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rd, err := f.Open("/shared.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rd)
	rd.Close()
	if string(b) != "via ftp" {
		t.Fatalf("ftp read = %q", b)
	}
	o["password"] = "nope"
	if _, err := dialFTP(o); err == nil {
		t.Fatal("bad password accepted")
	}
}

func TestWidgetSSHServer(t *testing.T) {
	t.Setenv("WAILSTERM_DATA", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "inside.txt"), []byte("ok"), 0o644)
	var r widgetRuntime
	port := freePort(t)
	res, err := r.run("ssh-server", map[string]any{"port": float64(port), "directory": dir})
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop(asMap(res)["instanceId"].(string))
	c, err := ssh.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port), &ssh.ClientConfig{
		User: "test", Auth: []ssh.AuthMethod{ssh.Password("test")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sess, _ := c.NewSession()
	out, err := sess.Output("echo hi")
	if err != nil || !strings.Contains(string(out), "hi") {
		t.Fatalf("exec = %q, %v", out, err)
	}
	sc, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	// "/" is the widget directory, and ".." cannot climb out of it
	for _, p := range []string{"/", "/../../.."} {
		infos, err := sc.ReadDir(p)
		if err != nil || len(infos) != 1 || infos[0].Name() != "inside.txt" {
			t.Fatalf("ReadDir(%q) = %v, %v", p, infos, err)
		}
	}
	w, _ := sc.Create("/new.txt")
	w.Write([]byte("uploaded"))
	w.Close()
	if b, _ := os.ReadFile(filepath.Join(dir, "new.txt")); string(b) != "uploaded" {
		t.Fatalf("sftp write = %q", b)
	}
}

func TestWidgetRenameAndList(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.jpg", "a.jpg", "c.txt"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	var r widgetRuntime
	res, err := r.run("rename", map[string]any{"directory": dir, "template": "photo-{n:3}.{ext}", "fileTypes": "jpg"})
	if err != nil || asMap(res)["totalRenamed"] != 2 {
		t.Fatalf("rename = %v, %v", res, err)
	}
	for _, n := range []string{"photo-001.jpg", "photo-002.jpg", "c.txt"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("missing %s", n)
		}
	}
	if res, _ := r.run("rename", map[string]any{"directory": dir, "template": "../x"}); asMap(res)["success"] != false {
		t.Fatal("path separator in template accepted")
	}
	ids := map[string]bool{}
	for _, w := range r.list() {
		ids[w["id"].(string)] = true
	}
	for _, id := range []string{"local-file-server", "local-ftp-server", "ssh-server", "rename", "batch-op"} {
		if !ids[id] {
			t.Fatalf("listWidgets missing %s: %v", id, ids)
		}
	}
}
