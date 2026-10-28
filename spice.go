package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// SPICE is session-spice.js + spice-proxy.js: the browser spice client opens one socket per
// SPICE channel, each bridged to its own TCP (optionally TLS) connection to the server.

type spiceTarget struct {
	o        opts
	channels int
}

var (
	spiceMu      sync.Mutex
	spiceTargets = map[string]*spiceTarget{}
)

// parseSubject reads a spice-gtk host-subject such as "OU=PVE Cluster Node,O=Proxmox,CN=pve1";
// a backslash escapes "\" and ",". Returns nil for a malformed string.
func parseSubject(s string) [][2]string {
	var pairs [][2]string
	var key, value strings.Builder
	inValue := false
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c == '\\' && i+1 < len(r) && (r[i+1] == '\\' || r[i+1] == ',') {
			i++
			if inValue {
				value.WriteRune(r[i])
			} else {
				key.WriteRune(r[i])
			}
			continue
		}
		if !inValue {
			switch c {
			case ' ':
			case '=':
				inValue = true
			case ',':
				return nil
			default:
				key.WriteRune(c)
			}
			continue
		}
		if c == ',' {
			if value.Len() == 0 {
				return nil
			}
			pairs = append(pairs, [2]string{strings.ToUpper(key.String()), value.String()})
			key.Reset()
			value.Reset()
			inValue = false
			continue
		}
		value.WriteRune(c)
	}
	if !inValue || key.Len() == 0 || value.Len() == 0 {
		return nil
	}
	return append(pairs, [2]string{strings.ToUpper(key.String()), value.String()})
}

// nodeSubjectKeys are the short names Node puts in cert.subject.
var nodeSubjectKeys = map[string]string{
	"2.5.4.3": "CN", "2.5.4.6": "C", "2.5.4.7": "L", "2.5.4.8": "ST", "2.5.4.10": "O", "2.5.4.11": "OU",
	"1.2.840.113549.1.9.1": "emailAddress",
}

// subjectMatches requires every attribute of the certificate subject to be named, and matched, by expected.
func subjectMatches(expected string, cert *x509.Certificate) bool {
	pairs := parseSubject(expected)
	if len(pairs) == 0 || len(pairs) != len(cert.Subject.Names) {
		return false
	}
	for _, p := range pairs {
		found := false
		for _, n := range cert.Subject.Names {
			if nodeSubjectKeys[n.Type.String()] == p[0] && strings.TrimSpace(fmt.Sprint(n.Value)) == p[1] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// spiceTLS mirrors spice-proxy.js: without a CA nothing is verified (like rejectUnauthorized:
// false); with a CA the chain must verify, and the host-subject (when given) replaces the
// hostname check.
func spiceTLS(host, ca, hostSubject string) (*tls.Config, error) {
	cfg := &tls.Config{InsecureSkipVerify: true}
	if net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	if ca == "" {
		return cfg, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ca)) {
		return nil, errors.New("invalid CA certificate")
	}
	cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		certs := make([]*x509.Certificate, len(raw))
		for i, b := range raw {
			c, err := x509.ParseCertificate(b)
			if err != nil {
				return err
			}
			certs[i] = c
		}
		if len(certs) == 0 {
			return errors.New("no server certificate")
		}
		inter := x509.NewCertPool()
		for _, c := range certs[1:] {
			inter.AddCert(c)
		}
		opts := x509.VerifyOptions{Roots: pool, Intermediates: inter}
		if hostSubject == "" {
			opts.DNSName = host
		}
		if _, err := certs[0].Verify(opts); err != nil {
			return err
		}
		if hostSubject != "" && !subjectMatches(hostSubject, certs[0]) {
			return fmt.Errorf("TLS certificate subject does not match host-subject %q", hostSubject)
		}
		return nil
	}
	return cfg, nil
}

func dialSpice(o opts) (net.Conn, error) {
	timeout := time.Duration(o.num("readyTimeout", 10000)) * time.Millisecond
	host := o.str("host")
	c, err := dialVia(o.str("proxy"), net.JoinHostPort(host, strconv.Itoa(o.num("port", 5900))), timeout)
	if err != nil || o["tls"] != true {
		return c, err
	}
	cfg, err := spiceTLS(host, o.str("ca"), o.str("hostSubject"))
	if err != nil {
		c.Close()
		return nil, err
	}
	tc := tls.Client(c, cfg)
	tc.SetDeadline(time.Now().Add(timeout))
	if err := tc.Handshake(); err != nil {
		c.Close()
		return nil, fmt.Errorf("TLS connection failed: %w", err)
	}
	tc.SetDeadline(time.Time{})
	return tc, nil
}

func (s *Server) handleSpice(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	spiceMu.Lock()
	t := spiceTargets[pid]
	if t != nil {
		t.channels++
	}
	spiceMu.Unlock()
	if t == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	defer func() {
		spiceMu.Lock()
		if t.channels--; t.channels == 0 && spiceTargets[pid] == t {
			delete(spiceTargets, pid) // the session ends with its last channel
		}
		spiceMu.Unlock()
	}()
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	conn, err := dialSpice(t.o)
	if err != nil {
		ws.c.Close(websocket.StatusInternalError, truncate(err.Error(), 120))
		return
	}
	defer conn.Close()
	bridge(r.Context(), ws.c, conn)
}
