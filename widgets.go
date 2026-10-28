package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Widgets are electerm's app/widgets: built-in tools the UI lists, configures and runs.
// User widgets are Node.js modules, so wailsterm cannot run them; only built-ins are offered.
// ponytail: the MCP server widget is not ported (its tools run in the renderer through a bridge).

type widgetInstance interface {
	start() (map[string]any, error)
	stop() error
}

type builtinWidget struct {
	info map[string]any
	// run returns an instance for type "instance"; other types return their result directly.
	run func(cfg map[string]any, log *widgetLogger) (widgetInstance, map[string]any, error)
}

func cfgDefault(name, typ string, def any, desc string) map[string]any {
	return map[string]any{"name": name, "type": typ, "default": def, "description": desc}
}

func builtinWidgets() map[string]builtinWidget {
	home, _ := os.UserHomeDir()
	autoRun := cfgDefault("autoRun", "boolean", false, "Automatically run this widget when the app launches")
	return map[string]builtinWidget{
		"local-file-server": {info: map[string]any{
			"name": "Static File Server", "description": "A simple local file server to serve static files from your computer.",
			"version": "1.0.0", "author": "ZHAO Xudong", "type": "instance",
			"configs": []any{
				cfgDefault("host", "string", "127.0.0.1", "The IP address to bind the server to"),
				cfgDefault("port", "number", 3456, "The port number to listen on"),
				cfgDefault("directory", "string", home, "The directory to serve files from (default: user's home directory)"),
				cfgDefault("maxAge", "number", 365*24*60*60*1000, "Browser cache max-age in milliseconds"),
				cfgDefault("cacheControl", "boolean", true, "Enable or disable setting Cache-Control response header"),
				cfgDefault("index", "string", "index.html", "Name of the index file to serve"),
				map[string]any{"name": "dotfiles", "type": "string", "default": "allow", "choices": []any{"allow", "deny", "ignore"}, "description": "Option for serving dotfiles"},
				autoRun,
			},
		}, run: runFileServer},
		"local-ftp-server": {info: map[string]any{
			"name": "Local FTP Server", "description": "A local FTP server to share files over FTP protocol.",
			"version": "1.0.0", "author": "ZHAO Xudong", "type": "instance",
			"configs": []any{
				cfgDefault("host", "string", "0.0.0.0", "The IP address to bind the FTP server to"),
				cfgDefault("port", "number", 2121, "The port number to listen on"),
				cfgDefault("directory", "string", home, "The directory to serve files from (default: user's home directory)"),
				cfgDefault("anonymous", "boolean", false, "Allow anonymous FTP access"),
				cfgDefault("username", "string", "ftpuser", "Username for FTP authentication (used when anonymous is false)"),
				cfgDefault("password", "string", "ftppass", "Password for FTP authentication (used when anonymous is false)"),
				cfgDefault("pasvUrl", "string", "", "Address to advertise in PASV replies (leave empty to auto-detect). Set this when the machine has several network interfaces or is behind NAT."),
				autoRun,
			},
		}, run: runFTPServer},
		"ssh-server": {info: map[string]any{
			"name": "SSH Server", "description": "A local SSH server with shell, exec and SFTP access to a directory.",
			"version": "1.0.0", "author": "ZHAO Xudong", "type": "instance",
			"configs": []any{
				cfgDefault("host", "string", "127.0.0.1", "The IP address to bind the SSH server to"),
				cfgDefault("port", "number", 22225, "The port number to listen on"),
				cfgDefault("username", "string", "test", "Username for password authentication"),
				cfgDefault("password", "string", "test", "Password for password authentication"),
				cfgDefault("directory", "string", home, "Directory exposed over SFTP and used as the shell/exec working directory (default: user's home directory)"),
				autoRun,
			},
		}, run: runSSHServer},
		"rename": {info: map[string]any{
			"name": "File Renamer", "description": "Batch rename files in a folder using customizable templates",
			"version": "1.0.0", "author": "ZHAO Xudong", "type": "once",
			"configs": []any{
				cfgDefault("directory", "string", "", "The directory containing files to rename"),
				cfgDefault("template", "string", "{name}-{n}.{ext}", "Template for new file names. Available tags:\n{n} - Sequential number (e.g., 1, 2, 3)\n{n:padding} - Padded number (e.g., {n:3} => 001, 002)\n{name} - Original filename without extension\n{ext} - File extension\n{date} - File creation date (YYYY-MM-DD)\n{time} - File creation time (HH-mm-ss)\n{random} - Random string"),
				cfgDefault("includeSubfolders", "boolean", false, "Process files in subfolders"),
				cfgDefault("fileTypes", "string", "*", "Comma-separated list of file extensions (e.g., jpg,png,gif) or * for all"),
				cfgDefault("startNumber", "number", 1, "Starting number for sequential naming"),
				cfgDefault("preserveCase", "boolean", true, "Preserve case of original filenames"),
			},
		}, run: func(cfg map[string]any, _ *widgetLogger) (widgetInstance, map[string]any, error) {
			return nil, renameFiles(cfg), nil
		}},
		"batch-op": {info: map[string]any{
			"name": "Batch Operation", "description": "Define and execute multi-step SSH/SFTP workflows with progress tracking.",
			"version": "1.0.0", "type": "frontend", "singleInstance": false, "configs": []any{},
		}, run: func(cfg map[string]any, _ *widgetLogger) (widgetInstance, map[string]any, error) {
			// runs entirely in the UI; electerm's widgetRun just hands back an id
			return nil, map[string]any{"instanceId": newID(), "widgetId": "batch-op", "success": true,
				"msg": "Batch operation workflow started", "serverInfo": nil, "config": cfg}, nil
		}},
	}
}

func widgetConfig(w builtinWidget, user map[string]any) map[string]any {
	cfg := map[string]any{}
	for _, c := range asSlice(w.info["configs"]) {
		cm := asMap(c)
		cfg[str(cm, "name")] = cm["default"]
	}
	maps.Copy(cfg, user)
	return cfg
}

type widgetRuntime struct {
	mu        sync.Mutex
	instances map[string]widgetInstance // instanceId -> running instance
	owner     map[string]string         // instanceId -> widgetId
}

func (r *widgetRuntime) list() []map[string]any {
	out := []map[string]any{}
	for id, w := range builtinWidgets() {
		info := maps.Clone(w.info)
		info["builtin"] = true
		out = append(out, map[string]any{"id": id, "builtin": true, "state": "ok", "info": info})
	}
	// electerm lists widget-*.js files in directory order
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	return out
}

func (r *widgetRuntime) run(widgetID string, user map[string]any) (any, error) {
	w, ok := builtinWidgets()[widgetID]
	if !ok {
		return nil, fmt.Errorf("Widget not found: %s", widgetID)
	}
	cfg := widgetConfig(w, user)
	if w.info["type"] != "instance" {
		_, res, err := w.run(cfg, nil)
		return res, err
	}
	r.mu.Lock()
	if r.instances == nil {
		r.instances, r.owner = map[string]widgetInstance{}, map[string]string{}
	}
	if w.info["singleInstance"] == true {
		for _, owner := range r.owner {
			if owner == widgetID {
				r.mu.Unlock()
				return nil, fmt.Errorf("Widget %s already has a running instance. Only one instance is allowed.", widgetID)
			}
		}
	}
	r.mu.Unlock()
	instanceID := newID()
	logger := &widgetLogger{id: instanceID}
	logger.log("info", fmt.Sprintf("Starting %v (%s)", w.info["name"], widgetID))
	inst, _, err := w.run(cfg, logger)
	if err != nil {
		return nil, err
	}
	res, err := inst.start()
	if err != nil {
		logger.log("error", "Failed to start: "+err.Error())
		inst.stop()
		return nil, err
	}
	logger.log("info", str(res, "msg"))
	r.mu.Lock()
	r.instances[instanceID], r.owner[instanceID] = inst, widgetID
	r.mu.Unlock()
	out := map[string]any{"instanceId": instanceID, "widgetId": widgetID, "singleInstance": w.info["singleInstance"] == true}
	maps.Copy(out, res)
	return out, nil
}

func (r *widgetRuntime) stop(instanceID string) (any, error) {
	r.mu.Lock()
	inst := r.instances[instanceID]
	r.mu.Unlock()
	if inst == nil {
		return nil, nil
	}
	logger := &widgetLogger{id: instanceID}
	logger.log("info", "Stopping...")
	if err := inst.stop(); err != nil {
		logger.log("error", "Failed to stop: "+err.Error())
		return nil, err
	}
	logger.log("info", "Stopped")
	r.mu.Lock()
	delete(r.instances, instanceID)
	delete(r.owner, instanceID)
	r.mu.Unlock()
	return map[string]any{"instanceId": instanceID, "status": "stopped"}, nil
}

func (r *widgetRuntime) stopAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.instances))
	for id := range r.instances {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.stop(id)
	}
}

func (r *widgetRuntime) call(widgetID, fn string) (any, error) {
	w, ok := builtinWidgets()[widgetID]
	if !ok {
		return nil, fmt.Errorf("Widget not found: %s", widgetID)
	}
	if fn == "getDefaultConfig" {
		return widgetConfig(w, nil), nil
	}
	return nil, fmt.Errorf("Widget %s does not export the function %s", widgetID, fn)
}

var errUserWidgets = errors.New("user widgets are Node.js modules and are not supported in wailsterm; built-in widgets are available")

// ---- instance logs (widgets/instance-log.js): <logdir>/<instanceId>.log, one "|"-separated line per entry ----

func widgetLogDir() string {
	d := filepath.Join(dataDir(), "widget_logs")
	os.MkdirAll(d, 0o700)
	return d
}

func pruneWidgetLogs() {
	des, _ := os.ReadDir(widgetLogDir())
	re := regexp.MustCompile(`\.log(\.\d+)?$`)
	for _, d := range des {
		if fi, err := d.Info(); err == nil && re.MatchString(d.Name()) && time.Since(fi.ModTime()) > 7*24*time.Hour {
			os.Remove(filepath.Join(widgetLogDir(), d.Name()))
		}
	}
}

type widgetLogger struct {
	mu sync.Mutex
	id string
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", `\n`), "\n", `\n`)
}

func (l *widgetLogger) write(fields ...string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	file := filepath.Join(widgetLogDir(), l.id+".log")
	if fi, err := os.Stat(file); err == nil && fi.Size() > 1<<20 {
		os.Remove(file + ".1")
		os.Rename(file, file+".1")
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(strings.Join(append([]string{time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, fields...), "|") + "\n")
}

func (l *widgetLogger) log(level, msg string) {
	if len(msg) > 4000 {
		msg = msg[:4000] + "..."
	}
	l.write("log", level, oneLine(msg))
}

// event records a connection-style entry; ok is "ok", "fail" or "info".
func (l *widgetLogger) event(typ, ok, from, msg string) {
	token := func(s string) string { return strings.ReplaceAll(oneLine(s), "|", "/") }
	if from == "" {
		from = "-"
	}
	l.write("conn", ok, token(typ), token(from), oneLine(msg))
}

func okState(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

func numCfg(cfg map[string]any, k string) int {
	switch v := cfg[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}
