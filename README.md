# wailsterm

[electerm](https://github.com/electerm/electerm) with its Node/Electron backend rewritten in Go, running in a Wails 3 window.

- `ui/client` is electerm's `src/client` (MIT, see `ui/LICENSE.electerm`), unmodified except for the VNC file
  transfer drawer (`vnc/vnc-session.jsx`, `session/session.jsx`, `sftp/sftp-entry.jsx`), "Login with GitHub" (device flow) in
  `setting-sync/setting-sync-form.jsx`, and terminal text contrast
  on light themes (`terminal/mixins/term-init.js`, `terminal/terminal-color-query.mjs`).
- `ui/shim/api.js` replaces Electron's preload bridge (`window.api`) with HTTP calls.
- The Go side (`server.go`, `ipc.go`, `sessions.go`, `sftp.go`, `fsrpc.go`, `db.go`) implements the protocols
  electerm's client already speaks: `/common/s`, `/terminals/:pid`, `/sftp/:id`, `/transfer/:id` WebSockets,
  plus the `runSync` / `runGlobalAsync` IPC tables. It serves the UI on a random loopback port with a per-run
  token; the Wails window just loads that URL.

Ported: local terminal (ConPTY/pty); SSH with password, private key (inline or path), keyboard-interactive
prompts, ssh-agent (Windows OpenSSH pipe / SSH_AUTH_SOCK) and agent forwarding, host key confirmation against
~/.ssh/known_hosts, jump hosts (connection hopping), socks4/4a/socks5/http proxies, tunnels (local, remote, dynamic SOCKS), X11 forwarding (to a local VcXsrv/Xming/XQuartz/X.org),
keepalive; telnet; serial; FTP/FTPS; VNC (noVNC bridge, plus TightVNC 2.x file transfer over a second shared connection) and RDP (RDCleanPath proxy for the IronRDP client),
both also via proxy/jump hosts; SPICE (per-channel bridge, TLS with CA and host-subject checks); web tabs (iframe); trzsz (trz/tsz) in any
terminal, plus rz/sz where a local lrzsz is installed; SFTP file manager with upload/download/pause/resume; local
file manager; terminal session logs; ~/.ssh/config import; terminal theme catalog; font list; external editor;
settings sync (GitHub/Gitee gist, custom server, electerm cloud, WebDAV; encryption compatible with electerm);
AI assistant (OpenAI chat/responses and Anthropic formats, streaming); built-in widgets (static file server,
FTP server, SSH server, file renamer, batch operation); bookmarks, settings, themes, quick commands and the rest of the UI state
(encrypted at rest with DPAPI on Windows), seeded with electerm's first-run defaults.

Not ported yet (hidden or inert in the UI): built-in
zmodem (needs a native Go implementation; today it relies on a local lrzsz) and xmodem, the MCP server widget,
user widgets (they are Node.js modules), auto-upgrade.

Data lives in `<UserConfigDir>/wailsterm` (override with `WAILSTERM_DATA`).

    cd ui && npm install && cd ..
    wails3 build                         # -> bin/wailsterm.exe
    WAILSTERM_ADDR=127.0.0.1:38999 bin/wailsterm.exe   # fixed port: open the same UI in a browser for debugging
