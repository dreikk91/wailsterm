package main

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"time"

	"go.bug.st/serial"
)

const (
	iac  = 255
	dont = 254
	do   = 253
	wont = 252
	will = 251
	sb   = 250
	se   = 240

	optEcho = 1
	optSGA  = 3
)

// telnetReader strips IAC negotiation from the stream and answers it:
// accept server ECHO/SGA, refuse everything else.
type telnetReader struct {
	r *bufio.Reader
	w io.Writer
}

func (t *telnetReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if n > 0 && t.r.Buffered() == 0 {
			break
		}
		b, err := t.r.ReadByte()
		if err != nil {
			return n, err
		}
		if b != iac {
			p[n] = b
			n++
			continue
		}
		cmd, err := t.r.ReadByte()
		if err != nil {
			return n, err
		}
		switch cmd {
		case iac:
			p[n] = iac
			n++
		case do, dont:
			opt, _ := t.r.ReadByte()
			if cmd == do {
				t.w.Write([]byte{iac, wont, opt})
			}
		case will, wont:
			opt, _ := t.r.ReadByte()
			if cmd == will {
				reply := byte(dont)
				if opt == optEcho || opt == optSGA {
					reply = do
				}
				t.w.Write([]byte{iac, reply, opt})
			}
		case sb:
			for prev := byte(0); ; {
				c, err := t.r.ReadByte()
				if err != nil {
					return n, err
				}
				if prev == iac && c == se {
					break
				}
				prev = c
			}
		}
	}
	return n, nil
}

// telnetWriter doubles literal 0xFF bytes as the protocol requires.
type telnetWriter struct{ c net.Conn }

func (w telnetWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		out = append(out, b)
		if b == iac {
			out = append(out, iac)
		}
	}
	_, err := w.c.Write(out)
	return len(p), err
}

func noResize(int, int) error { return nil }

func (t *TermService) OpenTelnet(b Bookmark) (string, error) {
	if b.Port == 0 {
		b.Port = 23
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(b.Host, strconv.Itoa(b.Port)), 10*time.Second)
	if err != nil {
		return "", err
	}
	id := t.add(&session{w: telnetWriter{c}, resize: noResize, close: c.Close})
	go t.pump(id, &telnetReader{bufio.NewReader(c), c})
	return id, nil
}

func (t *TermService) OpenSerial(b Bookmark) (string, error) {
	if b.Port == 0 {
		b.Port = 9600
	}
	p, err := serial.Open(b.Host, &serial.Mode{BaudRate: b.Port})
	if err != nil {
		return "", err
	}
	id := t.add(&session{w: p, resize: noResize, close: p.Close})
	go t.pump(id, p)
	return id, nil
}

func (t *TermService) SerialPorts() ([]string, error) { return serial.GetPortsList() }
