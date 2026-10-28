package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// defaultConfig is electerm's src/app/common/config-default.js merged with default-setting.js.
var defaultConfig = map[string]any{
	"keepaliveInterval": 10000, "rightClickSelectsWord": false, "mouseEventsRequireAlt": false,
	"rightClickBehavior": "context", "ctrlOrMetaOpenTerminalLink": false,
	"hotkey": "Control+2", "sshReadyTimeout": 50000, "scrollback": 3000, "onStartSessions": []any{},
	"fontSize": 16, "fontFamily": "Maple Mono, mono, courier-new, courier, monospace",
	"execWindows": "System32/WindowsPowerShell/v1.0/powershell.exe", "execMac": "zsh", "execLinux": "bash",
	"execWindowsArgs": []any{}, "execMacArgs": []any{}, "execLinuxArgs": []any{},
	"enableGlobalProxy": false, "disableConnectionHistory": false, "disableTransferHistory": false,
	"terminalBackgroundImagePath": "", "terminalBackgroundCoverMode": false,
	"terminalBackgroundFilterOpacity": 1, "terminalBackgroundFilterBlur": 0,
	"terminalBackgroundFilterBrightness": 1, "terminalBackgroundFilterGrayscale": 0,
	"terminalBackgroundFilterContrast": 1, "rendererType": "dom", "terminalType": "xterm-256color",
	"keepaliveCountMax": 10, "saveTerminalLogToFile": false, "checkUpdateOnStart": false,
	"cursorBlink": false, "cursorStyle": "block", "useSystemTitleBar": true, "opacity": 1,
	"defaultEditor": "", "terminalWordSeparator": "./\\()\"'-:,.;<>~!@#$%^&*|+=[]{}`~ ?",
	"confirmBeforeExit": false, "initDefaultTabOnStart": true, "screenReaderMode": false,
	"autoRefreshWhenSwitchToSftp": false, "addTimeStampToTermLog": false, "backspaceMode": "^?",
	"shiftEnterMode": "\\n", "showHiddenFilesOnSftpStart": true,
	"terminalInfos":           []any{"uptime", "cpu", "mem", "activities", "network", "disks"},
	"remoteMonitorBarEnabled": false,
	"remoteMonitorBarItems":   []any{"hostname", "cpu", "cpuHistory", "memory", "upload", "download", "uptime", "users", "disks"},
	"filePropsEnabled":        []any{"name", "size", "modifyTime"},
	"hideIP":                  false, "dataSyncSelected": "all", "nameAI": "", "baseURLAI": "", "modelAI": "",
	"roleAI": "", "apiPathAI": "/chat/completions", "authHeaderNameAI": "Authorization: Bearer", "proxyAI": "",
	"sessionLogPath": "", "sshSftpSplitView": false, "showCmdSuggestions": false,
	"restoreTerminalSessionOnReload": false, "startDirectoryLocal": "", "allowMultiInstance": false,
	"disableDeveloperTool": false, "dragDropBehavior": "ask", "switchTabOnHover": false,
	"autoDistributeTabsWhenLayoutChange": false, "disableShortcutBar": false, "onlyShowTitleInTab": false,
	"disableConfirmForLargeClipboardContent": false, "doubleClickToOpenBookmark": false,
	"leftSideBarIcons": []any{"bookmarks", "quickConnect", "terminalThemes", "setting"},
	"terminalTimeout":  5000, "zoom": 1, "debug": false, "theme": "default",
	"syncSetting": map[string]any{"autoSync": false, "autoSyncInterval": 0, "autoSyncDirection": "upload"},
	"keyword2FA":  "verification code,otp,one-time,two-factor,2fa,totp,authenticator,duo,yubikey,security code,mfa,passcode",
}

const userConfigID = "userConfig"

func (s *Server) userConfig() map[string]any {
	doc, _ := s.db.Action("data", "findOne", []any{map[string]any{"_id": userConfigID}})
	m, _ := doc.(map[string]any)
	return m
}

// config is lib/get-config.js: defaults overlaid with the saved user config, plus connection info.
func (s *Server) config() map[string]any {
	c := map[string]any{}
	for k, v := range defaultConfig {
		c[k] = v
	}
	for k, v := range s.userConfig() {
		c[k] = v
	}
	for _, k := range []string{"_id", "hashedPassword", "salt"} {
		delete(c, k)
	}
	host, port, _ := net.SplitHostPort(s.addr)
	c["host"], c["port"], c["tokenElecterm"] = host, port, s.token
	return c
}

var transferKeys = []string{"pause", "resume", "destroy"}

var fsFunctions = []string{
	"run", "runWinCmd", "access", "statAsync", "lstatAsync", "cp", "mv", "mkdir", "touch", "chmod",
	"rename", "unlink", "rmrf", "readdirAsync", "readFile", "readFileAsBase64", "writeFile", "openFile",
	"readCustom", "exists", "readdir", "realpath", "statCustom", "openCustom", "closeCustom",
	"writeCustom", "getFolderSize",
}

func argStr(args []any, i int) string {
	if i < len(args) {
		s, _ := args[i].(string)
		return s
	}
	return ""
}

func argStrs(args []any) []string {
	out := make([]string, len(args))
	for i := range args {
		out[i] = argStr(args, i)
	}
	return out
}

// nodeResolve mimics Node's path.resolve: join right-to-left until absolute, then make absolute.
func nodeResolve(parts ...string) string {
	p := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "" {
			continue
		}
		p = filepath.Join(parts[i], p)
		if filepath.IsAbs(parts[i]) {
			break
		}
	}
	abs, _ := filepath.Abs(p)
	return abs
}

func platform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// runSync is lib/ipc-sync.js.
func (s *Server) runSync(name string, args []any) (any, error) {
	app := application.Get()
	switch name {
	case "getConstants":
		home, _ := os.UserHomeDir()
		user := os.Getenv("USER")
		if user == "" {
			user = os.Getenv("USERNAME")
		}
		return map[string]any{
			"sep": string(filepath.Separator), "isTest": false, "isDev": false,
			"isWin": runtime.GOOS == "windows", "isMac": runtime.GOOS == "darwin", "isLinux": runtime.GOOS == "linux",
			"isArm": strings.Contains(runtime.GOARCH, "arm"), "iconPath": "images/electerm-round-128x128.png",
			"trayIconPath": "", "extIconPath": "icons/", "defaultUserName": user,
			"minWindowWidth": 320, "minWindowHeight": 400, "defaultLang": "en_us",
			"tempDir": os.TempDir(), "homeOrTmp": home,
			"packInfo": map[string]any{
				"name": "electerm", "version": s.version, "description": "Terminal/ssh/sftp client (wailsterm: Go + Wails 3 port)",
				"homepage": "https://electerm.org", "releases": "https://github.com/electerm/electerm/releases",
				"bugs":       map[string]any{"url": "https://github.com/electerm/electerm/issues"},
				"repository": map[string]any{"url": "https://github.com/electerm/electerm"},
				"author":     map[string]any{"name": "ZHAO Xudong", "email": "zxdong@gmail.com", "url": "https://github.com/zxdong262"},
				"license":    "MIT", "languageRepo": "https://github.com/electerm/electerm-locales",
				"privacyNoticeLink": "https://github.com/electerm/electerm/wiki/privacy-notice",
				"knownIssuesLink":   "https://github.com/electerm/electerm/wiki/Know-issues",
				"sponsorLink":       "https://electerm.org/sponsor-electerm.html",
			},
			"versions":     fmt.Sprintf(`{"go":%q,"wails":"3","electerm-ui":%q}`, runtime.Version(), s.version),
			"transferKeys": transferKeys,
			"fsFunctions":  fsFunctions,
		}, nil
	case "getLanguage":
		if l, ok := s.userConfig()["language"].(string); ok && l != "" {
			return l, nil
		}
		return "en_us", nil
	case "shouldAuth", "isSecondInstance", "getArgs":
		return nil, nil
	case "nodePtyCheck", "isSystemTitleBar":
		return true, nil
	case "getStorageKey":
		return "electerm", nil
	case "readClipboard":
		text, _ := app.Clipboard.Text()
		return text, nil
	case "writeClipboard":
		app.Clipboard.SetText(argStr(args, 0))
		return nil, nil
	case "resolve":
		return nodeResolve(argStrs(args)...), nil
	case "join":
		return filepath.Join(argStrs(args)...), nil
	case "basename":
		return filepath.Base(argStr(args, 0)), nil
	case "showItemInFolder":
		return nil, showInFolder(argStr(args, 0))
	case "openExternal":
		u := argStr(args, 0)
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return nil, errors.New("only http(s) links can be opened")
		}
		return nil, app.Browser.OpenURL(u)
	case "osInfo":
		host, _ := os.Hostname()
		home, _ := os.UserHomeDir()
		info := map[string]string{"hostname": host, "platform": platform(), "arch": runtime.GOARCH,
			"homedir": home, "tmpdir": os.TempDir(), "cpus": fmt.Sprint(runtime.NumCPU())}
		out := []map[string]string{}
		for k, v := range info {
			b, _ := json.Marshal(v)
			out = append(out, map[string]string{"k": k, "v": string(b)})
		}
		return out, nil
	case "getLoadTime", "getInitTime":
		return map[string]any{"initTime": 0}, nil
	case "setLoadTime":
		return nil, nil
	case "isMaximized":
		return mainWindow != nil && mainWindow.IsMaximised(), nil
	}
	return nil, fmt.Errorf("sync function %q not implemented", name)
}

func showInFolder(p string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", "/select,", p).Start()
	case "darwin":
		return exec.Command("open", "-R", p).Start()
	}
	return exec.Command("xdg-open", filepath.Dir(p)).Start()
}

// runAsync is the asyncGlobals table of lib/ipc.js.
func (s *Server) runAsync(name string, args []any) (any, error) {
	switch name {
	case "init":
		exe, _ := os.Executable()
		return map[string]any{
			"config": s.config(), "installSrc": "", "appPath": dataDir(),
			"exePath": filepath.Dir(exe), "isPortable": false, "widgetLogPath": "",
		}, nil
	case "dbAction":
		if len(args) < 2 {
			return nil, errors.New("dbAction: missing args")
		}
		return s.db.Action(argStr(args, 0), argStr(args, 1), args[2:])
	case "saveUserConfig":
		upd, _ := args[0].(map[string]any)
		for _, k := range []string{"host", "terminalTypes", "tokenElecterm", "server", "port"} {
			delete(upd, k)
		}
		doc := s.userConfig()
		if doc == nil {
			doc = map[string]any{}
		}
		for k, v := range upd {
			doc[k] = v
		}
		doc["_id"] = userConfigID
		return s.db.Action("data", "update", []any{map[string]any{"_id": userConfigID}, doc, map[string]any{"upsert": true}})
	case "checkDbUpgrade", "checkMigrate", "checkProtocolRegistration", "getExitStatus":
		return false, nil
	case "migrate", "doUpgrade", "setExitStatus", "setCloseAction", "confirmExit", "setTitleBarOverlay",
		"setBackgroundColor", "setWindowSize", "changeHotkey", "registerDeepLink", "unregisterDeepLink",
		"setRdpSwapCtrlCmd", "openDevTools", "stopWidget":
		return nil, nil
	case "initCommandLine", "getPendingDeepLink", "getPendingVvFile":
		return nil, nil
	case "loadSshConfig":
		return loadSSHConfig(), nil
	case "loadFontList":
		return systemFonts(), nil
	case "watchFile":
		s.watch.watch(argStr(args, 0), s.Emit)
		return nil, nil
	case "unwatchFile":
		s.watch.unwatch()
		return nil, nil
	case "listWidgets", "listItermThemes":
		return []any{}, nil
	case "listSerialPorts":
		return listSerialPorts(), nil
	case "lookup":
		ips, err := net.LookupHost(argStr(args, 0))
		if err != nil || len(ips) == 0 {
			return nil, err
		}
		return ips[0], nil
	case "getEnv":
		if k := argStr(args, 0); k != "" {
			return os.Getenv(k), nil
		}
		return map[string]string{"HOME": os.Getenv("HOME"), "USERPROFILE": os.Getenv("USERPROFILE")}, nil
	case "encryptAsync":
		return encryptSync(argStr(args, 0), argStr(args, 1))
	case "decryptAsync":
		return decryptSync(argStr(args, 0), argStr(args, 1))
	case "safeEncrypt", "safeDecrypt":
		// the client does not call these; electerm uses them for its own DB, ours is encrypted in secret_*.go
		return argStr(args, 0), nil
	case "getScreenSize":
		if sc := application.Get().Screen.GetPrimary(); sc != nil {
			b := sc.WorkArea
			return map[string]any{"width": b.Width, "height": b.Height, "x": b.X, "y": b.Y}, nil
		}
		return map[string]any{"width": 1920, "height": 1080, "x": 0, "y": 0}, nil
	case "setTitle":
		if mainWindow != nil {
			mainWindow.SetTitle("electerm - " + argStr(args, 0))
		}
		return nil, nil
	case "minimize":
		mainWindow.Minimise()
		return nil, nil
	case "maximize":
		mainWindow.Maximise()
		return nil, nil
	case "unmaximize":
		mainWindow.UnMaximise()
		return nil, nil
	case "closeApp", "exit":
		mainWindow.Close()
		return nil, nil
	case "restart":
		exe, _ := os.Executable()
		if err := exec.Command(exe).Start(); err != nil {
			return nil, err
		}
		application.Get().Quit()
		return nil, nil
	case "openDialog":
		return openDialog(asMap(arg0(args)))
	case "saveDialog":
		return saveDialog(asMap(arg0(args)))
	case "getElectermCommandStatus":
		return map[string]any{"supported": false}, nil
	case "openFileWithEditor":
		return nil, openFileWithEditor(argStr(args, 0), argStr(args, 1))
	}
	return nil, fmt.Errorf("%q is not available in wailsterm yet", name)
}

func arg0(args []any) any {
	if len(args) > 0 {
		return args[0]
	}
	return nil
}

func hasProp(o map[string]any, p string) bool {
	props, _ := o["properties"].([]any)
	for _, v := range props {
		if v == p {
			return true
		}
	}
	return false
}

// openDialog mirrors Electron's dialog.showOpenDialogSync: an array of paths, or nil when cancelled.
func openDialog(o map[string]any) (any, error) {
	d := application.Get().Dialog.OpenFile().
		CanChooseFiles(!hasProp(o, "openDirectory") || hasProp(o, "openFile")).
		CanChooseDirectories(hasProp(o, "openDirectory")).
		CanCreateDirectories(true).
		ShowHiddenFiles(hasProp(o, "showHiddenFiles"))
	if t := str(o, "title"); t != "" {
		d.SetTitle(t)
	}
	if p := str(o, "defaultPath"); p != "" {
		d.SetDirectory(p)
	}
	if hasProp(o, "multiSelections") {
		files, err := d.PromptForMultipleSelection()
		if err != nil || len(files) == 0 {
			return nil, err
		}
		return files, nil
	}
	f, err := d.PromptForSingleSelection()
	if err != nil || f == "" {
		return nil, err
	}
	return []string{f}, nil
}

// saveDialog mirrors Electron's dialog.showSaveDialog: {canceled, filePath}.
func saveDialog(o map[string]any) (any, error) {
	d := application.Get().Dialog.SaveFile().CanCreateDirectories(true)
	if p := str(o, "defaultPath"); p != "" {
		d.SetDirectory(filepath.Dir(p)).SetFilename(filepath.Base(p))
	}
	f, err := d.PromptForSingleSelection()
	if err != nil {
		return nil, err
	}
	return map[string]any{"canceled": f == "", "filePath": f}, nil
}

func openPath(p string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", p).Start()
	case "darwin":
		return exec.Command("open", p).Start()
	}
	return exec.Command("xdg-open", p).Start()
}
