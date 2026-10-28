package main

import (
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

// dialAgent connects to the Windows OpenSSH agent pipe, or to a custom pipe/socket path.
func dialAgent(path string) (net.Conn, error) {
	if path == "" {
		path = `\\.\pipe\openssh-ssh-agent`
	}
	if strings.HasPrefix(path, `\\.\pipe\`) {
		timeout := 2 * time.Second
		return winio.DialPipe(path, &timeout)
	}
	return net.Dial("unix", path)
}
