package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
)

// parseEditorCommand splits a command line with quotes, like open-file-with-editor.js.
func parseEditorCommand(command string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote rune
	has := false
	rs := []rune(strings.TrimSpace(command))
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0 && c == '\\' && i+1 < len(rs) && rs[i+1] == quote:
			cur.WriteRune(quote)
			i++
		case quote != 0:
			cur.WriteRune(c)
		case c == '"' || c == '\'':
			quote, has = c, true
		case unicode.IsSpace(c):
			if cur.Len() > 0 || has {
				args = append(args, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(c)
		}
	}
	if quote != 0 {
		return nil, errors.New("editor command contains an unmatched quote")
	}
	if cur.Len() > 0 || has {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, errors.New("editor command is required")
	}
	return args, nil
}

// openFileWithEditor starts the user's editor on a (temporary) file the SFTP pane downloaded.
func openFileWithEditor(path, editor string) error {
	args, err := parseEditorCommand(editor)
	if err != nil {
		return err
	}
	cmd := exec.Command(args[0], append(args[1:], path)...)
	return cmd.Start()
}

// fileWatch is lib/watch-file.js: one watched file whose saves are pushed to the UI as
// "file-change" (or "file-deleted" / "file-read-error"), debounced by 300ms.
// ponytail: polls mtime every 500ms; switch to fsnotify if watching many files.
type fileWatch struct {
	mu   sync.Mutex
	path string
	stop chan struct{}
}

func (w *fileWatch) watch(path string, emit func(event string, args ...any)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop != nil {
		close(w.stop)
	}
	w.path, w.stop = path, make(chan struct{})
	stop := w.stop
	go func() {
		var last time.Time
		if fi, err := os.Stat(path); err == nil {
			last = fi.ModTime()
		}
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			fi, err := os.Stat(path)
			if err != nil {
				emit("file-deleted")
				return
			}
			if !fi.ModTime().Equal(last) {
				last = fi.ModTime()
				time.Sleep(300 * time.Millisecond)
				b, err := os.ReadFile(path)
				if err != nil {
					emit("file-read-error", err.Error())
					continue
				}
				emit("file-change", string(b))
			}
		}
	}()
}

func (w *fileWatch) unwatch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop != nil {
		close(w.stop)
		w.stop = nil
	}
}
