package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/bits"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TightVNC 2.x file transfer (ft-common/FTMessage.h). Messages are a UINT32 id whose first
// byte 0xFC doubles as the RFB message type; paths are unix-like ("/C:/dir"), "/" lists drives.
const (
	ftFileListReq      = 0xFC000102
	ftFileListReply    = 0xFC000103
	ftUploadStartReq   = 0xFC000106
	ftUploadStartReply = 0xFC000107
	ftUploadDataReq    = 0xFC000108
	ftUploadDataReply  = 0xFC000109
	ftUploadEndReq     = 0xFC00010A
	ftUploadEndReply   = 0xFC00010B
	ftDownloadStartReq = 0xFC00010C
	ftDownloadStartRep = 0xFC00010D
	ftDownloadDataReq  = 0xFC00010E
	ftDownloadDataRep  = 0xFC00010F
	ftDownloadEndReply = 0xFC000110
	ftMkdirReq         = 0xFC000111
	ftMkdirReply       = 0xFC000112
	ftRemoveReq        = 0xFC000113
	ftRemoveReply      = 0xFC000114
	ftRenameReq        = 0xFC000115
	ftRenameReply      = 0xFC000116
	ftFailedReply      = 0xFC000119

	ftChunk = 64 * 1024
)

// vncFS is a separate shared VNC connection that never asks for screen updates, so only
// file transfer replies (plus bell/clipboard, skipped) come back. One request at a time:
// every method holds mu; Open/Create hold it until Close, like ftpFS.
type vncFS struct {
	mu sync.Mutex
	c  net.Conn
	r  *bufio.Reader
}

// dialVNCFT connects like the VNC tab: through its jump hosts while the tab is open,
// otherwise directly or via the bookmark's proxy.
func dialVNCFT(o opts) (*vncFS, error) {
	timeout := time.Duration(o.num("readyTimeout", 50000)) * time.Millisecond
	hopMu.Lock()
	t := hopTargets[o.str("id")]
	hopMu.Unlock()
	var c net.Conn
	var err error
	if t != nil {
		c, err = t.dial(5900)
	} else {
		c, err = dialVia(o.str("proxy"), net.JoinHostPort(o.str("host"), strconv.Itoa(o.num("port", 5900))), timeout)
	}
	if err != nil {
		return nil, err
	}
	v := &vncFS{c: c, r: bufio.NewReader(c)}
	c.SetDeadline(time.Now().Add(timeout))
	if err := v.handshake(o.str("password")); err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return v, nil
}

func (v *vncFS) handshake(password string) error {
	ver := make([]byte, 12)
	if _, err := io.ReadFull(v.r, ver); err != nil {
		return err
	}
	if string(ver) < "RFB 003.007\n" {
		return errors.New("VNC file transfer needs a TightVNC 2.x server")
	}
	io.WriteString(v.c, "RFB 003.008\n")
	n, err := v.u8()
	if err != nil {
		return err
	}
	if n == 0 {
		return v.reason()
	}
	types := make([]byte, n)
	if _, err := io.ReadFull(v.r, types); err != nil {
		return err
	}
	if bytes.IndexByte(types, 16) < 0 {
		return errors.New("VNC server has no TightVNC extensions, so no file transfer")
	}
	v.c.Write([]byte{16})
	if n, err := v.u32(); err != nil { // tunnel caps: answer NOTUNNEL
		return err
	} else if n > 0 {
		if err := v.skip(int(n) * 16); err != nil {
			return err
		}
		v.c.Write([]byte{0, 0, 0, 0})
	}
	nauth, err := v.u32()
	if err != nil {
		return err
	}
	auth := uint32(1) // none
	for range nauth {
		code, err := v.u32()
		if err != nil {
			return err
		}
		if err := v.skip(12); err != nil {
			return err
		}
		if code == 2 {
			auth = 2
		}
	}
	if nauth > 0 {
		v.c.Write(binary.BigEndian.AppendUint32(nil, auth))
	}
	if auth == 2 {
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(v.r, challenge); err != nil {
			return err
		}
		v.c.Write(vncAuthResponse(password, challenge))
	}
	if res, err := v.u32(); err != nil {
		return err
	} else if res != 0 {
		return v.reason()
	}
	v.c.Write([]byte{1}) // ClientInit: shared, keep the screen session
	if err := v.skip(20); err != nil {
		return err
	}
	nameLen, err := v.u32()
	if err != nil {
		return err
	}
	if err := v.skip(int(nameLen)); err != nil {
		return err
	}
	counts := make([]byte, 8)
	if _, err := io.ReadFull(v.r, counts); err != nil {
		return err
	}
	ncaps := int(binary.BigEndian.Uint16(counts)) + int(binary.BigEndian.Uint16(counts[2:])) + int(binary.BigEndian.Uint16(counts[4:]))
	ok := false
	for range ncaps {
		code, err := v.u32()
		if err != nil {
			return err
		}
		if err := v.skip(12); err != nil {
			return err
		}
		ok = ok || code == ftFileListReq
	}
	if !ok {
		return errors.New("VNC server does not offer file transfer (disabled, or not TightVNC 2.x)")
	}
	return nil
}

// vncAuthResponse is RFB VNC authentication: DES with the bit-reversed password as key.
func vncAuthResponse(password string, challenge []byte) []byte {
	key := make([]byte, 8)
	copy(key, password)
	for i, b := range key {
		key[i] = bits.Reverse8(b)
	}
	c, _ := des.NewCipher(key)
	out := make([]byte, 16)
	c.Encrypt(out, challenge)
	c.Encrypt(out[8:], challenge[8:])
	return out
}

func (v *vncFS) u8() (byte, error) { return v.r.ReadByte() }

func (v *vncFS) u32() (uint32, error) {
	b := make([]byte, 4)
	_, err := io.ReadFull(v.r, b)
	return binary.BigEndian.Uint32(b), err
}

func (v *vncFS) u64() (uint64, error) {
	b := make([]byte, 8)
	_, err := io.ReadFull(v.r, b)
	return binary.BigEndian.Uint64(b), err
}

func (v *vncFS) skip(n int) error {
	_, err := v.r.Discard(n)
	return err
}

func (v *vncFS) str() (string, error) {
	n, err := v.u32()
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	_, err = io.ReadFull(v.r, b)
	return string(b), err
}

func (v *vncFS) reason() error {
	s, err := v.str()
	if err != nil {
		return err
	}
	return errors.New(s)
}

// ftMsg builds a request: id, then fields (uint8/uint16/uint32/uint64, string as StringUTF8, []byte raw).
func ftMsg(id uint32, fields ...any) []byte {
	b := binary.BigEndian.AppendUint32(nil, id)
	for _, f := range fields {
		switch f := f.(type) {
		case uint8:
			b = append(b, f)
		case uint16:
			b = binary.BigEndian.AppendUint16(b, f)
		case uint32:
			b = binary.BigEndian.AppendUint32(b, f)
		case uint64:
			b = binary.BigEndian.AppendUint64(b, f)
		case string:
			b = append(binary.BigEndian.AppendUint32(b, uint32(len(f))), f...)
		case []byte:
			b = append(b, f...)
		}
	}
	return b
}

// call sends a request and returns the id of the reply; its body is left in v.r.
func (v *vncFS) call(req []byte, want ...uint32) (uint32, error) {
	if _, err := v.c.Write(req); err != nil {
		return 0, err
	}
	for {
		t, err := v.u8()
		if err != nil {
			return 0, err
		}
		switch t {
		case 2: // Bell
			continue
		case 3: // ServerCutText
			if err := v.skip(3); err != nil {
				return 0, err
			}
			n, err := v.u32()
			if err != nil {
				return 0, err
			}
			if err := v.skip(int(n)); err != nil {
				return 0, err
			}
			continue
		case 0xFC:
		default:
			return 0, fmt.Errorf("unexpected VNC message %d", t)
		}
		b := []byte{t, 0, 0, 0}
		if _, err := io.ReadFull(v.r, b[1:]); err != nil {
			return 0, err
		}
		id := binary.BigEndian.Uint32(b)
		if id == ftFailedReply {
			return 0, v.reason()
		}
		for _, w := range want {
			if id == w {
				return id, nil
			}
		}
		return 0, fmt.Errorf("unexpected file transfer reply %#x", id)
	}
}

func (v *vncFS) do(req []byte, want uint32) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, err := v.call(req, want)
	return err
}

// block reads a CompressedBlock.
func (v *vncFS) block() ([]byte, error) {
	level, err := v.u8()
	if err != nil {
		return nil, err
	}
	size, err := v.u32()
	if err != nil {
		return nil, err
	}
	raw, err := v.u32()
	if err != nil {
		return nil, err
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(v.r, b); err != nil || level == 0 {
		return b, err
	}
	z, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out := make([]byte, raw)
	_, err = io.ReadFull(z, out)
	return out, err
}

type vncInfo struct {
	name  string
	size  int64
	mtime time.Time
	dir   bool
}

func (i vncInfo) Name() string       { return i.name }
func (i vncInfo) Size() int64        { return i.size }
func (i vncInfo) ModTime() time.Time { return i.mtime }
func (i vncInfo) IsDir() bool        { return i.dir }
func (i vncInfo) Sys() any           { return nil }
func (i vncInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}

func vncPath(p string) string {
	if p == "" || p == "." {
		return "/"
	}
	return path.Clean("/" + strings.ReplaceAll(p, `\`, "/"))
}

func (v *vncFS) ReadDir(p string) ([]os.FileInfo, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.call(ftMsg(ftFileListReq, uint8(0), vncPath(p)), ftFileListReply); err != nil {
		return nil, err
	}
	b, err := v.block()
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, io.ErrUnexpectedEOF
	}
	n := binary.BigEndian.Uint32(b)
	b = b[4:]
	out := []os.FileInfo{}
	for range n {
		if len(b) < 22 {
			return nil, io.ErrUnexpectedEOF
		}
		size, mtime := binary.BigEndian.Uint64(b), binary.BigEndian.Uint64(b[8:])
		flags := binary.BigEndian.Uint16(b[16:]) // the server writes UINT16, not the documented UINT8
		l := int(binary.BigEndian.Uint32(b[18:]))
		if len(b) < 22+l {
			return nil, io.ErrUnexpectedEOF
		}
		out = append(out, vncInfo{name: string(b[22 : 22+l]), size: int64(size), mtime: time.UnixMilli(int64(mtime)), dir: flags&1 != 0})
		b = b[22+l:]
	}
	return out, nil
}

func (v *vncFS) Stat(p string) (os.FileInfo, error) {
	p = vncPath(p)
	if p == "/" {
		return vncInfo{name: "/", dir: true}, nil
	}
	list, err := v.ReadDir(path.Dir(p))
	if err != nil {
		return nil, err
	}
	for _, fi := range list {
		if fi.Name() == path.Base(p) {
			return fi, nil
		}
	}
	return nil, os.ErrNotExist
}

type vncReader struct {
	v    *vncFS
	buf  []byte
	done bool
}

func (r *vncReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.done {
			return 0, io.EOF
		}
		id, err := r.v.call(ftMsg(ftDownloadDataReq, uint8(0), uint32(ftChunk)), ftDownloadDataRep, ftDownloadEndReply)
		if err != nil {
			return 0, err
		}
		if id == ftDownloadEndReply { // UINT8 flags, UINT64 mtime
			r.done = true
			if err := r.v.skip(9); err != nil {
				return 0, err
			}
			continue
		}
		if r.buf, err = r.v.block(); err != nil {
			return 0, err
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *vncReader) Close() error {
	r.v.mu.Unlock()
	return nil
}

func (v *vncFS) Open(p string) (io.ReadCloser, error) {
	v.mu.Lock()
	if _, err := v.call(ftMsg(ftDownloadStartReq, vncPath(p), uint64(0)), ftDownloadStartRep); err != nil {
		v.mu.Unlock()
		return nil, err
	}
	return &vncReader{v: v}, nil
}

type vncWriter struct{ v *vncFS }

func (w vncWriter) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += ftChunk {
		chunk := p[off:min(off+ftChunk, len(p))]
		n := uint32(len(chunk))
		if _, err := w.v.call(ftMsg(ftUploadDataReq, uint8(0), n, n, chunk), ftUploadDataReply); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

func (w vncWriter) Close() error {
	defer w.v.mu.Unlock()
	_, err := w.v.call(ftMsg(ftUploadEndReq, uint16(0), uint64(time.Now().UnixMilli())), ftUploadEndReply)
	return err
}

func (v *vncFS) Create(p string) (io.WriteCloser, error) {
	v.mu.Lock()
	if _, err := v.call(ftMsg(ftUploadStartReq, vncPath(p), uint8(1), uint64(0)), ftUploadStartReply); err != nil {
		v.mu.Unlock()
		return nil, err
	}
	return vncWriter{v}, nil
}

func (v *vncFS) MkdirAll(p string) error {
	p = vncPath(p)
	if fi, err := v.Stat(p); err == nil && fi.IsDir() {
		return nil
	}
	if parent := path.Dir(p); parent != "/" {
		if err := v.MkdirAll(parent); err != nil {
			return err
		}
	}
	return v.do(ftMsg(ftMkdirReq, p), ftMkdirReply)
}

func (v *vncFS) Join(elem ...string) string { return path.Join(elem...) }

func (v *vncFS) Remove(p string) error { return v.do(ftMsg(ftRemoveReq, vncPath(p)), ftRemoveReply) }

func (v *vncFS) RemoveAll(p string) error {
	fi, err := v.Stat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		list, err := v.ReadDir(p)
		if err != nil {
			return err
		}
		for _, c := range list {
			if err := v.RemoveAll(path.Join(vncPath(p), c.Name())); err != nil {
				return err
			}
		}
	}
	return v.Remove(p)
}

func (v *vncFS) Rename(from, to string) error {
	return v.do(ftMsg(ftRenameReq, vncPath(from), vncPath(to)), ftRenameReply)
}

func (v *vncFS) Getwd() (string, error) { return "/", nil }

func (v *vncFS) RealPath(p string) (string, error) { return vncPath(p), nil }

func (v *vncFS) Close() error { return v.c.Close() }
