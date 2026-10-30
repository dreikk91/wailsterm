package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/net/proxy"
)

// interactor shows electerm's session-interactive dialog and returns the answers
// (empty means the user cancelled). options follow session-ssh.js onKeyboardEvent.
type interactor func(options map[string]any) ([]string, error)

// ---- known hosts (electerm ssh-known-hosts.js) ----

func knownHostsPath() string {
	if p := os.Getenv("WAILSTERM_KNOWN_HOSTS"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "known_hosts")
}

func hostConfirm(title string, instructions []string, submit string) map[string]any {
	return map[string]any{
		"mode": "confirm", "name": title, "instructions": instructions, "prompts": []any{},
		"submitText": submit, "cancelText": "Reject", "confirmResult": "trust",
	}
}

// lineMatchesHost reports whether a known_hosts host field names target (plain or |1| hashed).
func lineMatchesHost(field, target string) bool {
	for _, h := range strings.Split(field, ",") {
		if h == target {
			return true
		}
		if parts := strings.Split(h, "|"); len(parts) == 4 && parts[1] == "1" {
			salt, err1 := base64.StdEncoding.DecodeString(parts[2])
			want, err2 := base64.StdEncoding.DecodeString(parts[3])
			if err1 == nil && err2 == nil {
				m := hmac.New(sha1.New, salt)
				m.Write([]byte(target))
				if hmac.Equal(m.Sum(nil), want) {
					return true
				}
			}
		}
	}
	return false
}

func replaceKnownHost(file, target string, key ssh.PublicKey) error {
	raw, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && !strings.HasPrefix(f[0], "#") && lineMatchesHost(f[0], target) && f[1] == key.Type() {
			continue
		}
		if line != "" {
			kept = append(kept, line)
		}
	}
	kept = append(kept, knownhosts.Line([]string{target}, key))
	return os.WriteFile(file, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// hostKeyCallback checks ~/.ssh/known_hosts; unknown or changed keys are put to the user.
func hostKeyCallback(ask interactor) (ssh.HostKeyCallback, error) {
	file := knownHostsPath()
	os.MkdirAll(filepath.Dir(file), 0o700)
	if f, err := os.OpenFile(file, os.O_CREATE|os.O_RDONLY, 0o600); err == nil {
		f.Close()
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		check, err := knownhosts.New(file)
		if err != nil {
			return err
		}
		err = check(host, remote, key)
		var ke *knownhosts.KeyError
		if err == nil || !errors.As(err, &ke) {
			return err
		}
		target := knownhosts.Normalize(host)
		fp := ssh.FingerprintSHA256(key)
		var prompt map[string]any
		if len(ke.Want) == 0 {
			prompt = hostConfirm("Trust SSH host key for "+target+"?", []string{
				"The authenticity of host '" + target + "' can't be established.",
				"Key type: " + key.Type(), "Fingerprint: " + fp, "Known hosts file: " + file,
				"Trust this host key and add it to known_hosts?",
			}, "Trust and Save")
		} else {
			prompt = hostConfirm("SSH host key changed for "+target, []string{
				"WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!",
				"The host key for '" + target + "' has changed.",
				"New key type: " + key.Type(), "New fingerprint: " + fp, "Known hosts file: " + file,
				"This could indicate a man-in-the-middle attack, or the remote host was reinstalled (e.g. router reboot).",
				"Update the known_hosts entry with the new key?",
			}, "Update Key")
		}
		ans, err := ask(prompt)
		if err != nil || len(ans) == 0 || ans[0] != "trust" {
			if len(ke.Want) > 0 {
				return fmt.Errorf("SSH host key verification failed for %s. Presented %s fingerprint %s does not match %s", target, key.Type(), fp, file)
			}
			return errors.New("SSH host key verification was canceled by the user")
		}
		return replaceKnownHost(file, target, key)
	}, nil
}

// ---- authentication ----

func defaultKeys() []ssh.Signer {
	home, _ := os.UserHomeDir()
	var out []ssh.Signer
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		if pem, err := os.ReadFile(filepath.Join(home, ".ssh", n)); err == nil {
			if s, err := ssh.ParsePrivateKey(pem); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// agentClient opens the ssh agent the bookmark asks for (useSshAgent defaults to on in electerm).
func agentClient(o opts) agent.ExtendedAgent {
	if v, ok := o["useSshAgent"].(bool); ok && !v {
		return nil
	}
	c, err := dialAgent(o.str("sshAgent"))
	if err != nil {
		return nil
	}
	return agent.NewClient(c)
}

func authMethods(o opts, ag agent.ExtendedAgent, ask interactor) ([]ssh.AuthMethod, error) {
	password := o.str("password")
	var auth []ssh.AuthMethod
	key := o.str("privateKey")
	if key == "" && o.str("privateKeyPath") != "" {
		b, err := os.ReadFile(o.str("privateKeyPath"))
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		key = string(b)
	}
	if key != "" {
		var signer ssh.Signer
		var err error
		if strings.HasPrefix(key, ppkPrefix) {
			signer, err = parsePPK([]byte(key), []byte(o.str("passphrase")))
		} else if pass := o.str("passphrase"); pass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(key))
		}
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if ag != nil {
		auth = append(auth, ssh.PublicKeysCallback(ag.Signers))
	}
	if key == "" && password == "" {
		if keys := defaultKeys(); len(keys) > 0 {
			auth = append(auth, ssh.PublicKeys(keys...))
		}
	}
	prompt := func(name, instruction string, qs []string, echos []bool) ([]string, error) {
		ps := make([]any, len(qs))
		for i, q := range qs {
			ps[i] = map[string]any{"prompt": q, "echo": echos[i]}
		}
		ins := []any{}
		if instruction != "" {
			ins = append(ins, instruction)
		}
		ans, err := ask(map[string]any{"name": name, "instructions": ins, "prompts": ps})
		if err == nil && len(ans) == 0 {
			err = errors.New("user cancel")
		}
		return ans, err
	}
	if password != "" {
		auth = append(auth, ssh.Password(password))
	} else {
		auth = append(auth, ssh.PasswordCallback(func() (string, error) {
			a, err := prompt("", "", []string{"password:"}, []bool{false})
			if err != nil {
				return "", err
			}
			return a[0], nil
		}))
	}
	auth = append(auth, ssh.KeyboardInteractive(func(name, instruction string, qs []string, echos []bool) ([]string, error) {
		if len(qs) == 0 {
			return nil, nil
		}
		if len(qs) == 1 && !echos[0] && password != "" && strings.Contains(strings.ToLower(qs[0]), "password") {
			return []string{password}, nil
		}
		if iv := o.str("interactiveValues"); iv != "" {
			return strings.Split(iv, "\n"), nil
		}
		return prompt(name, instruction, qs, echos)
	}))
	return auth, nil
}

// ---- proxy (electerm build-proxy.js strings: socks5://, socks4://, http://, https://) ----

func dialVia(proxyURL, addr string, timeout time.Duration) (net.Conn, error) {
	if proxyURL == "" {
		return net.DialTimeout("tcp", addr, timeout)
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("proxy: %w", err)
	}
	switch u.Scheme {
	case "socks5", "socks5h":
		var a *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			a = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, a, &net.Dialer{Timeout: timeout})
		if err != nil {
			return nil, err
		}
		return d.Dial("tcp", addr)
	case "socks4", "socks4a":
		return socks4Dial(u, addr, timeout)
	case "http", "https":
		c, err := net.DialTimeout("tcp", u.Host, timeout)
		if err != nil {
			return nil, err
		}
		req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
		if u.User != nil {
			pw, _ := u.User.Password()
			req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pw)) + "\r\n"
		}
		c.SetDeadline(time.Now().Add(timeout))
		if _, err := io.WriteString(c, req+"\r\n"); err != nil {
			c.Close()
			return nil, err
		}
		br := bufio.NewReader(c)
		status, err := br.ReadString('\n')
		if err != nil || !strings.Contains(status, " 200") {
			c.Close()
			return nil, fmt.Errorf("proxy CONNECT failed: %s", strings.TrimSpace(status))
		}
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				c.Close()
				return nil, err
			}
			if l == "\r\n" || l == "\n" {
				break
			}
		}
		c.SetDeadline(time.Time{})
		return c, nil
	}
	return nil, fmt.Errorf("proxy type %q not supported", u.Scheme)
}

// socks4Dial sends a SOCKS4 CONNECT, or SOCKS4a (server-side DNS) when addr is not an IPv4 literal.
func socks4Dial(u *url.URL, addr string, timeout time.Duration) (net.Conn, error) {
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(ps)
	if err != nil {
		return nil, err
	}
	req := []byte{4, 1, byte(port >> 8), byte(port)}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		req = append(req, 0, 0, 0, 1)
	} else {
		req = append(req, ip...)
	}
	req = append(append(req, u.User.Username()...), 0)
	if ip == nil {
		req = append(append(req, host...), 0)
	}
	c, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(timeout))
	resp := make([]byte, 8)
	if _, err = c.Write(req); err == nil {
		_, err = io.ReadFull(c, resp)
	}
	if err == nil && resp[1] != 0x5a {
		err = fmt.Errorf("socks4 proxy rejected the connection (code %d)", resp[1])
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// ---- connection: proxy -> jump hosts -> target ----

func hopOpts(m any) opts {
	o, _ := m.(map[string]any)
	return opts(o)
}

func addrOf(o opts) string {
	return net.JoinHostPort(o.str("host"), strconv.Itoa(o.num("port", 22)))
}

// dialSSH connects through electerm's connectionHoppings (when hasHopping is set), first hop
// first, and returns the client for the bookmark's own host plus the intermediate clients.
func dialSSH(o opts, ask interactor) (*ssh.Client, []*ssh.Client, error) {
	chain := []opts{}
	if hops, _ := o["connectionHoppings"].([]any); o["hasHopping"] == true && len(hops) > 0 {
		for _, h := range hops {
			chain = append(chain, hopOpts(h))
		}
	}
	chain = append(chain, o)
	timeout := time.Duration(o.num("readyTimeout", 50000)) * time.Millisecond
	hk, err := hostKeyCallback(ask)
	if err != nil {
		return nil, nil, err
	}
	ag := agentClient(o)
	conn, err := dialVia(o.str("proxy"), addrOf(chain[0]), timeout)
	if err != nil {
		return nil, nil, err
	}
	var clients []*ssh.Client
	fail := func(err error) (*ssh.Client, []*ssh.Client, error) {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
		return nil, nil, err
	}
	for i, target := range chain {
		auth, err := authMethods(target, ag, ask)
		if err != nil {
			conn.Close()
			return fail(err)
		}
		cc, chans, reqs, err := ssh.NewClientConn(conn, addrOf(target), &ssh.ClientConfig{
			User: target.str("username"), Auth: auth, HostKeyCallback: hk, Timeout: timeout,
		})
		if err != nil {
			conn.Close()
			if i < len(chain)-1 {
				err = fmt.Errorf("jump host %s: %w", addrOf(target), err)
			}
			return fail(err)
		}
		client := ssh.NewClient(cc, chans, reqs)
		clients = append(clients, client)
		if i < len(chain)-1 {
			if conn, err = client.Dial("tcp", addrOf(chain[i+1])); err != nil {
				return fail(fmt.Errorf("jump to %s: %w", addrOf(chain[i+1]), err))
			}
		}
	}
	last := clients[len(clients)-1]
	if ag != nil && o["agentForward"] == true {
		agent.ForwardToAgent(last, ag)
	}
	return last, clients[:len(clients)-1], nil
}

func startSSH(o opts, ask interactor, notify func(map[string]any)) (*Term, error) {
	client, jumps, err := dialSSH(o, ask)
	if err != nil {
		return nil, err
	}
	var closers []io.Closer
	closeAll := func() error {
		for _, c := range closers {
			c.Close()
		}
		err := client.Close()
		for i := len(jumps) - 1; i >= 0; i-- {
			jumps[i].Close()
		}
		return err
	}
	results := []any{}
	for _, t := range asSlice(o["sshTunnels"]) {
		tun := hopOpts(t)
		if tun.str("sshTunnel") == "" || tun.num("sshTunnelLocalPort", 0) == 0 {
			continue
		}
		l, err := startTunnel(client, tun)
		r := map[string]any{"sshTunnel": t}
		if err != nil {
			r["error"] = err.Error()
		} else {
			closers = append(closers, l)
		}
		results = append(results, r)
	}
	if len(results) > 0 && notify != nil {
		notify(map[string]any{"action": "ssh-tunnel-result", "tabId": o.str("srcTabId"),
			"update": map[string]any{"sshTunnelResults": results}})
	}
	if o["enableSsh"] == false {
		return &Term{out: strings.NewReader(""), in: io.Discard, client: client, resize: noResize, close: closeAll}, nil
	}
	sess, err := client.NewSession()
	if err != nil {
		closeAll()
		return nil, err
	}
	in, _ := sess.StdinPipe()
	out, _ := sess.StdoutPipe()
	sess.Stderr = sess.Stdout
	if o["agentForward"] == true {
		agent.RequestAgentForwarding(sess)
	}
	if o["x11"] == true {
		err := startX11(client, sess, func(msg string) {
			if notify != nil {
				notify(map[string]any{"action": "x11-warning", "message": msg, "url": x11HelpURL, "tabId": o.str("srcTabId")})
			}
		})
		if err != nil {
			closeAll()
			return nil, err
		}
	}
	if lang := o.str("envLang"); lang != "" {
		sess.Setenv("LANG", lang)
	}
	term := o.str("term")
	if term == "" {
		term = "xterm-256color"
	}
	if err := sess.RequestPty(term, o.num("rows", 24), o.num("cols", 80), ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		closeAll()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		closeAll()
		return nil, err
	}
	if iv := o.num("keepaliveInterval", 0); iv > 0 {
		go func() {
			for range time.Tick(time.Duration(iv) * time.Millisecond) {
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					return
				}
			}
		}()
	}
	return &Term{
		out:    out,
		in:     in,
		client: client,
		resize: func(c, r int) error { return sess.WindowChange(r, c) },
		close:  closeAll,
	}, nil
}

func asSlice(v any) []any {
	a, _ := v.([]any)
	return a
}

// ---- tunnels (electerm ssh-tunnel.js) ----

func pipeBoth(a, b io.ReadWriteCloser) {
	go func() { io.Copy(a, b); a.Close() }()
	io.Copy(b, a)
	b.Close()
}

func startTunnel(client *ssh.Client, t opts) (io.Closer, error) {
	local := net.JoinHostPort(orDefault(t.str("sshTunnelLocalHost"), "127.0.0.1"), strconv.Itoa(t.num("sshTunnelLocalPort", 0)))
	remote := net.JoinHostPort(orDefault(t.str("sshTunnelRemoteHost"), "127.0.0.1"), strconv.Itoa(t.num("sshTunnelRemotePort", 0)))
	var l net.Listener
	var err error
	var dial func() (net.Conn, error)
	switch t.str("sshTunnel") {
	case "forwardRemoteToLocal":
		l, err = client.Listen("tcp", remote)
		dial = func() (net.Conn, error) { return net.Dial("tcp", local) }
	case "forwardLocalToRemote":
		l, err = net.Listen("tcp", local)
		dial = func() (net.Conn, error) { return client.Dial("tcp", remote) }
	case "dynamicForward":
		l, err = net.Listen("tcp", local)
	default:
		return nil, fmt.Errorf("unknown tunnel type %q", t.str("sshTunnel"))
	}
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				if dial == nil {
					socks5Serve(c, client)
					return
				}
				d, err := dial()
				if err != nil {
					log.Printf("tunnel %s: %v", t.str("sshTunnel"), err)
					c.Close()
					return
				}
				pipeBoth(c, d)
			}()
		}
	}()
	return l, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// socks5Serve handles one no-auth SOCKS5 CONNECT and pipes it through the SSH client.
func socks5Serve(c net.Conn, client *ssh.Client) {
	defer c.Close()
	br := bufio.NewReader(c)
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[0] != 5 {
		return
	}
	if _, err := io.ReadFull(br, make([]byte, hdr[1])); err != nil {
		return
	}
	c.Write([]byte{5, 0})
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[1] != 1 {
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(br, b)
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		io.ReadFull(br, b)
		host = net.IP(b).String()
	case 3:
		n, _ := br.ReadByte()
		b := make([]byte, n)
		io.ReadFull(br, b)
		host = string(b)
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	d, err := client.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))))
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	pipeBoth(struct {
		io.Reader
		io.WriteCloser
	}{br, c}, d)
}

// ---- ~/.ssh/config import (what electerm gets from ssh-config-loader) ----

func loadSSHConfig() []map[string]any {
	home, _ := os.UserHomeDir()
	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return []map[string]any{}
	}
	defer f.Close()
	out := []map[string]any{}
	var cur map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, _ := strings.Cut(strings.Replace(line, "=", " ", 1), " ")
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.ToLower(k) {
		case "host":
			cur = nil
			if strings.ContainsAny(v, "*?! ") {
				continue // patterns are defaults, not connectable hosts
			}
			cur = map[string]any{"title": v, "host": v, "port": 22}
			out = append(out, cur)
		case "match":
			cur = nil
		}
		if cur == nil {
			continue
		}
		switch strings.ToLower(k) {
		case "hostname":
			cur["host"] = v
		case "port":
			if p, err := strconv.Atoi(v); err == nil {
				cur["port"] = p
			}
		case "user":
			cur["username"] = v
		case "identityfile":
			if strings.HasPrefix(v, "~") {
				v = filepath.Join(home, v[1:])
			}
			cur["privateKeyPath"] = v
			cur["authType"] = "privateKey"
		}
	}
	return out
}
