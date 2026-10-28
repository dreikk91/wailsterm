package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseSubject(t *testing.T) {
	got := parseSubject(`OU=PVE Cluster Node, O=Proxmox\, Inc,CN=pve1`)
	want := [][2]string{{"OU", "PVE Cluster Node"}, {"O", "Proxmox, Inc"}, {"CN", "pve1"}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("parseSubject = %q", got)
	}
	for _, bad := range []string{"CN", "CN=", "CN=a,", ",CN=a"} {
		if parseSubject(bad) != nil {
			t.Fatalf("parseSubject(%q) accepted", bad)
		}
	}
}

// spiceCA returns a CA PEM and a server certificate it signed with the given subject.
func spiceCA(t *testing.T, subject pkix.Name) (string, tls.Certificate) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: subject, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"pve1.local"}}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestSpiceTLSAndChannels(t *testing.T) {
	ca, cert := spiceCA(t, pkix.Name{OrganizationalUnit: []string{"PVE Cluster Node"}, Organization: []string{"Proxmox Virtual Environment"}, CommonName: "pve1"})
	srv, _ := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	defer srv.Close()
	go func() {
		for {
			c, err := srv.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c)
		}
	}()
	_, p, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(p)
	base := opts{"host": "127.0.0.1", "port": float64(port), "tls": true}
	with := func(extra map[string]any) opts {
		o := opts{}
		for k, v := range base {
			o[k] = v
		}
		for k, v := range extra {
			o[k] = v
		}
		return o
	}
	good := []opts{
		base, // no CA: not verified, like electerm
		with(map[string]any{"ca": ca, "hostSubject": "OU=PVE Cluster Node,O=Proxmox Virtual Environment,CN=pve1"}),
	}
	for i, o := range good {
		c, err := dialSpice(o)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		c.Close()
	}
	bad := []opts{
		with(map[string]any{"ca": ca}),                           // hostname 127.0.0.1 is not in the certificate
		with(map[string]any{"ca": ca, "hostSubject": "CN=pve1"}), // subject must match in full
	}
	otherCA, _ := spiceCA(t, pkix.Name{CommonName: "x"})
	bad = append(bad, with(map[string]any{"ca": otherCA, "hostSubject": "OU=PVE Cluster Node,O=Proxmox Virtual Environment,CN=pve1"}))
	for i, o := range bad {
		if c, err := dialSpice(o); err == nil {
			c.Close()
			t.Fatalf("bad case %d connected", i)
		}
	}

	// two channels of one session through /spice/:pid
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	s := &Server{token: "tok", addr: ln.Addr().String(), terms: map[string]*Term{}}
	go http.Serve(ln, s.routes())
	spiceMu.Lock()
	spiceTargets["sp"] = &spiceTarget{o: base}
	spiceMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var socks []*websocket.Conn
	for i := 0; i < 2; i++ {
		c, _, err := websocket.Dial(ctx, "ws://"+s.addr+"/spice/sp?token=tok", nil)
		if err != nil {
			t.Fatalf("channel %d: %v", i, err)
		}
		msg := []byte("REDQ channel " + strconv.Itoa(i))
		c.Write(ctx, websocket.MessageBinary, msg)
		if _, b, err := c.Read(ctx); err != nil || string(b) != string(msg) {
			t.Fatalf("channel %d echo = %q, %v", i, b, err)
		}
		socks = append(socks, c)
	}
	socks[0].Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond)
	spiceMu.Lock()
	alive := spiceTargets["sp"] != nil
	spiceMu.Unlock()
	if !alive {
		t.Fatal("session ended while a channel is still open")
	}
	socks[1].Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond)
	spiceMu.Lock()
	alive = spiceTargets["sp"] != nil
	spiceMu.Unlock()
	if alive {
		t.Fatal("session kept after its last channel closed")
	}
}
