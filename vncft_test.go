package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

// fakeTightVNC mimics TightVNC 2.x RfbInitializer + FileTransferRequestHandler over an in-memory tree.
func fakeTightVNC(t *testing.T, password string) (string, map[string][]byte) {
	files := map[string][]byte{"/C:": nil, "/C:/a.txt": []byte("hello")} // nil = directory
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeTight(c, password, files)
		}
	}()
	return ln.Addr().String(), files
}

func serveFakeTight(c net.Conn, password string, files map[string][]byte) {
	defer c.Close()
	r := bufio.NewReader(c)
	u32 := func() uint32 { b := make([]byte, 4); io.ReadFull(r, b); return binary.BigEndian.Uint32(b) }
	u64 := func() uint64 { b := make([]byte, 8); io.ReadFull(r, b); return binary.BigEndian.Uint64(b) }
	str := func() string { b := make([]byte, u32()); io.ReadFull(r, b); return string(b) }
	cap := func(code uint32) []byte { return append(binary.BigEndian.AppendUint32(nil, code), "TGHTFTCFLRST"...) }
	io.WriteString(c, "RFB 003.008\n")
	r.Discard(12)
	c.Write([]byte{2, 2, 16})
	if b, _ := r.ReadByte(); b != 16 {
		return
	}
	c.Write(append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, append(binary.BigEndian.AppendUint32(nil, 2), "STDVVNCAUTH_"...)...))
	u32()
	challenge := bytes.Repeat([]byte{7}, 16)
	c.Write(challenge)
	resp := make([]byte, 16)
	io.ReadFull(r, resp)
	if !bytes.Equal(resp, vncAuthResponse(password, challenge)) {
		c.Write(ftMsg(1, "Authentication failed"))
		return
	}
	c.Write([]byte{0, 0, 0, 0})
	r.ReadByte()                                                      // ClientInit
	c.Write(make([]byte, 20))                                         // ServerInit
	c.Write(append(binary.BigEndian.AppendUint32(nil, 4), "desk"...)) // name
	c.Write([]byte{0, 0, 0, 1, 0, 0, 0, 0})
	c.Write(cap(ftFileListReq))
	c.Write([]byte{3, 0, 0, 0, 0, 0, 0, 2, 'x', 'y'}) // a clipboard update to skip
	var upload, download string
	var dlOff int
	fail := func(msg string) { c.Write(ftMsg(ftFailedReply, msg)) }
	for {
		id := u32()
		switch id {
		case ftFileListReq:
			r.ReadByte()
			dir := str()
			var body []byte
			n := uint32(0)
			prefix := strings.TrimSuffix(dir, "/") + "/"
			for p, d := range files {
				name := strings.TrimPrefix(p, prefix)
				if p != dir && strings.HasPrefix(p, prefix) && !strings.Contains(name, "/") {
					flags := uint16(0)
					if d == nil {
						flags = 1
					}
					body = binary.BigEndian.AppendUint64(body, uint64(len(d)))
					body = binary.BigEndian.AppendUint64(body, 1700000000000)
					body = binary.BigEndian.AppendUint16(body, flags)
					body = append(binary.BigEndian.AppendUint32(body, uint32(len(name))), name...)
					n++
				}
			}
			body = append(binary.BigEndian.AppendUint32(nil, n), body...)
			c.Write(ftMsg(ftFileListReply, uint8(0), uint32(len(body)), uint32(len(body)), body))
		case ftUploadStartReq:
			upload = str()
			r.ReadByte()
			u64()
			files[upload] = []byte{}
			c.Write(ftMsg(ftUploadStartReply))
		case ftUploadDataReq:
			r.ReadByte()
			n := u32()
			u32()
			b := make([]byte, n)
			io.ReadFull(r, b)
			files[upload] = append(files[upload], b...)
			c.Write(ftMsg(ftUploadDataReply))
		case ftUploadEndReq:
			r.Discard(10)
			c.Write(ftMsg(ftUploadEndReply))
		case ftDownloadStartReq:
			download, dlOff = str(), 0
			u64()
			if _, ok := files[download]; !ok {
				fail("no such file")
				continue
			}
			c.Write(ftMsg(ftDownloadStartRep))
		case ftDownloadDataReq:
			r.ReadByte()
			size := int(u32())
			d := files[download]
			if dlOff >= len(d) {
				c.Write(ftMsg(ftDownloadEndReply, uint8(0), uint64(0)))
				continue
			}
			chunk := d[dlOff:min(dlOff+size, len(d))]
			dlOff += len(chunk)
			c.Write(ftMsg(ftDownloadDataRep, uint8(0), uint32(len(chunk)), uint32(len(chunk)), chunk))
		case ftMkdirReq:
			files[str()] = nil
			c.Write(ftMsg(ftMkdirReply))
		case ftRemoveReq:
			delete(files, str())
			c.Write(ftMsg(ftRemoveReply))
		case ftRenameReq:
			from, to := str(), str()
			files[to] = files[from]
			delete(files, from)
			c.Write(ftMsg(ftRenameReply))
		default:
			return
		}
	}
}

func TestVNCFileTransfer(t *testing.T) {
	addr, files := fakeTightVNC(t, "secret")
	host, port, _ := net.SplitHostPort(addr)
	pn, _ := strconv.Atoi(port)
	o := opts{"host": host, "port": float64(pn), "password": "secret"}
	if _, err := dialVNCFT(opts{"host": host, "port": float64(pn), "password": "wrong"}); err == nil || !strings.Contains(err.Error(), "Authentication failed") {
		t.Fatalf("wrong password: %v", err)
	}
	v, err := dialRemote("vnc", o)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	if root, err := v.ReadDir("/"); err != nil || len(root) != 1 || root[0].Name() != "C:" || !root[0].IsDir() {
		t.Fatalf("drives = %v %v", root, err)
	}
	list, err := v.ReadDir("/C:")
	if err != nil || len(list) != 1 || list[0].Name() != "a.txt" || list[0].Size() != 5 || list[0].IsDir() {
		t.Fatalf("list = %v %v", list, err)
	}
	big := bytes.Repeat([]byte("0123456789"), 20000) // several chunks
	if err := v.MkdirAll("/C:/sub/deep"); err != nil {
		t.Fatal(err)
	}
	if files["/C:/sub"] != nil || files["/C:/sub/deep"] != nil {
		t.Fatal("mkdir did not create directories")
	}
	w, err := v.Create("/C:/sub/b.bin")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(big)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files["/C:/sub/b.bin"], big) {
		t.Fatalf("uploaded %d bytes", len(files["/C:/sub/b.bin"]))
	}
	rd, err := v.Open("/C:/sub/b.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	rd.Close()
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("download %d bytes, %v", len(got), err)
	}
	if _, err := v.Open("/C:/missing"); err == nil || err.Error() != "no such file" {
		t.Fatalf("missing file: %v", err)
	}
	if err := v.Rename("/C:/a.txt", "/C:/sub/a.txt"); err != nil || files["/C:/sub/a.txt"] == nil {
		t.Fatalf("rename: %v", err)
	}
	if fi, err := v.Stat("/C:/sub/a.txt"); err != nil || fi.Size() != 5 {
		t.Fatalf("stat: %v %v", fi, err)
	}
	if err := v.RemoveAll("/C:/sub"); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("left after RemoveAll: %v", files)
	}
}
