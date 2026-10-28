package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// nodeMode converts a Go FileMode to Node's st_mode number (type bits + permissions).
func nodeMode(m fs.FileMode) uint32 {
	t := uint32(0o100000)
	switch {
	case m&fs.ModeSymlink != 0:
		t = 0o120000
	case m.IsDir():
		t = 0o040000
	}
	return t | uint32(m.Perm())
}

// nodeStat shapes FileInfo like the JSON of Node's fs.Stats (and ssh2's Stats).
func nodeStat(fi fs.FileInfo) map[string]any {
	ms := fi.ModTime().UnixMilli()
	t := fi.ModTime().UTC().Format(time.RFC3339Nano)
	return map[string]any{
		"mode": nodeMode(fi.Mode()), "size": fi.Size(), "uid": 0, "gid": 0,
		"atimeMs": ms, "mtimeMs": ms, "ctimeMs": ms, "birthtimeMs": ms,
		"atime": t, "mtime": t, "ctime": t, "birthtime": t,
		"isDirectory": fi.IsDir(), "isSymbolicLink": fi.Mode()&fs.ModeSymlink != 0,
		"isD": fi.IsDir(), "isF": fi.Mode().IsRegular(),
	}
}

var (
	fdMu   sync.Mutex
	fdNext = 100
	fds    = map[int]*os.File{}
)

func argNum(args []any, i int) int {
	if i < len(args) {
		if f, ok := args[i].(float64); ok {
			return int(f)
		}
	}
	return 0
}

func openFlags(flag string) int {
	return map[string]int{
		"r": os.O_RDONLY, "r+": os.O_RDWR, "w": os.O_WRONLY | os.O_CREATE | os.O_TRUNC,
		"w+": os.O_RDWR | os.O_CREATE | os.O_TRUNC, "a": os.O_WRONLY | os.O_CREATE | os.O_APPEND,
		"a+": os.O_RDWR | os.O_CREATE | os.O_APPEND,
	}[flag]
}

func windowsDrives() []string {
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		if _, err := os.Stat(string(c) + `:\`); err == nil {
			out = append(out, string(c)+":")
		}
	}
	return out
}

func readdirNames(p string) ([]string, error) {
	if runtime.GOOS == "windows" {
		if p == "/" {
			return windowsDrives(), nil
		}
		if len(p) == 2 && p[1] == ':' {
			p += `\`
		}
	}
	des, err := os.ReadDir(p)
	names := make([]string, 0, len(des))
	for _, d := range des {
		names = append(names, d.Name())
	}
	return names, err
}

func copyLocal(from, to string) error {
	fi, err := os.Stat(from)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.MkdirAll(to, 0o755); err != nil {
			return err
		}
		des, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, d := range des {
			if err := copyLocal(filepath.Join(from, d.Name()), filepath.Join(to, d.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	r, err := os.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func shellOut(cmd string) (string, string, error) {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("powershell.exe", "-NoLogo", "-NonInteractive", "-Command", cmd)
	} else {
		c = exec.Command("bash", "--login", "-c", cmd)
	}
	var out, errb strings.Builder
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	return out.String(), errb.String(), err
}

// fsCall is src/app/lib/fs.js (fsExport), dispatched by the common socket's "fs" action.
func fsCall(fn string, args []any) (any, error) {
	p := argStr(args, 0)
	switch fn {
	case "statAsync", "statCustom":
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		return nodeStat(fi), nil
	case "lstatAsync":
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		return nodeStat(fi), nil
	case "readdirAsync", "readdir":
		return readdirNames(p)
	case "access":
		_, err := os.Stat(p)
		return nil, err
	case "exists":
		_, err := os.Stat(p)
		return err == nil, nil
	case "realpath":
		return filepath.EvalSymlinks(p)
	case "mkdir":
		return nil, os.MkdirAll(p, 0o755)
	case "touch":
		return nil, os.WriteFile(p, nil, 0o644)
	case "chmod":
		return nil, os.Chmod(p, fs.FileMode(argNum(args, 1)))
	case "rename":
		return nil, os.Rename(p, argStr(args, 1))
	case "mv":
		if err := os.Rename(p, argStr(args, 1)); err != nil {
			if err := copyLocal(p, argStr(args, 1)); err != nil {
				return nil, err
			}
			return true, os.RemoveAll(p)
		}
		return true, nil
	case "cp":
		return nil, copyLocal(p, argStr(args, 1))
	case "unlink":
		return nil, os.Remove(p)
	case "rmrf":
		return nil, os.RemoveAll(p)
	case "readFile":
		b, err := os.ReadFile(p)
		return string(b), err
	case "readFileAsBase64":
		b, err := os.ReadFile(p)
		return base64.StdEncoding.EncodeToString(b), err
	case "writeFile":
		mode := fs.FileMode(argNum(args, 2))
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(p, []byte(argStr(args, 1)), mode) == nil, nil
	case "openFile":
		return nil, openPath(p)
	case "getFolderSize":
		var size, count int64
		filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if fi, err := d.Info(); err == nil {
					size += fi.Size()
					count++
				}
			}
			return nil
		})
		return map[string]any{"size": float64(size) / 1024, "count": count}, nil
	case "openCustom":
		f, err := os.OpenFile(p, openFlags(argStr(args, 1)), 0o644)
		if err != nil {
			return nil, err
		}
		fdMu.Lock()
		defer fdMu.Unlock()
		fdNext++
		fds[fdNext] = f
		return fdNext, nil
	case "readCustom", "writeCustom", "closeCustom":
		fdMu.Lock()
		f := fds[argNum(args, 0)]
		if fn == "closeCustom" {
			delete(fds, argNum(args, 0))
		}
		fdMu.Unlock()
		if f == nil {
			return nil, errors.New("bad file descriptor")
		}
		switch fn {
		case "closeCustom":
			return true, f.Close()
		case "writeCustom":
			b, err := base64.StdEncoding.DecodeString(argStr(args, 1))
			if err != nil {
				return nil, err
			}
			_, err = f.Write(b)
			return 1, err
		}
		buf := make([]byte, argNum(args, 1))
		var n int
		var err error
		// Node signature: read(fd, buffer, offset, length, position)
		if len(args) > 4 && args[4] != nil {
			n, err = f.ReadAt(buf, int64(argNum(args, 4)))
		} else {
			n, err = f.Read(buf)
		}
		if err == io.EOF {
			err = nil
		}
		return map[string]any{"n": n, "newArr": base64.StdEncoding.EncodeToString(buf)}, err
	case "run":
		out, _, err := shellOut(p)
		return out, err
	case "runWinCmd":
		out, errOut, err := shellOut(p)
		return map[string]any{"stdout": out, "stderr": errOut}, err
	}
	return nil, fmt.Errorf("invalid fs function: %s", fn)
}
