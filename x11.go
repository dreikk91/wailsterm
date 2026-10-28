package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// X11 forwarding, electerm's x11.js + session-ssh.js: a local X server (VcXsrv/Xming,
// XQuartz, X.org) does the drawing; each remote "x11" channel is piped to it.

const x11HelpURL = "https://github.com/electerm/electerm/wiki/Use-X11"

// x11Candidates are the local X endpoints to try for DISPLAY, most likely first.
func x11Candidates(display string) [][2]string {
	d := strings.TrimSpace(display)
	if strings.Contains(d, "/") { // XQuartz: DISPLAY is the socket path
		return [][2]string{{"unix", d}}
	}
	n := 0
	if m := regexp.MustCompile(`:(\d+)(?:\.\d+)?$`).FindStringSubmatch(d); m != nil {
		n, _ = strconv.Atoi(m[1])
	}
	var list [][2]string
	add := func(n int) {
		if runtime.GOOS != "windows" {
			list = append(list, [2]string{"unix", "/tmp/.X11-unix/X" + strconv.Itoa(n)})
		}
		list = append(list, [2]string{"tcp", "127.0.0.1:" + strconv.Itoa(6000+n)})
	}
	add(n)
	if n != 0 {
		add(0)
	}
	return list
}

func dialX11(display string, timeout time.Duration) (net.Conn, error) {
	var err error
	for _, c := range x11Candidates(display) {
		var conn net.Conn
		if conn, err = net.DialTimeout(c[0], c[1], timeout); err == nil {
			return conn, nil
		}
	}
	return nil, err
}

func x11Hint() string {
	switch runtime.GOOS {
	case "darwin":
		return "X11 forwarding is on, but no local X server was found. Install and start XQuartz (https://www.xquartz.org), log out and back in so DISPLAY is set, then reconnect."
	case "windows":
		return `X11 forwarding is on, but no local X server was found. Start VcXsrv (or Xming) with XLaunch on display number 0, keep "Disable access control" checked, make sure it listens on 127.0.0.1:6000, then reconnect.`
	}
	return "X11 forwarding is on, but no local X server was found. Start your X session (or install an X server and xauth), then reconnect."
}

// x11Cookie is the local MIT-MAGIC-COOKIE-1 from xauth, or a random one when there is none
// (VcXsrv with access control disabled ignores it, like ssh2's generated cookie).
func x11Cookie() (cookie string, local bool) {
	if out, err := exec.Command("xauth", "list", ":0").Output(); err == nil {
		if m := regexp.MustCompile(`MIT-MAGIC-COOKIE-1 +([0-9a-fA-F]{1,38})`).FindSubmatch(out); m != nil {
			return string(m[1]), true
		}
	}
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b), false
}

// startX11 asks for X11 forwarding on the shell session and serves the server's x11
// channels. warn is sent at most once (x11-warning) when the local side cannot serve it.
func startX11(client *ssh.Client, sess *ssh.Session, notify func(string)) error {
	var once sync.Once
	warn := func(msg string) { once.Do(func() { notify(msg) }) }
	display := os.Getenv("DISPLAY")
	cookie, local := x11Cookie()
	chans := client.HandleChannelOpen("x11") // before x11-req: the server may open one right away
	if chans == nil {
		return errors.New("x11 channels are already handled on this connection")
	}
	req := []byte{0} // single connection: false
	req = binary.BigEndian.AppendUint32(req, 18)
	req = append(req, "MIT-MAGIC-COOKIE-1"...)
	req = binary.BigEndian.AppendUint32(req, uint32(len(cookie)))
	req = append(req, cookie...)
	req = binary.BigEndian.AppendUint32(req, 0) // screen
	if ok, err := sess.SendRequest("x11-req", true, req); err != nil {
		return err
	} else if !ok {
		warn("The SSH server refused X11 forwarding (X11Forwarding no in sshd_config, or xauth missing on the server).")
	}
	go func() {
		c, err := dialX11(display, 800*time.Millisecond)
		if err != nil {
			warn(x11Hint())
			return
		}
		c.Close()
		if !local && runtime.GOOS != "windows" {
			warn(`No X11 auth cookie could be read (xauth missing or no entry for the current display), the X server may reject remote GUI apps. Check with "xauth list", or open access with "xhost +localhost".`)
		}
	}()
	go func() {
		for nc := range chans {
			go func() {
				x, err := dialX11(display, 5*time.Second)
				if err != nil {
					nc.Reject(ssh.ConnectionFailed, err.Error())
					warn("A remote app asked for X11 forwarding, but no local X server could be reached (display: " + display + "). " + x11Hint())
					return
				}
				ch, reqs, err := nc.Accept()
				if err != nil {
					x.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				go func() {
					io.Copy(ch, x)
					ch.CloseWrite()
				}()
				io.Copy(x, ch)
				x.Close()
				ch.Close()
			}()
		}
	}()
	return nil
}
