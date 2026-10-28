package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// der* mirror electerm's hand-written encoder in rdp-proxy.js, to check byte compatibility.
func derLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for ; n > 0; n >>= 8 {
		b = append([]byte{byte(n)}, b...)
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}
func derWrap(tag byte, c []byte) []byte { return append(append([]byte{tag}, derLen(len(c))...), c...) }
func derInt(v int) []byte {
	var b []byte
	for ; v > 0; v >>= 8 {
		b = append([]byte{byte(v)}, b...)
	}
	if len(b) == 0 || b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return derWrap(0x02, b)
}
func ctx(n int, c []byte) []byte { return derWrap(0xa0+byte(n), c) }

func TestRDCleanPathDER(t *testing.T) {
	x224 := []byte{3, 0, 0, 11, 6, 0xe0, 0, 0, 0, 0, 0}
	// request as IronRDP sends it: version, destination, x224 pdu
	req := derWrap(0x30, bytes.Join([][]byte{
		ctx(0, derInt(rdCleanPathVersion)), ctx(2, derWrap(0x0c, []byte("10.0.0.5:3389"))), ctx(6, derWrap(0x04, x224)),
	}, nil))
	p, err := parseRDCleanPathRequest(req)
	if err != nil || p.Destination != "10.0.0.5:3389" || !bytes.Equal(p.X224, x224) {
		t.Fatalf("parse = %+v, %v", p, err)
	}

	certs := [][]byte{{1, 2, 3}, bytes.Repeat([]byte{9}, 300)}
	got, _ := asn1.Marshal(rdCleanPathPDU{Version: rdCleanPathVersion, X224: x224, CertChain: certs, ServerAddr: "10.0.0.5:3389"})
	want := derWrap(0x30, bytes.Join([][]byte{
		ctx(0, derInt(rdCleanPathVersion)), ctx(6, derWrap(0x04, x224)),
		ctx(7, derWrap(0x30, append(derWrap(0x04, certs[0]), derWrap(0x04, certs[1])...))),
		ctx(9, derWrap(0x0c, []byte("10.0.0.5:3389"))),
	}, nil))
	if !bytes.Equal(got, want) {
		t.Fatalf("response DER differs from electerm's encoding\n got %x\nwant %x", got, want)
	}

	gotErr, _ := asn1.Marshal(rdCleanPathErrorPDU{Version: rdCleanPathVersion, Error: rdCleanPathError{Code: 1, HTTPStatus: 502}})
	wantErr := derWrap(0x30, append(ctx(0, derInt(rdCleanPathVersion)), ctx(1, derWrap(0x30, append(ctx(0, derInt(1)), ctx(1, derInt(502))...)))...))
	if !bytes.Equal(gotErr, wantErr) {
		t.Fatalf("error DER differs\n got %x\nwant %x", gotErr, wantErr)
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rdp-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestRDPProxy(t *testing.T) {
	cert := selfSigned(t)
	confirm := []byte{3, 0, 0, 19, 14, 0xd0, 0, 0, 0x12, 0x34, 0, 2, 0, 8, 0, 1, 0, 0, 0}
	// fake RDP server: X.224 confirm, then TLS, then echo
	rdp, _ := net.Listen("tcp", "127.0.0.1:0")
	defer rdp.Close()
	go func() {
		c, err := rdp.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if _, err := readTPKT(c); err != nil {
			return
		}
		c.Write(confirm)
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}})
		io.Copy(tc, tc)
	}()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	s := &Server{token: "tok", addr: ln.Addr().String(), terms: map[string]*Term{}}
	go http.Serve(ln, s.routes())
	tgt, _ := prepareHops(opts{"host": "127.0.0.1"}, nil)
	hopMu.Lock()
	hopTargets["r1"] = tgt
	hopMu.Unlock()

	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(cctx, "ws://"+s.addr+"/rdp/r1?token=tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	req, _ := asn1.Marshal(rdCleanPathPDU{Version: rdCleanPathVersion, Destination: rdp.Addr().String(),
		X224: []byte{3, 0, 0, 11, 6, 0xe0, 0, 0, 0, 0, 0}})
	c.Write(cctx, websocket.MessageBinary, req)
	_, b, err := c.Read(cctx)
	if err != nil {
		t.Fatal(err)
	}
	var resp rdCleanPathPDU
	if _, err := asn1.Unmarshal(b, &resp); err != nil {
		t.Fatalf("response: %v", err)
	}
	if !bytes.Equal(resp.X224, confirm) || len(resp.CertChain) != 1 || !bytes.Equal(resp.CertChain[0], cert.Certificate[0]) {
		t.Fatalf("response = %+v", resp)
	}
	// after the handshake the socket carries plaintext that the proxy sends over TLS
	c.Write(cctx, websocket.MessageBinary, []byte("rdp payload"))
	if _, b, err = c.Read(cctx); err != nil || string(b) != "rdp payload" {
		t.Fatalf("relay = %q, %v", b, err)
	}
}

// TestRDPHandshakeLive checks X.224 + TLS against a real RDP server (no credentials are sent).
// Run with RDP_TEST_ADDR=host:3389.
func TestRDPHandshakeLive(t *testing.T) {
	addr := os.Getenv("RDP_TEST_ADDR")
	if addr == "" {
		t.Skip("RDP_TEST_ADDR not set")
	}
	// X.224 Connection Request with RDP_NEG_REQ asking for TLS | CredSSP
	x224 := []byte{3, 0, 0, 19, 14, 0xe0, 0, 0, 0, 0, 0, 1, 0, 8, 0, 3, 0, 0, 0}
	tgt, _ := prepareHops(opts{}, nil)
	tc, resp, err := rdpHandshake(tgt, &rdCleanPathPDU{Version: rdCleanPathVersion, Destination: addr, X224: x224})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	var p rdCleanPathPDU
	asn1.Unmarshal(resp, &p)
	st := tc.ConnectionState()
	t.Logf("X.224 confirm % x; TLS %s %s; %d cert(s), CN=%s", p.X224, tls.VersionName(st.Version),
		tls.CipherSuiteName(st.CipherSuite), len(p.CertChain), st.PeerCertificates[0].Subject.CommonName)
	if len(p.CertChain) == 0 || p.X224[5] != 0xd0 {
		t.Fatal("unexpected handshake result")
	}
}
