# wailsterm

Port of [electerm](https://github.com/electerm/electerm) to Go + Wails 3.

Done: tabs, local shell (ConPTY/pty), SSH terminal (password, key, keyboard-interactive, TOFU known_hosts), bookmarks.
Not yet: SFTP file manager, serial/telnet, quick commands, themes, sync, i18n.

Config lives in `<UserConfigDir>/wailsterm` (`bookmarks.json`, `known_hosts`).

    wails3 dev     # live dev
    wails3 build   # -> bin/wailsterm.exe
