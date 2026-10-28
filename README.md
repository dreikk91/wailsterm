# wailsterm

Port of [electerm](https://github.com/electerm/electerm) to Go + Wails 3.

Done: tabs, local shell (ConPTY/pty), SSH (password, key, keyboard-interactive, TOFU known_hosts),
telnet, serial, SFTP panel (browse, upload, download, rename, delete, mkdir), bookmarks with filter,
quick commands, settings (font, theme, scrollback).
Not yet: transfer progress, local file pane, jump hosts/proxies, port forwarding, sync, i18n, AI.

Config lives in `<UserConfigDir>/wailsterm` (`bookmarks.json`, `quickcommands.json`, `settings.json`, `known_hosts`).

    wails3 dev     # live dev
    wails3 build   # -> bin/wailsterm.exe
