package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aymanbagabas/go-pty"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/pkg/sftp"
	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
)

// ---- Static File Server (widget-local-file-server.js) ----

type httpWidget struct {
	srv  *http.Server
	ln   net.Listener
	addr string
	info map[string]any
}

func runFileServer(cfg map[string]any, log *widgetLogger) (widgetInstance, map[string]any, error) {
	dir := str(cfg, "directory")
	files := http.FileServer(http.Dir(dir))
	dotfiles, index := str(cfg, "dotfiles"), str(cfg, "index")
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		func() {
			for _, seg := range strings.Split(r.URL.Path, "/") {
				if strings.HasPrefix(seg, ".") && seg != "." && seg != ".." && dotfiles != "allow" {
					if dotfiles == "deny" {
						http.Error(rec, "Forbidden", http.StatusForbidden)
					} else {
						http.NotFound(rec, r)
					}
					return
				}
			}
			if cfg["cacheControl"] != false {
				rec.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(numCfg(cfg, "maxAge")/1000))
			}
			if index != "" && index != "index.html" && strings.HasSuffix(r.URL.Path, "/") {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path.Clean(r.URL.Path)), index)); err == nil {
					r.URL.Path += index
				}
			}
			files.ServeHTTP(rec, r)
		}()
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		log.event(r.Method, okState(rec.status < 400), host, fmt.Sprintf("%s -> %d (%dms)", r.URL.RequestURI(), rec.status, time.Since(start).Milliseconds()))
		if rec.status >= 400 {
			log.log("warn", fmt.Sprintf("%s %s -> %d", r.Method, r.URL.RequestURI(), rec.status))
		}
	})
	addr := net.JoinHostPort(str(cfg, "host"), strconv.Itoa(numCfg(cfg, "port")))
	return &httpWidget{srv: &http.Server{Handler: h}, addr: addr, info: map[string]any{"url": "http://" + addr, "path": dir}}, nil, nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (h *httpWidget) start() (map[string]any, error) {
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		return nil, err
	}
	h.ln = ln
	go h.srv.Serve(ln)
	return map[string]any{"serverInfo": h.info, "msg": "Static File Server is running at " + h.info["url"].(string), "success": true}, nil
}

func (h *httpWidget) stop() error {
	if h.ln == nil {
		return nil
	}
	return h.srv.Close()
}

// ---- Local FTP Server (widget-local-ftp-server.js) ----

type ftpWidget struct {
	cfg map[string]any
	log *widgetLogger
	srv *ftpserver.FtpServer
}

func runFTPServer(cfg map[string]any, log *widgetLogger) (widgetInstance, map[string]any, error) {
	return &ftpWidget{cfg: cfg, log: log}, nil, nil
}

func (f *ftpWidget) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{
		ListenAddr:               net.JoinHostPort(str(f.cfg, "host"), strconv.Itoa(numCfg(f.cfg, "port"))),
		PublicHost:               str(f.cfg, "pasvUrl"),
		PassiveTransferPortRange: &ftpserver.PortRange{Start: 50000, End: 51000},
	}, nil
}

func (f *ftpWidget) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	f.log.event("connect", "ok", cc.RemoteAddr().String(), "control connection opened")
	return "electerm FTP server", nil
}

func (f *ftpWidget) ClientDisconnected(cc ftpserver.ClientContext) {
	f.log.event("disconnect", "info", cc.RemoteAddr().String(), "control connection closed")
}

func (f *ftpWidget) AuthUser(cc ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	from := cc.RemoteAddr().String()
	if f.cfg["anonymous"] != true && (user != str(f.cfg, "username") || pass != str(f.cfg, "password")) {
		f.log.event("login", "fail", from, "user "+user+" rejected: invalid username or password")
		return nil, errors.New("Invalid username or password")
	}
	f.log.event("login", "ok", from, "user "+user+" accepted")
	return afero.NewBasePathFs(afero.NewOsFs(), str(f.cfg, "directory")), nil
}

func (f *ftpWidget) GetTLSConfig() (*tls.Config, error) {
	return nil, errors.New("TLS is not configured")
}

func (f *ftpWidget) start() (map[string]any, error) {
	f.srv = ftpserver.NewFtpServer(f)
	if err := f.srv.Listen(); err != nil {
		return nil, err
	}
	go f.srv.Serve()
	url := "ftp://" + net.JoinHostPort(str(f.cfg, "host"), strconv.Itoa(numCfg(f.cfg, "port")))
	return map[string]any{"serverInfo": map[string]any{"url": url, "path": str(f.cfg, "directory")},
		"msg": "Local FTP Server is running at " + url, "success": true}, nil
}

func (f *ftpWidget) stop() error {
	if f.srv == nil {
		return nil
	}
	return f.srv.Stop()
}

// ---- SSH Server (widget-ssh-server.js): password auth, shell, exec and SFTP confined to directory ----

type sshWidget struct {
	cfg map[string]any
	log *widgetLogger
	ln  net.Listener
}

func runSSHServer(cfg map[string]any, log *widgetLogger) (widgetInstance, map[string]any, error) {
	return &sshWidget{cfg: cfg, log: log}, nil, nil
}

func (s *sshWidget) start() (map[string]any, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, _ := ssh.NewSignerFromKey(key)
	conf := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if c.User() == str(s.cfg, "username") && string(pass) == str(s.cfg, "password") {
			s.log.event("auth", "ok", c.RemoteAddr().String(), "user "+c.User()+" accepted")
			return nil, nil
		}
		s.log.event("auth", "fail", c.RemoteAddr().String(), "user "+c.User()+" rejected (method password)")
		return nil, errors.New("authentication failed")
	}}
	conf.AddHostKey(signer)
	host, port := str(s.cfg, "host"), numCfg(s.cfg, "port")
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	s.ln = ln
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.log.event("connect", "info", nc.RemoteAddr().String(), "client from "+nc.RemoteAddr().String())
			go s.serve(nc, conf)
		}
	}()
	url := fmt.Sprintf("ssh://%s:%s@%s", str(s.cfg, "username"), str(s.cfg, "password"), net.JoinHostPort(host, strconv.Itoa(port)))
	s.log.log("info", "sftp/shell root: "+str(s.cfg, "directory"))
	return map[string]any{"serverInfo": map[string]any{"url": url, "path": str(s.cfg, "directory")},
		"msg": "SSH Server is running at " + url, "success": true}, nil
}

func (s *sshWidget) stop() error {
	if s.ln == nil {
		return nil
	}
	return s.ln.Close()
}

func defaultShell() string {
	if runtime.GOOS == "windows" {
		if c := os.Getenv("COMSPEC"); c != "" {
			return c
		}
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/bash"
}

func (s *sshWidget) serve(nc net.Conn, conf *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, conf)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	dir := str(s.cfg, "directory")
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "only sessions are supported")
			continue
		}
		ch, reqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			var p pty.Pty
			cols, rows := 80, 24
			for req := range reqs {
				switch req.Type {
				case "pty-req":
					var r struct {
						Term          string
						Cols, Rows    uint32
						Width, Height uint32
						Modes         string
					}
					ssh.Unmarshal(req.Payload, &r)
					cols, rows = int(r.Cols), int(r.Rows)
					req.Reply(true, nil)
				case "window-change":
					var r struct{ Cols, Rows, W, H uint32 }
					ssh.Unmarshal(req.Payload, &r)
					if p != nil {
						p.Resize(int(r.Cols), int(r.Rows))
					}
				case "shell":
					s.log.event("shell", "info", sc.RemoteAddr().String(), fmt.Sprintf("interactive shell (%dx%d)", cols, rows))
					if p, err = pty.New(); err != nil {
						req.Reply(false, nil)
						return
					}
					p.Resize(cols, rows)
					cmd := p.Command(defaultShell())
					cmd.Dir = dir
					if err := cmd.Start(); err != nil {
						req.Reply(false, nil)
						return
					}
					req.Reply(true, nil)
					go io.Copy(p, ch)
					go func() {
						io.Copy(ch, p)
						cmd.Wait()
						ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{0}))
						ch.Close()
					}()
				case "exec":
					var r struct{ Command string }
					ssh.Unmarshal(req.Payload, &r)
					s.log.event("exec", "info", sc.RemoteAddr().String(), r.Command)
					req.Reply(true, nil)
					var cmd *exec.Cmd
					if runtime.GOOS == "windows" {
						cmd = exec.Command("cmd.exe", "/c", r.Command)
					} else {
						cmd = exec.Command(defaultShell(), "-c", r.Command)
					}
					cmd.Dir, cmd.Stdout, cmd.Stderr = dir, ch, ch.Stderr()
					code := 0
					if err := cmd.Run(); err != nil {
						code = 1
						var ee *exec.ExitError
						if errors.As(err, &ee) {
							code = ee.ExitCode()
						}
					}
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
					return
				case "subsystem":
					var r struct{ Name string }
					ssh.Unmarshal(req.Payload, &r)
					if r.Name != "sftp" {
						req.Reply(false, nil)
						continue
					}
					s.log.event("sftp", "info", sc.RemoteAddr().String(), "SFTP subsystem")
					req.Reply(true, nil)
					srv := sftp.NewRequestServer(ch, rootedHandlers(dir))
					srv.Serve()
					srv.Close()
					return
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}

// rootFS serves SFTP requests inside root only, like widget-ssh-server.js confine().
type rootFS struct{ root string }

func rootedHandlers(root string) sftp.Handlers {
	r := &rootFS{root: root}
	return sftp.Handlers{FileGet: r, FilePut: r, FileCmd: r, FileList: r}
}

func (r *rootFS) abs(p string) string {
	return filepath.Join(r.root, filepath.FromSlash(path.Clean("/"+p)))
}

func (r *rootFS) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	return os.Open(r.abs(req.Filepath))
}

func (r *rootFS) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	flags := os.O_WRONLY | os.O_CREATE
	pf := req.Pflags()
	if pf.Trunc {
		flags |= os.O_TRUNC
	}
	if pf.Append {
		flags |= os.O_APPEND
	}
	return os.OpenFile(r.abs(req.Filepath), flags, 0o644)
}

func (r *rootFS) Filecmd(req *sftp.Request) error {
	p := r.abs(req.Filepath)
	switch req.Method {
	case "Setstat":
		if req.AttrFlags().Permissions {
			return os.Chmod(p, req.Attributes().FileMode())
		}
		return nil
	case "Rename", "PosixRename":
		return os.Rename(p, r.abs(req.Target))
	case "Rmdir":
		return os.Remove(p)
	case "Remove":
		return os.Remove(p)
	case "Mkdir":
		return os.Mkdir(p, 0o755)
	}
	return sftp.ErrSSHFxOpUnsupported
}

type fileList []os.FileInfo

func (l fileList) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}

func (r *rootFS) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	p := r.abs(req.Filepath)
	switch req.Method {
	case "List":
		des, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		var out fileList
		for _, d := range des {
			if fi, err := d.Info(); err == nil {
				out = append(out, fi)
			}
		}
		return out, nil
	case "Stat", "Lstat":
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		return fileList{fi}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

// ---- File Renamer (widget-rename.js) ----

var renameTag = regexp.MustCompile(`\{(n|name|ext|date|time|random|parent)(?::([^}]+))?\}`)

func renameFiles(cfg map[string]any) map[string]any {
	dir := str(cfg, "directory")
	if dir == "" {
		return map[string]any{"success": false, "error": "Directory must be specified"}
	}
	types := str(cfg, "fileTypes")
	var files []string
	var walk func(d string) error
	walk = func(d string) error {
		des, err := os.ReadDir(d)
		if err != nil {
			return err
		}
		for _, e := range des {
			full := filepath.Join(d, e.Name())
			if e.IsDir() && cfg["includeSubfolders"] == true {
				if err := walk(full); err != nil {
					return err
				}
			} else if e.Type().IsRegular() {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
				if types == "*" || types == "" || containsTrimmed(strings.Split(strings.ToLower(types), ","), ext) {
					files = append(files, full)
				}
			}
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	sort.Strings(files)
	start := numCfg(cfg, "startNumber")
	details := []any{}
	for i, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
		base := filepath.Base(f)
		ext := filepath.Ext(base)
		name := strings.TrimSuffix(base, ext)
		if cfg["preserveCase"] == false {
			name = strings.ToLower(name)
		}
		newName := renameTag.ReplaceAllStringFunc(str(cfg, "template"), func(m string) string {
			sub := renameTag.FindStringSubmatch(m)
			switch sub[1] {
			case "n":
				n := strconv.Itoa(start + i)
				if pad, err := strconv.Atoi(sub[2]); err == nil && len(n) < pad {
					n = strings.Repeat("0", pad-len(n)) + n
				}
				return n
			case "name":
				return name
			case "ext":
				return strings.TrimPrefix(ext, ".")
			case "date":
				return fi.ModTime().UTC().Format("2006-01-02")
			case "time":
				return fi.ModTime().Format("15-04-05")
			case "random":
				v, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
				return strconv.FormatInt(v.Int64(), 36)
			}
			return filepath.Base(filepath.Dir(f))
		})
		if strings.TrimSpace(newName) == "" || newName == "." || newName == ".." {
			return map[string]any{"success": false, "error": "Template produced an invalid file name"}
		}
		if strings.ContainsAny(newName, `/\`) {
			return map[string]any{"success": false, "error": "Template must not include path separators"}
		}
		newPath := filepath.Join(filepath.Dir(f), newName)
		if err := os.Rename(f, newPath); err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
		details = append(details, map[string]any{"oldPath": f, "newPath": newPath, "success": true})
	}
	return map[string]any{"success": true, "totalRenamed": len(files),
		"msg": fmt.Sprintf("Renamed %d files successfully", len(files)), "details": details}
}

func containsTrimmed(list []string, v string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) == v {
			return true
		}
	}
	return false
}
