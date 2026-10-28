package main

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

type Listing struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Sep     string      `json:"sep"`
	Entries []FileEntry `json:"entries"`
}

// fsys is what the file manager needs from either side: the local disk or an SFTP server.
type fsys interface {
	ReadDir(p string) ([]os.FileInfo, error)
	Stat(p string) (os.FileInfo, error)
	Open(p string) (io.ReadCloser, error)
	Create(p string) (io.WriteCloser, error)
	MkdirAll(p string) error
	Rename(from, to string) error
	RemoveAll(p string) error
	Home() (string, error)
	Join(elem ...string) string
	Dir(p string) string
	Sep() string
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
func (localFS) Rename(from, to string) error            { return os.Rename(from, to) }
func (localFS) RemoveAll(p string) error                { return os.RemoveAll(p) }
func (localFS) Home() (string, error)                   { return os.UserHomeDir() }
func (localFS) Join(elem ...string) string              { return filepath.Join(elem...) }
func (localFS) Dir(p string) string                     { return filepath.Dir(p) }
func (localFS) Sep() string                             { return string(filepath.Separator) }

type remoteFS struct{ *sftp.Client }

func (r remoteFS) Open(p string) (io.ReadCloser, error)    { return r.Client.Open(p) }
func (r remoteFS) Create(p string) (io.WriteCloser, error) { return r.Client.Create(p) }
func (r remoteFS) Home() (string, error)                   { return r.Getwd() }
func (remoteFS) Join(elem ...string) string                { return path.Join(elem...) }
func (remoteFS) Dir(p string) string                       { return path.Dir(p) }
func (remoteFS) Sep() string                               { return "/" }

// fs returns the local disk for id "" and the session's SFTP server otherwise.
// The SFTP client is opened once per session and dies with the SSH connection.
func (t *TermService) fs(id string) (fsys, error) {
	if id == "" {
		return localFS{}, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[id]
	if s == nil || s.client == nil {
		return nil, errors.New("not an SSH session")
	}
	if s.sftp == nil {
		c, err := sftp.NewClient(s.client)
		if err != nil {
			return nil, err
		}
		s.sftp = c
	}
	return remoteFS{s.sftp}, nil
}

// FsList lists dir on the local disk (id "") or the session's server; empty dir means home.
func (t *TermService) FsList(id, dir string) (Listing, error) {
	f, err := t.fs(id)
	if err != nil {
		return Listing{}, err
	}
	if dir == "" {
		if dir, err = f.Home(); err != nil {
			return Listing{}, err
		}
	}
	infos, err := f.ReadDir(dir)
	if err != nil {
		return Listing{}, err
	}
	l := Listing{Path: dir, Parent: f.Dir(dir), Sep: f.Sep(), Entries: make([]FileEntry, 0, len(infos))}
	for _, fi := range infos {
		p := f.Join(dir, fi.Name())
		isDir := fi.IsDir()
		if fi.Mode()&os.ModeSymlink != 0 {
			if st, err := f.Stat(p); err == nil {
				isDir = st.IsDir()
			}
		}
		l.Entries = append(l.Entries, FileEntry{fi.Name(), p, fi.Size(), fi.Mode().String(), fi.ModTime().Unix(), isDir})
	}
	sort.Slice(l.Entries, func(i, j int) bool {
		a, b := l.Entries[i], l.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return a.Name < b.Name
	})
	return l, nil
}

func (t *TermService) FsMkdir(id, p string) error {
	f, err := t.fs(id)
	if err != nil {
		return err
	}
	return f.MkdirAll(p)
}

func (t *TermService) FsRename(id, from, to string) error {
	f, err := t.fs(id)
	if err != nil {
		return err
	}
	return f.Rename(from, to)
}

func (t *TermService) FsRemove(id, p string) error {
	f, err := t.fs(id)
	if err != nil {
		return err
	}
	return f.RemoveAll(p)
}

type Progress struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error"`
	End   bool   `json:"end"`
}

var transferSeq atomic.Int64

// Transfer copies srcPaths (files or directories, recursively) into dstDir.
// upload=true copies local→remote, false copies remote→local. Progress is emitted as "transfer" events.
func (t *TermService) Transfer(id string, upload bool, srcPaths []string, dstDir string) error {
	remote, err := t.fs(id)
	if err != nil {
		return err
	}
	var src, dst fsys = localFS{}, remote
	if !upload {
		src, dst = remote, localFS{}
	}
	p := Progress{ID: id + ":" + time.Now().Format("150405") + ":" + strconv.FormatInt(transferSeq.Add(1), 10)}
	for _, s := range srcPaths {
		p.Total += treeSize(src, s)
	}
	last := time.Now()
	emit := func(force bool) {
		if force || time.Since(last) > 150*time.Millisecond {
			last = time.Now()
			application.Get().Event.Emit("transfer", p)
		}
	}
	for _, s := range srcPaths {
		if err = copyTree(src, dst, s, dst.Join(dstDir, pathBase(s)), &p, emit); err != nil {
			break
		}
	}
	if err != nil {
		p.Error = err.Error()
	}
	p.End = true
	emit(true)
	return err
}

// pathBase works for both separators since local paths on Windows use '\' and remote ones '/'.
func pathBase(p string) string { return path.Base(filepath.ToSlash(p)) }

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

type countingWriter struct {
	w    io.Writer
	p    *Progress
	emit func(bool)
}

func (c countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.p.Done += int64(n)
	c.emit(false)
	return n, err
}

func copyTree(src, dst fsys, from, to string, p *Progress, emit func(bool)) error {
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
			if err := copyTree(src, dst, src.Join(from, c.Name()), dst.Join(to, c.Name()), p, emit); err != nil {
				return err
			}
		}
		return nil
	}
	p.Name = fi.Name()
	r, err := src.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := dst.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(countingWriter{w, p, emit}, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
