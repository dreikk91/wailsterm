package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// hopTarget is a VNC or RDP tab prepared by create-terminal: where to connect and, when the
// bookmark uses connection hopping, the SSH jump client to connect through (session-hop.js).
type hopTarget struct {
	o      opts
	client *ssh.Client // last SSH jump host, when the bookmark uses connection hopping
	jumps  []*ssh.Client
}

var (
	hopMu      sync.Mutex
	hopTargets = map[string]*hopTarget{}
)

// prepareHops connects the bookmark's SSH jump hosts (if any) now, while host key and
// password prompts can still reach the tab; the VNC TCP connection itself is opened per socket.
func prepareHops(o opts, ask interactor) (*hopTarget, error) {
	t := &hopTarget{o: o}
	hops := asSlice(o["connectionHoppings"])
	if o["hasHopping"] != true || len(hops) == 0 {
		return t, nil
	}
	last := hopOpts(hops[len(hops)-1])
	via := opts{}
	for k, v := range last {
		via[k] = v
	}
	via["hasHopping"] = len(hops) > 1
	via["connectionHoppings"] = hops[:len(hops)-1]
	via["proxy"], via["readyTimeout"] = o["proxy"], o["readyTimeout"]
	via["useSshAgent"], via["sshAgent"] = o["useSshAgent"], o["sshAgent"]
	client, jumps, err := dialSSH(via, ask)
	if err != nil {
		return nil, err
	}
	t.client, t.jumps = client, jumps
	return t, nil
}

func (t *hopTarget) dial(defaultPort int) (net.Conn, error) {
	return t.dialAddr(net.JoinHostPort(t.o.str("host"), strconv.Itoa(t.o.num("port", defaultPort))))
}

func (t *hopTarget) dialAddr(addr string) (net.Conn, error) {
	if t.client != nil {
		return t.client.Dial("tcp", addr)
	}
	return dialVia(t.o.str("proxy"), addr, time.Duration(t.o.num("readyTimeout", 50000))*time.Millisecond)
}

func (t *hopTarget) Close() {
	if t.client != nil {
		t.client.Close()
	}
	for i := len(t.jumps) - 1; i >= 0; i-- {
		t.jumps[i].Close()
	}
}

// takeTarget accepts the socket for a prepared tab; the target is released when done() runs.
func takeTarget(w http.ResponseWriter, r *http.Request) (*hopTarget, *wsConn, func()) {
	pid := r.PathValue("pid")
	hopMu.Lock()
	t := hopTargets[pid]
	hopMu.Unlock()
	if t == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return nil, nil, nil
	}
	ws, err := accept(w, r)
	if err != nil {
		return nil, nil, nil
	}
	return t, ws, func() {
		hopMu.Lock()
		if hopTargets[pid] == t {
			delete(hopTargets, pid)
		}
		hopMu.Unlock()
		t.Close()
	}
}

// handleVNC is session-vnc.js: noVNC's socket bridged to the server's TCP port.
func (s *Server) handleVNC(w http.ResponseWriter, r *http.Request) {
	t, ws, done := takeTarget(w, r)
	if t == nil {
		return
	}
	defer done()
	conn, err := t.dial(5900)
	if err != nil {
		ws.c.Close(websocket.StatusInternalError, truncate(err.Error(), 120))
		return
	}
	defer conn.Close()
	bridge(r.Context(), ws.c, conn)
}

// bridge pipes binary WebSocket frames to a TCP connection and back until either side ends.
func bridge(ctx context.Context, c *websocket.Conn, conn net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		buf := make([]byte, 64*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 && c.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			break
		}
		if _, err := conn.Write(b); err != nil {
			break
		}
	}
	c.Close(websocket.StatusNormalClosure, "")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
