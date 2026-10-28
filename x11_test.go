package main

import (
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestX11Forwarding(t *testing.T) {
	x, err := net.Listen("tcp", "127.0.0.1:0") // the local X server, display :<port-6000>
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	go func() {
		for {
			c, err := x.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b := make([]byte, 4)
				if _, err := io.ReadFull(c, b); err == nil && string(b) == "ping" {
					c.Write([]byte("pong"))
				}
			}()
		}
	}()
	_, xp, _ := net.SplitHostPort(x.Addr().String())
	n, _ := strconv.Atoi(xp)
	t.Setenv("DISPLAY", "localhost:"+strconv.Itoa(n-6000)+".0")

	t.Setenv("WAILSTERM_KNOWN_HOSTS", filepath.Join(t.TempDir(), "known_hosts"))
	host, port := testSSHServer(t)
	trust := func(map[string]any) ([]string, error) { return []string{"trust"}, nil }
	var warnings []string
	term, err := startSSH(opts{"host": host, "port": float64(port), "username": "user", "password": "pass", "x11": true},
		trust, func(m map[string]any) {
			if m["action"] == "x11-warning" {
				warnings = append(warnings, m["message"].(string))
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	got := make(chan string, 1)
	go func() {
		var all []byte
		b := make([]byte, 64)
		for !strings.Contains(string(all), "x11-ok") {
			n, err := term.out.(io.Reader).Read(b)
			if err != nil {
				break
			}
			all = append(all, b[:n]...)
		}
		got <- string(all)
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, "x11-ok") {
			t.Fatalf("shell = %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("x11 channel was not forwarded")
	}
	if len(warnings) > 0 && strings.Contains(warnings[0], "no local X server") {
		t.Fatalf("warned although X is up: %v", warnings)
	}
}

func TestX11Candidates(t *testing.T) {
	var tcp []string
	for _, c := range x11Candidates("localhost:2.0") {
		if c[0] == "tcp" {
			tcp = append(tcp, c[1])
		}
	}
	if strings.Join(tcp, " ") != "127.0.0.1:6002 127.0.0.1:6000" {
		t.Fatalf("tcp candidates = %v", tcp)
	}
	if c := x11Candidates("/private/tmp/xquartz:0"); len(c) != 1 || c[0][0] != "unix" {
		t.Fatalf("xquartz = %v", c)
	}
}
