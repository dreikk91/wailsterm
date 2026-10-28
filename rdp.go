package main

import (
	"crypto/tls"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// RDP is electerm's rdp-proxy.js: an RDCleanPath proxy for the IronRDP WASM client in the UI.
//   1. the client sends an RDCleanPath request (DER) with the destination and its X.224 request
//   2. we connect, forward the X.224 request and read the server's X.224 confirm
//   3. we upgrade the connection to TLS and return the confirm plus the server certificates
//   4. we relay the socket to the TLS stream; IronRDP does the rest of RDP itself

const rdCleanPathVersion = 3390

// rdCleanPathPDU follows IronRDP's RDCleanPathPdu (explicit context tags; [8] is unused).
type rdCleanPathPDU struct {
	Version       int      `asn1:"explicit,tag:0"`
	Destination   string   `asn1:"explicit,tag:2,optional,utf8"`
	ProxyAuth     string   `asn1:"explicit,tag:3,optional,utf8"`
	ServerAuth    string   `asn1:"explicit,tag:4,optional,utf8"`
	Preconnection string   `asn1:"explicit,tag:5,optional,utf8"`
	X224          []byte   `asn1:"explicit,tag:6,optional"`
	CertChain     [][]byte `asn1:"explicit,tag:7,optional"`
	ServerAddr    string   `asn1:"explicit,tag:9,optional,utf8"`
}

type rdCleanPathError struct {
	Code       int `asn1:"explicit,tag:0"`
	HTTPStatus int `asn1:"explicit,tag:1,optional"`
}

type rdCleanPathErrorPDU struct {
	Version int              `asn1:"explicit,tag:0"`
	Error   rdCleanPathError `asn1:"explicit,tag:1"`
}

func parseRDCleanPathRequest(b []byte) (*rdCleanPathPDU, error) {
	var p rdCleanPathPDU
	if _, err := asn1.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("RDCleanPath request: %w", err)
	}
	if p.Version != rdCleanPathVersion {
		return nil, fmt.Errorf("unsupported RDCleanPath version %d", p.Version)
	}
	if p.Destination == "" || len(p.X224) == 0 {
		return nil, errors.New("RDCleanPath request without destination or X.224 PDU")
	}
	return &p, nil
}

// readTPKT reads exactly one TPKT-framed PDU (the X.224 connection confirm).
func readTPKT(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("RDP server closed the connection before the X.224 confirm: %w", err)
	}
	if hdr[0] != 3 {
		return nil, fmt.Errorf("invalid TPKT version %d in X.224 confirm", hdr[0])
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	if n < 4 {
		return nil, errors.New("invalid TPKT length")
	}
	pdu := make([]byte, n)
	copy(pdu, hdr)
	_, err := io.ReadFull(r, pdu[4:])
	return pdu, err
}

// rdpTLS accepts what RDP servers present: self-signed certificates and, on old Windows,
// TLS 1.0 with legacy suites (electerm falls back to node-forge for those).
// ponytail: the certificate is not verified, as in electerm; IronRDP binds CredSSP to the
// presented key, but there is no trust prompt yet.
func rdpTLS(host string) *tls.Config {
	var suites []uint16
	for _, s := range append(tls.CipherSuites(), tls.InsecureCipherSuites()...) {
		suites = append(suites, s.ID)
	}
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, CipherSuites: suites}
	if net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	return cfg
}

func rdpHandshake(t *hopTarget, req *rdCleanPathPDU) (*tls.Conn, []byte, error) {
	host, port, err := net.SplitHostPort(req.Destination)
	if err != nil {
		host, port = req.Destination, "3389"
	}
	addr := net.JoinHostPort(host, port)
	conn, err := t.dialAddr(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("connection failed: %w", err)
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write(req.X224); err != nil {
		conn.Close()
		return nil, nil, err
	}
	confirm, err := readTPKT(conn)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	tc := tls.Client(conn, rdpTLS(host))
	if err := tc.Handshake(); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("TLS handshake with %s: %w", addr, err)
	}
	conn.SetDeadline(time.Time{})
	var chain [][]byte
	for _, c := range tc.ConnectionState().PeerCertificates {
		chain = append(chain, c.Raw)
	}
	resp, err := asn1.Marshal(rdCleanPathPDU{Version: rdCleanPathVersion, X224: confirm, CertChain: chain, ServerAddr: addr})
	if err != nil {
		tc.Close()
		return nil, nil, err
	}
	return tc, resp, nil
}

func (s *Server) handleRDP(w http.ResponseWriter, r *http.Request) {
	t, ws, done := takeTarget(w, r)
	if t == nil {
		return
	}
	defer done()
	ctx := r.Context()
	_, first, err := ws.c.Read(ctx)
	if err != nil {
		return
	}
	fail := func(err error) {
		log.Printf("rdp: %v", err)
		pdu, _ := asn1.Marshal(rdCleanPathErrorPDU{Version: rdCleanPathVersion, Error: rdCleanPathError{Code: 1, HTTPStatus: 502}})
		ws.c.Write(ctx, websocket.MessageBinary, pdu)
		ws.c.Close(websocket.StatusNormalClosure, "")
	}
	req, err := parseRDCleanPathRequest(first)
	if err != nil {
		fail(err)
		return
	}
	tc, resp, err := rdpHandshake(t, req)
	if err != nil {
		fail(err)
		return
	}
	defer tc.Close()
	if err := ws.c.Write(ctx, websocket.MessageBinary, resp); err != nil {
		return
	}
	bridge(ctx, ws.c, tc)
}
