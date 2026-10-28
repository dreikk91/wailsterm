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
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
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
					if nch.ChannelType() == "direct-tcpip" {
						var d struct {
							Host  string
							Port  uint32
							OHost string
							OPort uint32
						}
						ssh.Unmarshal(nch.ExtraData(), &d)
						tc, err := net.Dial("tcp", net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
						if err != nil {
							nch.Reject(ssh.ConnectionFailed, err.Error())
							continue
						}
						ch, reqs, _ := nch.Accept()
						go ssh.DiscardRequests(reqs)
						go pipeBoth(ch, tc)
						continue
					}
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
	t.Setenv("WAILSTERM_KNOWN_HOSTS", filepath.Join(t.TempDir(), "known_hosts"))
	host, port := testSSHServer(t)
	asked := 0
	trust := func(o map[string]any) ([]string, error) {
		asked++
		if o["mode"] != "confirm" {
			t.Fatalf("unexpected prompt %v", o)
		}
		return []string{"trust"}, nil
	}

	o := opts{"termType": "ssh", "type": "remote", "host": host, "port": float64(port),
		"username": "user", "password": "pass", "cols": float64(80), "rows": float64(24)}
	term, err := startSession(o, trust, nil)
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
	if r, err := s.execCmd("t1", "true", time.Second); err != nil || r["stdout"] != "exec-ok" || r["exitCode"] != 0 {
		t.Fatalf("execCmd = %v, %v", r, err)
	}

	// SFTP functions in electerm's shapes
	sess := &sftpSess{terminalID: "t1", kind: "sftp"}
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

	// the unknown host key was confirmed once and pinned
	kh, _ := os.ReadFile(knownHostsPath())
	if asked != 1 || !strings.Contains(string(kh), "ssh-ed25519") {
		t.Fatalf("asked %d times, known_hosts %q", asked, kh)
	}
	// a second connection is silent; a rejected prompt for a new host fails
	if t2, err := startSession(o, trust, nil); err != nil || asked != 1 {
		t.Fatalf("reconnect: asked %d, err %v", asked, err)
	} else {
		t2.Close()
	}
	os.Remove(knownHostsPath())
	reject := func(map[string]any) ([]string, error) { return nil, nil }
	if _, err := startSession(o, reject, nil); err == nil {
		t.Fatal("rejected host key still connected")
	}
}

func TestJumpHostAndTunnels(t *testing.T) {
	t.Setenv("WAILSTERM_KNOWN_HOSTS", filepath.Join(t.TempDir(), "known_hosts"))
	host, port := testSSHServer(t)
	trust := func(map[string]any) ([]string, error) { return []string{"trust"}, nil }

	// an echo service only reachable "behind" the SSH server
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c)
		}
	}()
	_, echoPort, _ := net.SplitHostPort(echo.Addr().String())
	free := func() int {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	localPort, socksPort := free(), free()

	hop := map[string]any{"host": host, "port": float64(port), "username": "user", "password": "pass"}
	o := opts{"termType": "ssh", "host": host, "port": float64(port), "username": "user", "password": "pass",
		"hasHopping": true, "connectionHoppings": []any{hop},
		"sshTunnels": []any{
			map[string]any{"sshTunnel": "forwardLocalToRemote", "sshTunnelLocalPort": float64(localPort), "sshTunnelRemotePort": echoPort},
			map[string]any{"sshTunnel": "dynamicForward", "sshTunnelLocalPort": float64(socksPort)},
		}}
	var notified map[string]any
	term, err := startSession(o, trust, func(m map[string]any) { notified = m })
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	results := notified["update"].(map[string]any)["sshTunnelResults"].([]any)
	for _, r := range results {
		if e := r.(map[string]any)["error"]; e != nil {
			t.Fatalf("tunnel error: %v", e)
		}
	}

	roundTrip := func(c net.Conn, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Write([]byte("hi"))
		b := make([]byte, 2)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c, b); err != nil || string(b) != "hi" {
			t.Fatalf("echo through tunnel = %q, %v", b, err)
		}
	}
	roundTrip(net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(localPort)))

	d, err := proxy.SOCKS5("tcp", "127.0.0.1:"+strconv.Itoa(socksPort), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(d.Dial("tcp", echo.Addr().String()))
}

func TestLoadSSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(`Host *
  User nobody
Host web
  HostName 10.0.0.5
  Port 2200
  User deploy
  IdentityFile ~/.ssh/id_web
`), 0o600)
	got := loadSSHConfig()
	if len(got) != 1 || got[0]["host"] != "10.0.0.5" || got[0]["port"] != 2200 || got[0]["username"] != "deploy" ||
		got[0]["privateKeyPath"] != filepath.Join(home, ".ssh", "id_web") {
		t.Fatalf("loadSSHConfig = %v", got)
	}
}
