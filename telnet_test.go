package main

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

func TestTelnetReader(t *testing.T) {
	in := []byte{'h', iac, will, optEcho, 'i', iac, do, 31, iac, iac, iac, sb, 24, 1, iac, se, '!'}
	var replies bytes.Buffer
	got, err := io.ReadAll(&telnetReader{bufio.NewReader(bytes.NewReader(in)), &replies})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{'h', 'i', iac, '!'}; !bytes.Equal(got, want) {
		t.Errorf("data = %v, want %v", got, want)
	}
	if want := []byte{iac, do, optEcho, iac, wont, 31}; !bytes.Equal(replies.Bytes(), want) {
		t.Errorf("replies = %v, want %v", replies.Bytes(), want)
	}
}
