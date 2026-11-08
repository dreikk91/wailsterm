package main

import "testing"

func TestHostOK(t *testing.T) {
	loop := &Server{addr: "127.0.0.1:8080"}
	all := &Server{addr: "0.0.0.0:8080"}
	cases := []struct {
		s    *Server
		host string
		ok   bool
	}{
		{loop, "127.0.0.1:8080", true},
		{loop, "192.168.1.5:8080", false},
		{all, "192.168.1.5:8080", true},
		{all, "100.64.0.1:8080", true},
		{all, "192.168.1.5:9090", false},
		{all, "evil.example:8080", false}, // DNS rebinding
		{all, "192.168.1.5", false},
	}
	for _, c := range cases {
		if got := c.s.hostOK(c.host); got != c.ok {
			t.Errorf("%s hostOK(%q) = %v, want %v", c.s.addr, c.host, got, c.ok)
		}
	}
}
