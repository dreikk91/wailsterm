package main

import (
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

// termLog writes a session's output as plain text lines, like electerm's session-base.js logger.
// ponytail: strips escape sequences and applies \r/\b instead of running a headless xterm, so
// full-screen apps (vim, top) log as noise; port a vt emulator if those logs must be clean.
type termLog struct {
	mu        sync.Mutex
	f         *os.File
	line      []rune
	cr        bool
	state     int // 0 text, 1 after ESC, 2 CSI, 3 OSC/DCS string, 4 ESC inside string
	timestamp bool
	pending   []byte // incomplete UTF-8 tail
}

func openTermLog(path string, timestamp bool) (*termLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &termLog{f: f, timestamp: timestamp}, nil
}

func (l *termLog) flush() {
	prefix := ""
	if l.timestamp {
		prefix = "[" + time.Now().Format("2006-01-02 15:04:05") + "] "
	}
	l.f.WriteString(prefix + string(l.line) + "\n")
	l.line = l.line[:0]
}

func (l *termLog) Write(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := string(append(l.pending, b...))
	l.pending = nil
	// keep a trailing partial UTF-8 sequence for the next chunk
	for i := len(s) - 1; i >= 0 && i >= len(s)-3; i-- {
		if !utf8.RuneStart(s[i]) {
			continue
		}
		if !utf8.FullRuneInString(s[i:]) {
			l.pending = []byte(s[i:])
			s = s[:i]
		}
		break
	}
	for _, r := range s {
		switch l.state {
		case 1:
			switch r {
			case '[':
				l.state = 2
			case ']', 'P', '_', '^':
				l.state = 3
			default:
				l.state = 0
			}
			continue
		case 2:
			if r >= 0x40 && r <= 0x7e {
				l.state = 0
			}
			continue
		case 3:
			if r == 0x07 {
				l.state = 0
			} else if r == 0x1b {
				l.state = 4
			}
			continue
		case 4:
			l.state = 0 // ESC \ ends the string
			continue
		}
		switch {
		case r == 0x1b:
			l.state = 1
		case r == '\n':
			l.cr = false
			l.flush()
		case r == '\r':
			l.cr = true
		case r == '\b':
			if n := len(l.line); n > 0 {
				l.line = l.line[:n-1]
			}
		case r < 0x20 || r == 0x7f:
		default:
			if l.cr {
				l.line = l.line[:0]
				l.cr = false
			}
			l.line = append(l.line, r)
		}
	}
}

func (l *termLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.line) > 0 {
		l.flush()
	}
	l.f.Close()
}

// logging state of one terminal (electerm session-base.js toggle*/set*/start* methods)

func (t *Term) logWrite(b []byte) {
	t.logMu.Lock()
	l := t.log
	t.logMu.Unlock()
	if l != nil {
		l.Write(b)
	}
}

func (t *Term) startLog(path string) error {
	t.logMu.Lock()
	defer t.logMu.Unlock()
	if t.log != nil {
		t.log.Close()
		t.log = nil
	}
	l, err := openTermLog(path, t.logTS)
	if err != nil {
		return err
	}
	t.log = l
	return nil
}

func (t *Term) stopLog() {
	t.logMu.Lock()
	defer t.logMu.Unlock()
	if t.log != nil {
		t.log.Close()
		t.log = nil
	}
}

func (t *Term) logging() bool {
	t.logMu.Lock()
	defer t.logMu.Unlock()
	return t.log != nil
}

func (t *Term) defaultLogPath() string {
	return filepath.Join(t.logDir, t.logName+".log")
}

func (t *Term) logAction(action string, msg map[string]any) error {
	switch action {
	case "toggle-terminal-log":
		if t.logging() {
			t.stopLog()
			return nil
		}
		return t.startLog(t.defaultLogPath())
	case "toggle-terminal-log-timestamp":
		t.logMu.Lock()
		t.logTS = !t.logTS
		if t.log != nil {
			t.log.mu.Lock()
			t.log.timestamp = t.logTS
			t.log.mu.Unlock()
		}
		t.logMu.Unlock()
	case "set-terminal-log-path":
		if p := str(msg, "logPath"); p != "" {
			t.logDir = p
			if t.logging() {
				return t.startLog(t.defaultLogPath())
			}
		}
	case "start-terminal-log-file":
		if p := str(msg, "logFilePath"); p != "" {
			t.logTS = msg["addTimeStampToTermLog"] == true
			return t.startLog(p)
		}
	}
	return nil
}
