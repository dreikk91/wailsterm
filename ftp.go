package main

import (
	"crypto/tls"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

// ftpFS is electerm's session-ftp.js on github.com/jlaffaye/ftp. One control connection
// runs one command at a time, so every method holds mu; Open/Create hold it until Close.
type ftpFS struct {
	mu sync.Mutex
	c  *ftp.ServerConn
}

// dialFTP uses electerm's FTP bookmark fields: host, port, user, password, secure (explicit FTPS).
func dialFTP(o opts) (*ftpFS, error) {
	timeout := time.Duration(o.num("readyTimeout", 50000)) * time.Millisecond
	host := o.str("host")
	addr := net.JoinHostPort(host, strconv.Itoa(o.num("port", 21)))
	dialOpts := []ftp.DialOption{ftp.DialWithTimeout(timeout)}
	if o["secure"] == true {
		dialOpts = append(dialOpts, ftp.DialWithExplicitTLS(&tls.Config{ServerName: host}))
	}
	if p := o.str("proxy"); p != "" {
		dialOpts = append(dialOpts, ftp.DialWithDialFunc(func(_, a string) (net.Conn, error) { return dialVia(p, a, timeout) }))
	}
	c, err := ftp.Dial(addr, dialOpts...)
	if err != nil {
		return nil, err
	}
	user := o.str("user")
	if user == "" {
		user = o.str("username")
	}
	if user == "" {
		user = "anonymous"
	}
	if err := c.Login(user, o.str("password")); err != nil {
		c.Quit()
		return nil, err
	}
	return &ftpFS{c: c}, nil
}

type ftpInfo struct{ e *ftp.Entry }

func (i ftpInfo) Name() string       { return i.e.Name }
func (i ftpInfo) Size() int64        { return int64(i.e.Size) }
func (i ftpInfo) ModTime() time.Time { return i.e.Time }
func (i ftpInfo) IsDir() bool        { return i.e.Type == ftp.EntryTypeFolder }
func (i ftpInfo) Sys() any           { return nil }
func (i ftpInfo) Mode() fs.FileMode {
	switch i.e.Type {
	case ftp.EntryTypeFolder:
		return fs.ModeDir | 0o755
	case ftp.EntryTypeLink:
		return fs.ModeSymlink | 0o777
	}
	return 0o644
}

func (f *ftpFS) ReadDir(p string) ([]os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := f.c.List(p)
	out := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.Name != "." && e.Name != ".." {
			out = append(out, ftpInfo{e})
		}
	}
	return out, err
}

func (f *ftpFS) Stat(p string) (os.FileInfo, error) {
	if p == "/" || p == "" {
		return ftpInfo{&ftp.Entry{Name: "/", Type: ftp.EntryTypeFolder}}, nil
	}
	f.mu.Lock()
	if e, err := f.c.GetEntry(p); err == nil {
		f.mu.Unlock()
		return ftpInfo{e}, nil
	}
	f.mu.Unlock()
	// servers without MLST: find the name in the parent listing, as electerm does
	list, err := f.ReadDir(path.Dir(p))
	if err != nil {
		return nil, err
	}
	for _, fi := range list {
		if fi.Name() == path.Base(p) {
			return fi, nil
		}
	}
	return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
}

type unlockOnClose struct {
	io.Reader
	close func() error
}

func (u unlockOnClose) Close() error { return u.close() }

func (f *ftpFS) Open(p string) (io.ReadCloser, error) {
	f.mu.Lock()
	r, err := f.c.Retr(p)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	return unlockOnClose{r, func() error { defer f.mu.Unlock(); return r.Close() }}, nil
}

type ftpWriter struct {
	*io.PipeWriter
	done chan error
	f    *ftpFS
}

func (w ftpWriter) Close() error {
	defer w.f.mu.Unlock()
	w.PipeWriter.Close()
	return <-w.done
}

func (f *ftpFS) Create(p string) (io.WriteCloser, error) {
	f.mu.Lock()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := f.c.Stor(p, pr)
		pr.CloseWithError(err)
		done <- err
	}()
	return ftpWriter{pw, done, f}, nil
}

func (f *ftpFS) MkdirAll(p string) error {
	if fi, err := f.Stat(p); err == nil && fi.IsDir() {
		return nil
	}
	f.mu.Lock()
	cur := ""
	if strings.HasPrefix(p, "/") {
		cur = "/"
	}
	var err error
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		cur = path.Join(cur, part)
		err = f.c.MakeDir(cur) // fails for existing parents; only the last result matters
	}
	f.mu.Unlock()
	if fi, serr := f.Stat(p); serr == nil && fi.IsDir() {
		return nil
	}
	return err
}

func (f *ftpFS) Join(elem ...string) string { return path.Join(elem...) }

func (f *ftpFS) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c.Delete(p)
}

func (f *ftpFS) RemoveAll(p string) error {
	fi, err := f.Stat(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fi.IsDir() {
		return f.c.RemoveDirRecur(p)
	}
	return f.c.Delete(p)
}

func (f *ftpFS) Rename(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c.Rename(from, to)
}

func (f *ftpFS) Getwd() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c.CurrentDir()
}

// RealPath resolves p by changing into it, like session-ftp.js realpath.
func (f *ftpFS) RealPath(p string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == "" || p == "." {
		return f.c.CurrentDir()
	}
	back, err := f.c.CurrentDir()
	if err != nil {
		return "", err
	}
	if err := f.c.ChangeDir(p); err != nil {
		return "", err
	}
	real, err := f.c.CurrentDir()
	f.c.ChangeDir(back)
	return real, err
}

func (f *ftpFS) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c.Quit()
}
