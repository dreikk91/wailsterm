package main

import (
	"bufio"
	"io"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestSocks4aDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		head := make([]byte, 8)
		io.ReadFull(br, head)
		user, _ := br.ReadString(0)
		host, _ := br.ReadString(0)
		got <- string(head[:4]) + "|" + user + "|" + host
		c.Write([]byte{0, 0x5a, 0, 0, 0, 0, 0, 0})
		c.Write([]byte("hi"))
	}()
	c, err := dialVia("socks4a://bob@"+ln.Addr().String(), "example.com:22", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if g := <-got; g != "\x04\x01\x00\x16|bob\x00|example.com\x00" {
		t.Fatalf("request %q", g)
	}
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "hi" {
		t.Fatalf("payload %q %v", b, err)
	}
	if _, err := socks4Dial(&url.URL{Host: "127.0.0.1:1"}, "1.2.3.4:80", time.Second); err == nil {
		t.Fatal("expected dial error")
	}
}
