package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestTerminalSocket(t *testing.T) {
	// a fake session: whatever is typed comes back as output
	outR, outW := io.Pipe()
	term := &Term{pid: "p1", out: outR, in: outW, resize: noResize, close: outW.Close}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	s := &Server{token: "tok", addr: ln.Addr().String(), terms: map[string]*Term{"p1": term}}
	go http.Serve(ln, s.routes())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+s.addr+"/terminals/p1?token=tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	// control frames are swallowed, keystrokes reach the session and its output comes back
	c.Write(ctx, websocket.MessageText, []byte(`{"action":"keepalive"}`))
	c.Write(ctx, websocket.MessageText, []byte("ls -la\r"))
	got := ""
	for len(got) < len("ls -la\r") {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read after %q: %v", got, err)
		}
		got += string(b)
	}
	if got != "ls -la\r" {
		t.Fatalf("echo = %q", got)
	}

	// session end closes the socket
	outW.Close()
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("socket still open after session ended")
	}
}
