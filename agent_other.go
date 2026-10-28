//go:build !windows

package main

import (
	"errors"
	"net"
	"os"
)

// dialAgent connects to the agent socket (SSH_AUTH_SOCK unless the bookmark names one).
func dialAgent(path string) (net.Conn, error) {
	if path == "" {
		path = os.Getenv("SSH_AUTH_SOCK")
	}
	if path == "" {
		return nil, errors.New("no ssh agent")
	}
	return net.Dial("unix", path)
}
