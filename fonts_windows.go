package main

import (
	"os/exec"
	"strings"
	"syscall"
)

// systemFonts lists installed font families the way font-list does on Windows (.NET InstalledFontCollection).
func systemFonts() []string {
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[Text.Encoding]::UTF8; Add-Type -AssemblyName System.Drawing; (New-Object System.Drawing.Text.InstalledFontCollection).Families | ForEach-Object Name")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return []string{}
	}
	fonts := []string{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			fonts = append(fonts, l)
		}
	}
	return fonts
}
