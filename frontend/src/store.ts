import type {ITheme} from "@xterm/xterm";
import {TermService} from "../bindings/wailsterm";
import type {Bookmark} from "../bindings/wailsterm/models";

export type {Bookmark};
export type Kind = "local" | "ssh" | "telnet" | "serial";
export type QuickCommand = {id: string; name: string; cmd: string};
export type HistoryItem = Bookmark & {time: number};
export type Settings = {
    fontSize: number;
    fontFamily: string;
    theme: "dark" | "light";
    scrollback: number;
    cursorBlink: boolean;
    copyOnSelect: boolean;
};
export type Tab = {key: string; title: string; kind: Kind; bookmark?: Bookmark; sessionId?: string; gen: number};

export const defaultSettings: Settings = {
    fontSize: 14,
    fontFamily: "Consolas, Menlo, 'DejaVu Sans Mono', monospace",
    theme: "dark",
    scrollback: 5000,
    cursorBlink: true,
    copyOnSelect: false,
};

// UI and terminal palettes are electerm's defaults (src/client/common/theme-defaults.js).
export const uiThemes: Record<Settings["theme"], Record<string, string>> = {
    dark: {
        "main-dark": "#000", "main-light": "#2E3338", main: "#121214", text: "#ddd", "text-light": "#fff",
        "text-dark": "#888", "text-disabled": "#777", primary: "#08c", info: "#FFD166", success: "#06D6A0",
        error: "#EF476F", warn: "#E55934", border: "#2E3338",
    },
    light: {
        main: "#ededed", "main-dark": "#cccccc", "main-light": "#fefefe", text: "#555", "text-light": "#777",
        "text-dark": "#444", "text-disabled": "#888", primary: "#08c", info: "#FFD166", success: "#06D6A0",
        error: "#EF476F", warn: "#E55934", border: "#ccc",
    },
};

export const termThemes: Record<Settings["theme"], ITheme> = {
    dark: {
        foreground: "#bbbbbb", background: "#20111b", cursor: "#b5bd68", cursorAccent: "#1d1f21",
        selectionBackground: "rgba(200, 200, 200, 0.6)", black: "#575757", red: "#FF2C6D", green: "#19f9d8",
        yellow: "#FFB86C", blue: "#45A9F9", magenta: "#FF75B5", cyan: "#B084EB", white: "#CDCDCD",
        brightBlack: "#757575", brightRed: "#FF2C6D", brightGreen: "#19f9d8", brightYellow: "#FFCC95",
        brightBlue: "#6FC1FF", brightMagenta: "#FF9AC1", brightCyan: "#BCAAFE", brightWhite: "#E6E6E6",
    },
    light: {
        background: "#121214", foreground: "#af9a91", cursor: "#af9a91", selectionBackground: "#575256",
        cursorAccent: "#121214", black: "#572100", red: "#ba3934", green: "#91773f", yellow: "#b55600",
        blue: "#5f63b4", magenta: "#a17c7b", cyan: "#8faea9", white: "#af9a91", brightBlack: "#4e4b61",
        brightRed: "#d9443f", brightGreen: "#d6b04e", brightYellow: "#f66813", brightBlue: "#8086ef",
        brightMagenta: "#e2c2bb", brightCyan: "#a4dce7", brightWhite: "#d2c7a9",
    },
};

export async function load<T>(name: string, fallback: T): Promise<T> {
    const raw = await TermService.Load(name);
    return raw ? JSON.parse(raw) : fallback;
}

export const save = (name: string, v: unknown) => TermService.Save(name, JSON.stringify(v));

export const uid = () => crypto.randomUUID();

export const kindOf = (b: Bookmark): Kind => (b.type || "ssh") as Kind;

export function bookmarkLabel(b: Bookmark) {
    if (b.title) return b.title;
    if (kindOf(b) === "serial") return b.host;
    return `${b.user ? b.user + "@" : ""}${b.host}${b.port ? ":" + b.port : ""}`;
}

export function emptyBookmark(): Bookmark {
    return {id: "", type: "ssh", title: "", host: "", port: 22, user: "", password: "", keyPath: ""};
}

// parseQuickConnect accepts electerm-style "[ssh|telnet://]user@host[:port]"; returns null when unusable.
export function parseQuickConnect(s: string): Bookmark | null {
    const m = s.trim().match(/^(?:(ssh|telnet):\/\/)?(?:([^@\s]+)@)?([^:\s/@]+)(?::(\d+))?$/i);
    if (!m) return null;
    const type = (m[1] || "ssh").toLowerCase();
    return {...emptyBookmark(), id: uid(), type, user: m[2] || "", host: m[3], port: Number(m[4]) || (type === "telnet" ? 23 : 22)};
}

export const fmtSize = (n: number) =>
    n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KB` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${(n / (1 << 30)).toFixed(1)} GB`;
