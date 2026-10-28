package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/trzsz/trzsz-go/trzsz"
	"go.bug.st/serial"
	"golang.org/x/crypto/ssh"
)

// Term is one terminal session: what electerm's session-*.js classes provide to session-server.js.
type Term struct {
	pid    string
	out    io.Reader
	in     io.Writer
	resize func(cols, rows int) error
	close  func() error
	client *ssh.Client // set for SSH sessions; SFTP rides on it
	once   sync.Once

	cols atomic.Int32
	trz  atomic.Pointer[trzsz.TrzszFilter]

	logMu   sync.Mutex
	log     *termLog
	logDir  string
	logName string
	logTS   bool
}

func (t *Term) Close() {
	t.once.Do(func() {
		t.stopLog()
		t.close()
	})
}

// opts is the initOptions object electerm's client sends with create-terminal.
type opts map[string]any

func (o opts) str(k string) string {
	if s, ok := o[k].(string); ok {
		return s
	}
	return ""
}

func (o opts) num(k string, def int) int {
	switch v := o[k].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func (o opts) strs(k string) []string {
	a, _ := o[k].([]any)
	out := make([]string, 0, len(a))
	for _, v := range a {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func startSession(o opts, ask interactor, notify func(map[string]any)) (*Term, error) {
	typ := o.str("termType")
	if typ == "" {
		typ = o.str("type")
	}
	switch {
	case typ == "local" || (o.str("host") == "" && typ != "serial"):
		return startLocal(o)
	case typ == "telnet":
		return startTelnet(o)
	case typ == "serial":
		return startSerial(o)
	case typ == "ssh" || typ == "remote" || typ == "":
		return startSSH(o, ask, notify)
	}
	return nil, errors.New("session type not supported yet: " + typ)
}

func startLocal(o opts) (*Term, error) {
	var exe string
	var args []string
	switch runtime.GOOS {
	case "windows":
		exe, args = o.str("execWindows"), o.strs("execWindowsArgs")
		if strings.Contains(exe, "..") {
			return nil, errors.New(`execWindows should not contain ".."`)
		}
		if exe == "" {
			exe = "System32/WindowsPowerShell/v1.0/powershell.exe"
		}
		if !filepath.IsAbs(exe) {
			exe = filepath.Join(os.Getenv("windir"), exe)
		}
	case "darwin":
		exe, args = o.str("execMac"), append([]string{"--login"}, o.strs("execMacArgs")...)
	default:
		exe, args = o.str("execLinux"), o.strs("execLinuxArgs")
	}
	if exe == "" {
		exe = os.Getenv("SHELL")
	}
	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	p.Resize(o.num("cols", 80), o.num("rows", 24))
	cmd := p.Command(exe, args...)
	cmd.Dir, _ = os.UserHomeDir()
	if d := o.str("startDirectoryLocal"); d != "" {
		cmd.Dir = d
	}
	term := o.str("term")
	if term == "" {
		term = "xterm-256color"
	}
	cmd.Env = append(os.Environ(), "TERM="+term)
	if err := cmd.Start(); err != nil {
		p.Close()
		return nil, err
	}
	go cmd.Wait()
	return &Term{out: p, in: p, resize: p.Resize, close: p.Close}, nil
}

func noResize(int, int) error { return nil }

// lineEndingWriter turns the Enter key's \r into the session's txLineEnding (electerm line-ending.js).
type lineEndingWriter struct {
	w   io.Writer
	end string
}

func (l lineEndingWriter) Write(p []byte) (int, error) {
	if l.end == "" || l.end == "\r" {
		return l.w.Write(p)
	}
	_, err := l.w.Write([]byte(strings.ReplaceAll(string(p), "\r", l.end)))
	return len(p), err
}

func lineEnding(o opts) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\n").Replace(o.str("txLineEnding"))
}

func startTelnet(o opts) (*Term, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(o.str("host"), strconv.Itoa(o.num("port", 23))),
		time.Duration(o.num("readyTimeout", 50000))*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &Term{
		out:    &telnetReader{bufio.NewReader(c), c},
		in:     lineEndingWriter{telnetWriter{c}, lineEnding(o)},
		resize: noResize,
		close:  c.Close,
	}, nil
}

func startSerial(o opts) (*Term, error) {
	mode := &serial.Mode{BaudRate: o.num("baudRate", 9600), DataBits: o.num("dataBits", 8)}
	switch o.num("stopBits", 1) {
	case 2:
		mode.StopBits = serial.TwoStopBits
	default:
		mode.StopBits = serial.OneStopBit
	}
	mode.Parity = map[string]serial.Parity{
		"even": serial.EvenParity, "odd": serial.OddParity, "mark": serial.MarkParity, "space": serial.SpaceParity,
	}[o.str("parity")]
	p, err := serial.Open(o.str("path"), mode)
	if err != nil {
		return nil, err
	}
	return &Term{out: p, in: lineEndingWriter{p, lineEnding(o)}, resize: noResize, close: p.Close}, nil
}

func listSerialPorts() []map[string]any {
	ports, _ := serial.GetPortsList()
	out := make([]map[string]any, 0, len(ports))
	for _, p := range ports {
		out = append(out, map[string]any{"path": p})
	}
	return out
}
