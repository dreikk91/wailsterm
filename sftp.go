package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
)

// fsys is what a transfer needs from either side: the local disk or an SFTP server.
type fsys interface {
	ReadDir(p string) ([]os.FileInfo, error)
	Stat(p string) (os.FileInfo, error)
	Open(p string) (io.ReadCloser, error)
	Create(p string) (io.WriteCloser, error)
	MkdirAll(p string) error
	Join(elem ...string) string
}

type localFS struct{}

func (localFS) ReadDir(p string) ([]os.FileInfo, error) {
	des, err := os.ReadDir(p)
	infos := make([]os.FileInfo, 0, len(des))
	for _, de := range des {
		if fi, err := de.Info(); err == nil {
			infos = append(infos, fi)
		}
	}
	return infos, err
}
func (localFS) Stat(p string) (os.FileInfo, error)      { return os.Stat(p) }
func (localFS) Open(p string) (io.ReadCloser, error)    { return os.Open(p) }
func (localFS) Create(p string) (io.WriteCloser, error) { return os.Create(p) }
func (localFS) MkdirAll(p string) error                 { return os.MkdirAll(p, 0o755) }
func (localFS) Join(elem ...string) string              { return filepath.Join(elem...) }

type remoteFS struct{ *sftp.Client }

func (r remoteFS) Open(p string) (io.ReadCloser, error)    { return r.Client.Open(p) }
func (r remoteFS) Create(p string) (io.WriteCloser, error) { return r.Client.Create(p) }
func (remoteFS) Join(elem ...string) string                { return path.Join(elem...) }

func treeSize(f fsys, p string) int64 {
	fi, err := f.Stat(p)
	if err != nil {
		return 0
	}
	if !fi.IsDir() {
		return fi.Size()
	}
	infos, _ := f.ReadDir(p)
	var n int64
	for _, c := range infos {
		n += treeSize(f, f.Join(p, c.Name()))
	}
	return n
}

// progress counts copied bytes, honours pause, and stops when ctx is cancelled.
type progress struct {
	ctx     context.Context
	done    atomic.Int64
	total   int64
	pausing atomic.Bool
	emit    func(force bool)
}

type countingWriter struct {
	w io.Writer
	p *progress
}

func (c countingWriter) Write(b []byte) (int, error) {
	for c.p.pausing.Load() {
		if c.p.ctx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := c.p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.w.Write(b)
	c.p.done.Add(int64(n))
	c.p.emit(false)
	return n, err
}

func copyTree(src, dst fsys, from, to string, p *progress) error {
	fi, err := src.Stat(from)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := dst.MkdirAll(to); err != nil {
			return err
		}
		infos, err := src.ReadDir(from)
		if err != nil {
			return err
		}
		for _, c := range infos {
			if err := copyTree(src, dst, src.Join(from, c.Name()), dst.Join(to, c.Name()), p); err != nil {
				return err
			}
		}
		return nil
	}
	r, err := src.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := dst.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(countingWriter{w, p}, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

type sftpSess struct {
	terminalID string
	c          *sftp.Client
}

var (
	sftpMu sync.Mutex
	sftps  = map[string]*sftpSess{}
)

func getSftp(id string) *sftp.Client {
	sftpMu.Lock()
	defer sftpMu.Unlock()
	if s := sftps[id]; s != nil {
		return s.c
	}
	return nil
}

func rights(perm fs.FileMode, shift uint) string {
	s := ""
	for i, c := range "rwx" {
		if perm&(1<<(shift+2-uint(i))) != 0 {
			s += string(c)
		}
	}
	return s
}

// sftpEntry shapes a FileInfo like session-sftp.js list() items.
func sftpEntry(fi os.FileInfo) map[string]any {
	typ := "-"
	switch {
	case fi.IsDir():
		typ = "d"
	case fi.Mode()&fs.ModeSymlink != 0:
		typ = "l"
	}
	e := map[string]any{
		"type": typ, "name": fi.Name(), "size": fi.Size(),
		"modifyTime": fi.ModTime().UnixMilli(), "accessTime": fi.ModTime().UnixMilli(),
		"mode":   nodeMode(fi.Mode()),
		"rights": map[string]string{"user": rights(fi.Mode().Perm(), 6), "group": rights(fi.Mode().Perm(), 3), "other": rights(fi.Mode().Perm(), 0)},
	}
	if st, ok := fi.Sys().(*sftp.FileStat); ok {
		e["owner"], e["group"] = st.UID, st.GID
		e["accessTime"] = int64(st.Atime) * 1000
	}
	return e
}

func sftpStat(fi os.FileInfo) map[string]any {
	st := nodeStat(fi)
	if s, ok := fi.Sys().(*sftp.FileStat); ok {
		st["uid"], st["gid"] = s.UID, s.GID
		st["atime"], st["mtime"] = s.Atime, s.Mtime
	}
	return st
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (s *Server) sftpFunc(sess *sftpSess, fn string, args []any) (any, error) {
	if fn == "connect" {
		t := s.term(sess.terminalID)
		if t == nil || t.client == nil {
			return nil, errors.New("terminal is not an SSH session")
		}
		c, err := sftp.NewClient(t.client)
		if err != nil {
			return nil, err
		}
		sess.c = c
		return "ok", nil
	}
	c := sess.c
	if c == nil {
		return nil, errors.New("sftp not connected")
	}
	p := argStr(args, 0)
	switch fn {
	case "list":
		infos, err := c.ReadDir(p)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, len(infos))
		for i, fi := range infos {
			out[i] = sftpEntry(fi)
		}
		return out, nil
	case "getHomeDir":
		return c.Getwd()
	case "realpath":
		if p == "" {
			p = "."
		}
		return c.RealPath(p)
	case "readlink":
		return c.ReadLink(p)
	case "stat", "lstat":
		var fi os.FileInfo
		var err error
		if fn == "stat" {
			fi, err = c.Stat(p)
		} else {
			fi, err = c.Lstat(p)
		}
		if err != nil {
			return nil, err
		}
		return sftpStat(fi), nil
	case "mkdir":
		return 1, c.Mkdir(p)
	case "rmdir":
		return 1, c.RemoveAll(p)
	case "rm":
		return 1, c.Remove(p)
	case "touch":
		f, err := c.Create(p)
		if err != nil {
			return nil, err
		}
		return 1, f.Close()
	case "chmod":
		return 1, c.Chmod(p, fs.FileMode(argNum(args, 1)))
	case "rename", "mv":
		return 1, c.Rename(p, argStr(args, 1))
	case "cp":
		out, err := s.runCmd(sess.terminalID, "cp -r "+shQuote(p)+" "+shQuote(argStr(args, 1)))
		if err != nil {
			return nil, fmt.Errorf("%v: %s", err, out)
		}
		return 1, nil
	case "readFile":
		f, err := c.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		return string(b), err
	case "writeFile":
		f, err := c.Create(p)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(f, argStr(args, 1)); err != nil {
			f.Close()
			return nil, err
		}
		if m := argNum(args, 2); m != 0 {
			c.Chmod(p, fs.FileMode(m))
		}
		return 1, f.Close()
	}
	return nil, fmt.Errorf("invalid sftp function: %s", fn)
}

// handleSftp is session-server.js's /sftp/:id.
func (s *Server) handleSftp(w http.ResponseWriter, r *http.Request) {
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	var ids []string
	defer func() {
		sftpMu.Lock()
		for _, id := range ids {
			if ss := sftps[id]; ss != nil && ss.c != nil {
				ss.c.Close()
			}
			delete(sftps, id)
		}
		sftpMu.Unlock()
	}()
	ws.loop(func(msg map[string]any) {
		id := str(msg, "id")
		switch str(msg, "action") {
		case "sftp-new":
			sftpMu.Lock()
			sftps[id] = &sftpSess{terminalID: str(msg, "terminalId")}
			sftpMu.Unlock()
			ids = append(ids, id)
		case "sftp-func":
			sftpMu.Lock()
			sess := sftps[id]
			sftpMu.Unlock()
			uid := str(msg, "uid")
			if sess == nil {
				ws.reply(uid, nil, errors.New("no sftp session"))
				return
			}
			args, _ := msg["args"].([]any)
			go func() {
				data, err := s.sftpFunc(sess, str(msg, "func"), args)
				ws.reply(uid, data, err)
			}()
		case "sftp-destroy":
			ws.c.CloseNow()
		}
	})
}

// handleTransfer is session-server.js's /transfer/:id backed by transfer.js semantics.
func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var p *progress
	ws.loop(func(msg map[string]any) {
		id := str(msg, "id")
		switch str(msg, "action") {
		case "transfer-new":
			c := getSftp(str(msg, "sftpId"))
			if c == nil {
				ws.send(map[string]any{"id": "transfer:err:" + id, "error": map[string]any{"message": "no sftp session"}})
				return
			}
			var src, dst fsys = remoteFS{c}, localFS{}
			from, to := str(msg, "remotePath"), str(msg, "localPath")
			switch str(msg, "type") {
			case "download":
			case "upload":
				src, dst, from, to = dst, src, to, from
			default:
				ws.send(map[string]any{"id": "transfer:err:" + id, "error": map[string]any{"message": "transfer type not supported: " + str(msg, "type")}})
				return
			}
			p = &progress{ctx: ctx, total: treeSize(src, from)}
			last := time.Time{}
			p.emit = func(force bool) {
				if force || time.Since(last) > 500*time.Millisecond {
					last = time.Now()
					ws.send(map[string]any{"id": "transfer:data:" + id, "data": map[string]any{"transferred": p.done.Load(), "total": p.total}})
				}
			}
			go func() {
				err := copyTree(src, dst, from, to, p)
				if err != nil {
					ws.send(map[string]any{"id": "transfer:err:" + id, "error": map[string]any{"message": err.Error()}})
					return
				}
				p.emit(true)
				ws.send(map[string]any{"id": "transfer:end:" + id, "data": map[string]any{"transferred": p.done.Load(), "size": p.total}})
			}()
		case "transfer-func":
			if p == nil {
				return
			}
			switch str(msg, "func") {
			case "pause":
				p.pausing.Store(true)
			case "resume":
				p.pausing.Store(false)
			case "destroy":
				cancel()
			}
		}
	})
}
