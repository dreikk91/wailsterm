# wailsterm

Port of [electerm](https://github.com/electerm/electerm) to Go + Wails 3.

UI is electerm's layout rebuilt with the same stack (React + antd + xterm.js) and its default themes:
icon sidebar, sliding bookmarks/history panel, tab bar with context menu, per-session Terminal/SFTP/Split
switch, two-pane SFTP (local <-> remote) with transfer progress, settings modal, footer batch input + quick commands.

Backend: local shell (ConPTY/pty), SSH (password, key, keyboard-interactive, TOFU known_hosts), telnet,
serial, SFTP (recursive upload/download, rename, delete, mkdir).

Not yet: jump hosts/proxies, port forwarding, terminal themes editor, sync, i18n, AI, RDP/VNC.

Config lives in `<UserConfigDir>/wailsterm` (`bookmarks.json`, `quickcommands.json`, `settings.json`, `history.json`, `known_hosts`).

    wails3 dev     # live dev
    wails3 build   # -> bin/wailsterm.exe
