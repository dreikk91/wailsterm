# wailsterm

[electerm](https://github.com/electerm/electerm) with its Node/Electron backend rewritten in Go, running in a Wails 3 window.

- `ui/client` is electerm's `src/client` (MIT, see `ui/LICENSE.electerm`), unmodified.
- `ui/shim/api.js` replaces Electron's preload bridge (`window.api`) with HTTP calls.
- The Go side (`server.go`, `ipc.go`, `sessions.go`, `sftp.go`, `fsrpc.go`, `db.go`) implements the protocols
  electerm's client already speaks: `/common/s`, `/terminals/:pid`, `/sftp/:id`, `/transfer/:id` WebSockets,
  plus the `runSync` / `runGlobalAsync` IPC tables. It serves the UI on a random loopback port with a per-run
  token; the Wails window just loads that URL.

Ported: local terminal (ConPTY/pty); SSH with password, private key (inline or path), keyboard-interactive
prompts, ssh-agent (Windows OpenSSH pipe / SSH_AUTH_SOCK) and agent forwarding, host key confirmation against
~/.ssh/known_hosts, jump hosts (connection hopping), socks5/http proxies, tunnels (local, remote, dynamic SOCKS),
keepalive; telnet; serial; SFTP file manager with upload/download/pause/resume; local file manager; terminal
session logs; ~/.ssh/config import; bookmarks, settings, themes, quick commands and the rest of the UI state
(encrypted at rest with DPAPI on Windows), seeded with electerm's first-run defaults.

Not ported yet (hidden or inert in the UI): RDP, VNC, SPICE, FTP, web tabs, socks4 proxy, X11 forwarding,
zmodem/trzsz/xmodem, sync, widgets, AI, auto-upgrade.

Data lives in `<UserConfigDir>/wailsterm` (override with `WAILSTERM_DATA`).

    cd ui && npm install && cd ..
    wails3 build                         # -> bin/wailsterm.exe
    WAILSTERM_ADDR=127.0.0.1:38999 bin/wailsterm.exe   # fixed port: open the same UI in a browser for debugging
