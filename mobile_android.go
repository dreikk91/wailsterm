package main

import (
	"os"
	"path/filepath"
	"strings"
)

// An Android app process gets no usable HOME or TMPDIR. HOME is the phone's
// shared storage, where the file manager's local side starts (it needs the
// "All files access" the activity asks for); temp files and the data dir stay
// in the app's private storage. The package name is the process name.
func init() {
	b, _ := os.ReadFile("/proc/self/cmdline")
	pkg := strings.SplitN(strings.SplitN(string(b), "\x00", 2)[0], ":", 2)[0]
	base := "/data/data/" + pkg
	os.MkdirAll(base+"/cache", 0o700)
	os.Setenv("HOME", "/storage/emulated/0")
	os.Setenv("TMPDIR", base+"/cache")
	if os.Getenv("WAILSTERM_DATA") == "" {
		os.Setenv("WAILSTERM_DATA", filepath.Join(base, "files", "wailsterm"))
	}
}
