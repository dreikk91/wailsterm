package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestVNCBridge(t *testing.T) {
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
	_, ep, _ := net.SplitHostPort(echo.Addr().String())
	epn, _ := strconv.Atoi(ep)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	s := &Server{token: "tok", addr: ln.Addr().String(), terms: map[string]*Term{}}
	go http.Serve(ln, s.routes())
	defer ln.Close()

	tgt, err := prepareVNC(opts{"host": "127.0.0.1", "port": float64(epn)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	vncMu.Lock()
	vncTargets["v1"] = tgt
	vncMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := "ws://" + s.addr + "/vnc/v1?token="
	if _, _, err := websocket.Dial(ctx, base+"wrong", nil); err == nil {
		t.Fatal("bad token accepted")
	}
	c, _, err := websocket.Dial(ctx, base+"tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n"))
	_, b, err := c.Read(ctx)
	if err != nil || string(b) != "RFB 003.008\n" {
		t.Fatalf("bridge echo = %q, %v", b, err)
	}
}
