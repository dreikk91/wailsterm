//go:build !windows

package main

import (
	"os/exec"
	"sort"
	"strings"
)

// systemFonts lists font families via fontconfig, as font-list does on Linux and macOS.
func systemFonts() []string {
	out, err := exec.Command("fc-list", ":", "family").Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range strings.Split(line, ",") {
			if f = strings.TrimSpace(f); f != "" {
				seen[f] = true
			}
		}
	}
	fonts := make([]string, 0, len(seen))
	for f := range seen {
		fonts = append(fonts, f)
	}
	sort.Strings(fonts)
	return fonts
}
