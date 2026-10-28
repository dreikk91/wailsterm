package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/trzsz/trzsz-go/trzsz"
	"golang.org/x/crypto/ssh"
)

//go:embed all:ui/dist
var uiDist embed.FS

//go:embed ui/package.json
var uiPackage []byte

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// dataDir holds the DB and known_hosts; WAILSTERM_DATA overrides it (tests, portable installs).
func dataDir() string {
	d := os.Getenv("WAILSTERM_DATA")
	if d == "" {
		d, _ = os.UserConfigDir()
		d = filepath.Join(d, "wailsterm")
	}
	os.MkdirAll(d, 0o700)
	return d
}

// Server replaces electerm's Node side: the express server (src/app/server/server.js,
// dispatch-center.js), the per-session servers (session-server.js) and the Electron
// IPC handlers (lib/ipc.js, lib/ipc-sync.js). The UI is served from the same origin.
type Server struct {
	token   string
	addr    string
	version string
	db      *DB

	mu    sync.Mutex
	terms map[string]*Term

	subMu sync.Mutex
	subs  map[chan []byte]bool

	watch fileWatch
}

func NewServer() (*Server, error) {
	db, err := NewDB(filepath.Join(dataDir(), "db"))
	if err != nil {
		return nil, err
	}
	var pkg struct{ Version string }
	json.Unmarshal(uiPackage, &pkg)
	if err := seedDB(db, pkg.Version); err != nil {
		return nil, fmt.Errorf("seed database: %w", err)
	}
	return &Server{
		token:   newID() + newID(),
		version: pkg.Version,
		db:      db,
		terms:   map[string]*Term{},
		subs:    map[chan []byte]bool{},
	}, nil
}

// Start listens on a random loopback port (WAILSTERM_ADDR overrides it for development)
// and returns the URL the window should load.
func (s *Server) Start() (string, error) {
	addr := os.Getenv("WAILSTERM_ADDR")
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.addr = ln.Addr().String()
	go http.Serve(ln, s.routes())
	return "http://" + s.addr + "/", nil
}

func (s *Server) routes() http.Handler {
	static, _ := fs.Sub(uiDist, "ui/dist")
	files := http.FileServerFS(static)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /", files)
	mux.HandleFunc("POST /api/sync", s.authed(s.handleSync))
	mux.HandleFunc("POST /api/async", s.authed(s.handleAsync))
	mux.HandleFunc("GET /api/events", s.authed(s.handleEvents))
	mux.HandleFunc("POST /api/http", s.authed(s.handleHTTP))
	mux.HandleFunc("GET /common/s", s.authed(s.handleCommon))
	mux.HandleFunc("GET /terminals/{pid}", s.authed(s.handleTerminal))
	mux.HandleFunc("GET /sftp/{id}", s.authed(s.handleSftp))
	mux.HandleFunc("GET /transfer/{id}", s.authed(s.handleTransfer))
	mux.HandleFunc("GET /vnc/{pid}", s.authed(s.handleVNC))
	mux.HandleFunc("GET /rdp/{pid}", s.authed(s.handleRDP))
	// Requests must name our exact host:port, which defeats DNS rebinding from web pages.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// authed requires the per-run token (header for fetch/XHR, query for WebSocket/EventSource).
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != s.token && r.URL.Query().Get("token") != s.token {
			http.Error(w, "not valid request", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// index replaces src/client/views/index.pug (production branch).
var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html>
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1, shrink-to-fit=no">
<title>electerm</title>
<style>
body { background: #000; }
#content-loading { position: fixed; left: 0; top: 0; width: 100%; height: 100%; display: flex; flex-direction: column; justify-content: center; align-items: center; }
.morph-shape { background: linear-gradient(45deg, #08c 0%, #09c 100%); border-radius: 60% 40% 30% 70% / 60% 30% 70% 40%; z-index: 5; }
</style>
<link rel="stylesheet" href="css/style-{{.Version}}.css">
<link rel="modulepreload" href="js/electerm-{{.Version}}.js">
<style id="theme-css"></style>
<style id="custom-css"></style>
</head>
<body>
<div id="container"></div>
<div id="content-loading"><div class="morph-shape iblock pd3"><img class="iblock logo-filter" src="images/electerm.png" alt="" height="80"></div></div>
<script>window.et = {{.Et}}</script>
<script type="module" src="js/shim-{{.Version}}.js"></script>
<script type="module" src="js/basic-{{.Version}}.js"></script>
</body>
</html>`))

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	et := map[string]any{
		"version":             s.version,
		"siteName":            "electerm",
		"isDev":               false,
		"tokenElecterm":       s.token,
		"disableAIFeature":    true,
		"disableUpgradeCheck": true,
		"hasNodePty":          true,
		// session types the Go backend implements; the bookmark form hides the rest
		"supportSessionTypes": []string{"ssh", "telnet", "serial", "local", "ftp", "vnc", "rdp"},
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	indexTmpl.Execute(w, map[string]any{"Version": s.version, "Et": et})
}

type call struct {
	Name string `json:"name"`
	Args []any  `json:"args"`
}

func writeResult(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var c call
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeResult(w, nil, err)
		return
	}
	data, err := s.runSync(c.Name, c.Args)
	writeResult(w, data, err)
}

func (s *Server) handleAsync(w http.ResponseWriter, r *http.Request) {
	var c call
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeResult(w, nil, err)
		return
	}
	data, err := s.runAsync(c.Name, c.Args)
	if err != nil {
		log.Printf("async %s: %v", c.Name, err)
	}
	writeResult(w, data, err)
}

// handleEvents streams main-process events (Electron's webContents.send) as server-sent events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan []byte, 16)
	s.subMu.Lock()
	s.subs[ch] = true
	s.subMu.Unlock()
	defer func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}()
	fmt.Fprint(w, ": ok\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			fl.Flush()
		}
	}
}

func (s *Server) Emit(event string, args ...any) {
	msg, _ := json.Marshal(map[string]any{"event": event, "args": args})
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

// wsConn is a JSON WebSocket with electerm's request/response helpers (src/app/server/ws-dec.js).
type wsConn struct {
	c       *websocket.Conn
	ctx     context.Context
	mu      sync.Mutex
	waiters map[string]chan map[string]any
}

func accept(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	// Default origin policy: only pages served from this host:port may connect.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(64 << 20)
	return &wsConn{c: c, ctx: r.Context(), waiters: map[string]chan map[string]any{}}, nil
}

func (ws *wsConn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("ws marshal: %v", err)
		return
	}
	ws.c.Write(ws.ctx, websocket.MessageText, b)
}

func (ws *wsConn) reply(id string, data any, err error) {
	if err != nil {
		ws.send(map[string]any{"id": id, "error": map[string]any{"message": err.Error(), "stack": ""}})
		return
	}
	ws.send(map[string]any{"id": id, "data": data})
}

// ask sends msg (which carries "id") and waits for the client's message with the same id.
func (ws *wsConn) ask(id string, msg map[string]any) (map[string]any, error) {
	ch := make(chan map[string]any, 1)
	ws.mu.Lock()
	ws.waiters[id] = ch
	ws.mu.Unlock()
	defer func() {
		ws.mu.Lock()
		delete(ws.waiters, id)
		ws.mu.Unlock()
	}()
	ws.send(msg)
	select {
	case m := <-ch:
		return m, nil
	case <-ws.ctx.Done():
		return nil, errors.New("connection closed")
	}
}

// loop reads JSON messages; replies to ask() are routed to their waiter, the rest go to handle.
func (ws *wsConn) loop(handle func(msg map[string]any)) {
	defer ws.c.CloseNow()
	for {
		_, b, err := ws.c.Read(ws.ctx)
		if err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(b, &msg) != nil {
			continue
		}
		id, _ := msg["id"].(string)
		ws.mu.Lock()
		ch := ws.waiters[id]
		ws.mu.Unlock()
		if ch != nil && msg["action"] == nil {
			ch <- msg
			continue
		}
		handle(msg)
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func (s *Server) term(pid string) *Term {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terms[pid]
}

// handleCommon is dispatch-center.js's /common/s plus session-process.js's terminal bookkeeping.
func (s *Server) handleCommon(w http.ResponseWriter, r *http.Request) {
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	ws.loop(func(msg map[string]any) {
		go s.commonAction(ws, msg)
	})
}

func (s *Server) commonAction(ws *wsConn, msg map[string]any) {
	id := str(msg, "id")
	pid := str(msg, "pid")
	switch str(msg, "action") {
	case "create-terminal", "test-terminal":
		body, _ := msg["body"].(map[string]any)
		o := opts(body)
		ask := func(options map[string]any) ([]string, error) {
			qid := newID()
			res, err := ws.ask(qid, map[string]any{
				"id": qid, "action": "session-interactive", "tabId": o.str("tabId"), "options": options,
			})
			if err != nil {
				return nil, err
			}
			out := []string{}
			for _, v := range asSlice(res["results"]) {
				s, _ := v.(string)
				out = append(out, s)
			}
			return out, nil
		}
		_, port, _ := net.SplitHostPort(s.addr)
		if o.str("termType") == "ftp" {
			// FTP has no terminal: the file manager connects over /sftp (session-process.js skips create-terminal)
			if str(msg, "action") == "test-terminal" {
				f, err := dialFTP(o)
				if err == nil {
					f.Close()
				}
				ws.reply(id, true, err)
				return
			}
			ws.reply(id, map[string]any{"pid": o.str("uid"), "port": port}, nil)
			return
		}
		if typ := o.str("termType"); typ == "vnc" || typ == "rdp" {
			t, err := prepareHops(o, ask)
			if err == nil && str(msg, "action") == "test-terminal" {
				var c net.Conn
				if c, err = t.dial(map[string]int{"vnc": 5900, "rdp": 3389}[typ]); err == nil {
					c.Close()
				}
				t.Close()
				ws.reply(id, true, err)
				return
			}
			if err != nil {
				ws.reply(id, nil, err)
				return
			}
			hopMu.Lock()
			if old := hopTargets[o.str("uid")]; old != nil {
				old.Close()
			}
			hopTargets[o.str("uid")] = t
			hopMu.Unlock()
			ws.reply(id, map[string]any{"pid": o.str("uid"), "port": port}, nil)
			return
		}
		t, err := startSession(o, ask, func(m map[string]any) { ws.send(m) })
		if err != nil {
			ws.reply(id, nil, err)
			return
		}
		if str(msg, "action") == "test-terminal" {
			t.Close()
			ws.reply(id, true, nil)
			return
		}
		t.pid = o.str("uid")
		t.cols.Store(int32(o.num("cols", 80)))
		t.logDir, t.logName, t.logTS = o.str("sessionLogPath"), o.str("logName"), o["addTimeStampToTermLog"] == true
		if t.logName == "" {
			t.logName = t.pid
		}
		if t.logDir == "" {
			t.logDir = filepath.Join(dataDir(), "session_logs")
		}
		if o["saveTerminalLogToFile"] == true {
			if err := t.startLog(t.defaultLogPath()); err != nil {
				log.Printf("session log: %v", err)
			}
		}
		s.mu.Lock()
		if old := s.terms[t.pid]; old != nil {
			old.Close()
		}
		s.terms[t.pid] = t
		s.mu.Unlock()
		ws.reply(id, map[string]any{"pid": t.pid, "port": port}, nil)
	case "resize-terminal":
		if t := s.term(pid); t != nil {
			cols := int(msg["cols"].(float64))
			t.resize(cols, int(msg["rows"].(float64)))
			t.cols.Store(int32(cols))
			if f := t.trz.Load(); f != nil {
				f.SetTerminalColumns(int32(cols))
			}
		}
		ws.reply(id, "ok", nil)
	case "run-cmd":
		// electerm answers "" for sessions without an exec channel (terminal-api.js runCmd)
		out := ""
		if t := s.term(pid); t != nil && t.client != nil {
			out, _ = s.runCmd(pid, str(msg, "cmd"))
		}
		ws.reply(id, out, nil)
	case "exec-cmd":
		timeout := 120 * time.Second
		if ms, ok := msg["timeoutMs"].(float64); ok && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
		res, err := s.execCmd(pid, str(msg, "cmd"), timeout)
		ws.reply(id, res, err)
	case "sync":
		data, err := doSync(str(msg, "type"), str(msg, "func"), asSlice(msg["args"]), str(msg, "token"), str(msg, "proxy"))
		if err != nil {
			err = fmt.Errorf("Sync data error: %w", err)
		}
		ws.reply(id, data, err)
	case "fetch":
		data, err := axiosLike(asMap(msg["options"]), str(msg, "proxy"), false)
		ws.reply(id, data, err)
	case "fs":
		args, _ := msg["args"].([]any)
		data, err := fsCall(str(msg, "func"), args)
		ws.reply(id, data, err)
	case "toggle-terminal-log", "toggle-terminal-log-timestamp", "set-terminal-log-path", "start-terminal-log-file":
		var err error
		if t := s.term(pid); t != nil {
			err = t.logAction(str(msg, "action"), msg)
		}
		ws.reply(id, "ok", err)
	default:
		ws.reply(id, nil, fmt.Errorf("action %q not supported", str(msg, "action")))
	}
}

// runCmd runs a command on the session's SSH server over a separate exec channel.
func (s *Server) runCmd(pid, cmd string) (string, error) {
	t := s.term(pid)
	if t == nil || t.client == nil {
		return "", errors.New("exec channel not supported for this session type")
	}
	sess, err := t.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

// execCmd is session-common.js execCommand: separate streams, the real exit code, optional timeout.
func (s *Server) execCmd(pid, cmd string, timeout time.Duration) (map[string]any, error) {
	t := s.term(pid)
	if t == nil || t.client == nil {
		return nil, errors.New("exec channel not supported for this session type")
	}
	sess, err := t.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var out, errOut strings.Builder
	sess.Stdout, sess.Stderr = &out, &errOut
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	var exitCode any
	timedOut := false
	select {
	case err := <-done:
		var ee *ssh.ExitError
		switch {
		case err == nil:
			exitCode = 0
		case errors.As(err, &ee):
			exitCode = ee.ExitStatus()
		default:
			var missing *ssh.ExitMissingError
			if !errors.As(err, &missing) {
				return nil, err
			}
		}
	case <-time.After(timeout):
		timedOut = true
		sess.Close()
	}
	return map[string]any{"stdout": out.String(), "stderr": errOut.String(), "exitCode": exitCode, "timedOut": timedOut}, nil
}

// handleTerminal is session-server.js's /terminals/:pid: raw output out, keystrokes in.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	t := s.term(pid)
	if t == nil {
		http.Error(w, "no such terminal", http.StatusNotFound)
		return
	}
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer func() {
		cancel()
		t.Close()
		s.mu.Lock()
		if s.terms[pid] == t {
			delete(s.terms, pid)
		}
		s.mu.Unlock()
		ws.c.Close(websocket.StatusNormalClosure, "")
	}()
	// trzsz (trz/tsz, and rz/sz where a local lrzsz exists) runs as a filter between the
	// session and the socket, in place of electerm's server-side trzsz.js/zmodem.js.
	out, in := io.Reader(t.out), io.Writer(t.in)
	clientIn, toFilter := io.Pipe()
	fromFilter, clientOut := io.Pipe()
	f := trzsz.NewTrzszFilter(clientIn, clientOut, writeCloser{t.in}, t.out,
		trzsz.TrzszOptions{TerminalColumns: int32(t.cols.Load()), EnableZmodem: true})
	t.trz.Store(f)
	out, in = fromFilter, toFilter
	defer toFilter.Close()
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := out.Read(buf)
			if n > 0 {
				t.logWrite(buf[:n])
				if ws.c.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		typ, b, err := ws.c.Read(ctx)
		if err != nil {
			return
		}
		// JSON control frames (keepalive, zmodem/trzsz/xmodem events) are not ported; drop them.
		if typ == websocket.MessageText && len(b) > 0 && b[0] == '{' {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil && m["action"] != nil {
				continue
			}
		}
		if _, err := in.Write(b); err != nil {
			return
		}
	}
}

// writeCloser lets a plain writer be the filter's server input; closing it is a no-op.
type writeCloser struct{ io.Writer }

func (w writeCloser) Close() error {
	if c, ok := w.Writer.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
