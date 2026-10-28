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

// vncTarget is a VNC tab prepared by create-terminal; /vnc/:pid bridges noVNC to it (session-vnc.js).
type vncTarget struct {
	o      opts
	client *ssh.Client // last SSH jump host, when the bookmark uses connection hopping
	jumps  []*ssh.Client
}

var (
	vncMu      sync.Mutex
	vncTargets = map[string]*vncTarget{}
)

// prepareVNC connects the bookmark's SSH jump hosts (if any) now, while host key and
// password prompts can still reach the tab; the VNC TCP connection itself is opened per socket.
func prepareVNC(o opts, ask interactor) (*vncTarget, error) {
	t := &vncTarget{o: o}
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

func (t *vncTarget) dial() (net.Conn, error) {
	addr := net.JoinHostPort(t.o.str("host"), strconv.Itoa(t.o.num("port", 5900)))
	if t.client != nil {
		return t.client.Dial("tcp", addr)
	}
	return dialVia(t.o.str("proxy"), addr, time.Duration(t.o.num("readyTimeout", 50000))*time.Millisecond)
}

func (t *vncTarget) Close() {
	if t.client != nil {
		t.client.Close()
	}
	for i := len(t.jumps) - 1; i >= 0; i-- {
		t.jumps[i].Close()
	}
}

func (s *Server) handleVNC(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	vncMu.Lock()
	t := vncTargets[pid]
	vncMu.Unlock()
	if t == nil {
		http.Error(w, "no such vnc session", http.StatusNotFound)
		return
	}
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	defer func() {
		vncMu.Lock()
		if vncTargets[pid] == t {
			delete(vncTargets, pid)
		}
		vncMu.Unlock()
		t.Close()
	}()
	conn, err := t.dial()
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
