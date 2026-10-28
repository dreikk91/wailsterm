package main

import (
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

//go:embed seed.json
var seedJSON []byte

// localShell is one default local-terminal bookmark (electerm upgrade/default-local-bookmarks.js).
type localShell struct{ id, title, exec, description string }

var shellColors = map[string]string{
	"bash": "#4EAA25", "zsh": "#2E86DE", "fish": "#8E44AD", "cmd.exe": "#607D8B", "PowerShell": "#2E5C8A",
	"PowerShell 7": "#3B78C3", "WSL": "#E95420", "Git Bash": "#F05033",
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// winDirExec returns rel (stored windir-relative, as startLocal resolves it) when the file exists.
func winDirExec(rel string) string {
	if exists(filepath.Join(os.Getenv("windir"), rel)) {
		return rel
	}
	return ""
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p != "" && exists(p) {
			return p
		}
	}
	return ""
}

func lookPath(name string) string {
	p, _ := exec.LookPath(name)
	return p
}

func detectShells() []localShell {
	if runtime.GOOS == "windows" {
		pf, pf86, lad := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")
		all := []localShell{
			{"default-local-cmd", "cmd.exe", winDirExec("System32/cmd.exe"), "Local terminal running the Windows command prompt"},
			{"default-local-powershell", "PowerShell", winDirExec("System32/WindowsPowerShell/v1.0/powershell.exe"), "Local terminal running Windows PowerShell"},
			{"default-local-pwsh", "PowerShell 7", firstExisting(lookPath("pwsh"), filepath.Join(pf, "PowerShell", "7", "pwsh.exe")), "Local terminal running PowerShell 7 (pwsh.exe)"},
			{"default-local-wsl", "WSL", winDirExec("System32/wsl.exe"), "Local terminal running the default WSL distribution (wsl.exe, add -d <distro> to pick one)"},
			{"default-local-git-bash", "Git Bash", firstExisting(filepath.Join(pf, "Git", "bin", "bash.exe"), filepath.Join(pf86, "Git", "bin", "bash.exe"), filepath.Join(lad, "Programs", "Git", "bin", "bash.exe")), "Local terminal running Git Bash"},
		}
		var out []localShell
		for _, s := range all {
			if s.exec != "" {
				out = append(out, s)
			}
		}
		return out
	}
	names := []string{"bash", "zsh", "fish"}
	if runtime.GOOS == "darwin" {
		names = []string{"zsh", "bash", "fish"}
	}
	var out []localShell
	for _, n := range names {
		if lookPath(n) != "" {
			out = append(out, localShell{"default-local-" + n, n, n, "Local terminal running " + n})
		}
	}
	return out
}

func execProp() string {
	switch runtime.GOOS {
	case "windows":
		return "execWindows"
	case "darwin":
		return "execMac"
	}
	return "execLinux"
}

// seedDB is electerm's upgrade/init-db.js: on a fresh database insert the default themes,
// the "default" bookmark group, local shell bookmarks and quick commands, then stamp the version.
// It also adopts bookmarks that are in no group, which repairs databases from builds without seeding.
func seedDB(db *DB, version string) error {
	if v, _ := db.Action("data", "findOne", []any{map[string]any{"_id": "version"}}); v != nil {
		return nil
	}
	var seed struct {
		TerminalThemes []any              `json:"terminalThemes"`
		QuickCommands  map[string][][]any `json:"quickCommands"`
	}
	if err := json.Unmarshal(seedJSON, &seed); err != nil {
		return err
	}
	if _, err := db.Action("terminalThemes", "insert", []any{seed.TerminalThemes}); err != nil {
		return err
	}

	prop := execProp()
	var bookmarks []any
	var ids []any
	for _, s := range detectShells() {
		color := shellColors[s.title]
		if color == "" {
			color = "#0088cc"
		}
		bookmarks = append(bookmarks, map[string]any{
			"_id": s.id, "title": s.title, "type": "local", "description": s.description, "color": color,
			prop: s.exec, prop + "Args": []any{},
		})
		ids = append(ids, s.id)
	}
	existing, _ := db.Action("bookmarks", "find", []any{map[string]any{}})
	for _, b := range existing.([]map[string]any) {
		ids = append(ids, b["_id"])
	}
	if len(bookmarks) > 0 {
		if _, err := db.Action("bookmarks", "insert", []any{bookmarks}); err != nil {
			return err
		}
	}
	if _, err := db.Action("bookmarkGroups", "insert", []any{[]any{map[string]any{
		"_id": "default", "title": "default", "bookmarkIds": dedupe(ids), "bookmarkGroupIds": []any{}, "color": "#0088cc",
	}}}); err != nil {
		return err
	}

	osKey := runtime.GOOS
	if osKey != "windows" && osKey != "darwin" {
		osKey = "linux"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var qcs, history []any
	for _, c := range seed.QuickCommands[osKey] {
		id, name, command := c[0].(string), c[1].(string), c[2].(string)
		qcs = append(qcs, map[string]any{
			"_id": id, "name": name, "inputOnly": false, "labels": c[3], "shortcut": "",
			"commands": []any{map[string]any{"id": id + "-cmd", "command": command, "delay": 100}},
		})
		// the history popover shows newest first, so prepend to keep 'ls' on top
		history = append([]any{map[string]any{"_id": id + "-cmd", "cmd": command, "count": 1, "lastUseTime": now}}, history...)
	}
	if _, err := db.Action("quickCommands", "insert", []any{qcs}); err != nil {
		return err
	}
	if _, err := db.Action("terminalCommandHistory", "insert", []any{history}); err != nil {
		return err
	}
	_, err := db.Action("data", "update", []any{
		map[string]any{"_id": "version"}, map[string]any{"_id": "version", "value": version}, map[string]any{"upsert": true},
	})
	return err
}

func dedupe(a []any) []any {
	seen := map[any]bool{}
	out := []any{}
	for _, v := range a {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
