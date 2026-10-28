import {Events} from "@wailsio/runtime";
import {type ITheme, Terminal} from "@xterm/xterm";
import {FitAddon} from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import "./style.css";
import {TermService} from "../bindings/wailsterm";
import type {Bookmark, FileEntry} from "../bindings/wailsterm/models";

type Tab = {id: string; term: Terminal; fit: FitAddon; el: HTMLDivElement; tabEl: HTMLDivElement};
type QuickCommand = {name: string; cmd: string};
type Settings = {fontSize: number; fontFamily: string; theme: "dark" | "light"; scrollback: number};

const themes: Record<Settings["theme"], ITheme> = {
    dark: {background: "#141414", foreground: "#dddddd", cursor: "#dddddd"},
    light: {background: "#fafafa", foreground: "#222222", cursor: "#222222", selectionBackground: "#bcd4f0"},
};

const tabs = new Map<string, Tab>();
let active = "";
let bookmarks: Bookmark[] = [];
let quick: QuickCommand[] = [];
let settings: Settings = {fontSize: 14, fontFamily: "Consolas, Menlo, monospace", theme: "dark", scrollback: 5000};

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;
const el = <K extends keyof HTMLElementTagNameMap>(tag: K, props: object = {}, ...kids: (Node | string)[]) => {
    const e = Object.assign(document.createElement(tag), props);
    e.append(...kids);
    return e;
};
const form = $<HTMLFormElement>("#bm-form");
const label = (b: Bookmark) => b.title || (b.type === "serial" ? b.host : `${b.user ? b.user + "@" : ""}${b.host}`);

async function load<T>(name: string, fallback: T): Promise<T> {
    const raw = await TermService.Load(name);
    return raw ? JSON.parse(raw) : fallback;
}
const save = (name: string, v: unknown) => TermService.Save(name, JSON.stringify(v));

// ---- tabs ----

function activate(id: string) {
    active = id;
    for (const t of tabs.values()) {
        t.el.classList.toggle("active", t.id === id);
        t.tabEl.classList.toggle("active", t.id === id);
    }
    const t = tabs.get(id);
    if (t) {
        t.fit.fit();
        t.term.focus();
    }
}

function closeTab(id: string) {
    const t = tabs.get(id);
    if (!t) return;
    TermService.Close(id);
    t.term.dispose();
    t.el.remove();
    t.tabEl.remove();
    tabs.delete(id);
    if (active === id) activate(tabs.keys().next().value ?? "");
}

function termOptions() {
    return {fontFamily: settings.fontFamily, fontSize: settings.fontSize, scrollback: settings.scrollback, theme: themes[settings.theme]};
}

async function openTab(title: string, open: (cols: number, rows: number) => Promise<string>, withSftp = false) {
    const termEl = el("div", {className: "term"});
    const pane = el("div", {className: "pane active"}, termEl);
    $("#terms").appendChild(pane);
    const term = new Terminal({...termOptions(), cursorBlink: true});
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(termEl);
    fit.fit();
    let id: string;
    try {
        id = await open(term.cols, term.rows);
    } catch (e) {
        term.dispose();
        pane.remove();
        alert(`${title}: ${e}`);
        return;
    }
    const tabEl = el("div", {className: "tab", onclick: () => activate(id)}, title);
    if (withSftp) {
        tabEl.append(el("b", {title: "Files (SFTP)", onclick: (e) => { e.stopPropagation(); toggleSftp(id); }}, "📁"));
    }
    tabEl.append(el("b", {onclick: (e) => { e.stopPropagation(); closeTab(id); }}, "×"));
    $("#tabs").appendChild(tabEl);
    term.onData((d) => TermService.Write(id, d));
    term.onResize(({cols, rows}) => TermService.Resize(id, cols, rows));
    tabs.set(id, {id, term, fit, el: pane, tabEl});
    activate(id);
}

Events.On("term:data", (ev: any) => {
    const {id, data} = ev.data;
    tabs.get(id)?.term.write(Uint8Array.from(atob(data), (c) => c.charCodeAt(0)));
});
Events.On("term:exit", (ev: any) => {
    tabs.get(ev.data)?.term.write("\r\n\x1b[33m[session closed]\x1b[0m\r\n");
});

new ResizeObserver(() => tabs.get(active)?.fit.fit()).observe($("#terms"));

// ---- SFTP ----

const fmtSize = (n: number) => n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} K` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} M` : `${(n / (1 << 30)).toFixed(1)} G`;
const join = (dir: string, name: string) => (dir.endsWith("/") ? dir : dir + "/") + name;
const parent = (dir: string) => dir.replace(/\/[^/]+\/?$/, "") || "/";

function toggleSftp(id: string) {
    const t = tabs.get(id)!;
    const existing = t.el.querySelector(".sftp");
    if (existing) {
        existing.remove();
        t.fit.fit();
        return;
    }
    let cwd = "";
    const pathInput = el("input", {placeholder: "path"});
    const tbody = el("tbody");
    const status = el("div", {className: "status"});

    async function run<T>(what: string, fn: () => Promise<T>): Promise<T | undefined> {
        status.textContent = what + "…";
        try {
            const r = await fn();
            status.textContent = "";
            return r;
        } catch (e) {
            status.textContent = `${what}: ${e}`;
            return undefined;
        }
    }

    async function list(dir: string) {
        const l = await run("Listing", () => TermService.SftpList(id, dir));
        if (!l) return;
        cwd = pathInput.value = l.path;
        const row = (f: FileEntry) => el("tr", {ondblclick: () => f.isDir ? list(join(cwd, f.name)) : download(f)},
            el("td", {title: f.name}, (f.isDir ? "📁 " : "📄 ") + f.name),
            el("td", {}, f.isDir ? "" : fmtSize(f.size)),
            el("td", {}, new Date(f.modTime * 1000).toLocaleString()),
            el("td", {}, f.mode),
            el("td", {},
                el("button", {title: "Rename", onclick: () => rename(f)}, "✎"),
                el("button", {title: "Delete", onclick: () => remove(f)}, "×")));
        tbody.replaceChildren(
            el("tr", {ondblclick: () => list(parent(cwd))}, el("td", {}, "📁 ..")),
            ...(l.entries ?? []).map(row));
    }
    async function download(f: FileEntry) {
        const to = await run(`Downloading ${f.name}`, () => TermService.SftpDownload(id, join(cwd, f.name)));
        if (to) status.textContent = `Saved ${to}`;
    }
    async function rename(f: FileEntry) {
        const name = prompt("Rename to", f.name);
        if (name && name !== f.name) {
            await run("Renaming", () => TermService.SftpRename(id, join(cwd, f.name), join(cwd, name)));
            list(cwd);
        }
    }
    async function remove(f: FileEntry) {
        if (confirm(`Delete ${f.name}${f.isDir ? " and everything in it" : ""}?`)) {
            await run("Deleting", () => TermService.SftpRemove(id, join(cwd, f.name)));
            list(cwd);
        }
    }

    pathInput.onkeydown = (e) => { if (e.key === "Enter") list(pathInput.value); };
    const panel = el("div", {className: "sftp"},
        el("div", {className: "bar"},
            el("button", {title: "Up", onclick: () => list(parent(cwd))}, "↑"),
            pathInput,
            el("button", {title: "Refresh", onclick: () => list(cwd)}, "⟳"),
            el("button", {
                title: "New folder", onclick: async () => {
                    const name = prompt("Folder name");
                    if (name) { await run("Creating", () => TermService.SftpMkdir(id, join(cwd, name))); list(cwd); }
                },
            }, "+📁"),
            el("button", {
                title: "Upload", onclick: async () => {
                    const n = await run("Uploading", () => TermService.SftpUpload(id, cwd));
                    if (n) { list(cwd); status.textContent = `Uploaded ${n} file(s)`; }
                },
            }, "⇪")),
        el("div", {className: "files"}, el("table", {}, tbody)),
        status);
    t.el.appendChild(panel);
    t.fit.fit();
    list("");
}

// ---- bookmarks ----

function readForm(): Bookmark {
    const f = Object.fromEntries(new FormData(form)) as Record<string, string>;
    return {...f, id: f.id || crypto.randomUUID(), port: Number(f.port) || 0} as Bookmark;
}

function connect(b: Bookmark) {
    const name = label(b);
    if (b.type === "telnet") openTab(name, () => TermService.OpenTelnet(b));
    else if (b.type === "serial") openTab(name, () => TermService.OpenSerial(b));
    else openTab(name, (c, r) => TermService.OpenSSH(b, c, r), true);
}

function renderBookmarks() {
    const q = $<HTMLInputElement>("#bm-filter").value.toLowerCase();
    $("#bookmarks").replaceChildren(...bookmarks
        .filter((b) => `${label(b)} ${b.host}`.toLowerCase().includes(q))
        .map((b) => el("li", {},
            el("span", {title: `${b.type || "ssh"} ${b.user}@${b.host}:${b.port}`, ondblclick: () => connect(b)}, label(b)),
            el("button", {title: "Connect", onclick: () => connect(b)}, "▶"),
            el("button", {
                title: "Edit", onclick: () => {
                    form.reset();
                    for (const [k, v] of Object.entries(b)) {
                        const input = form.elements.namedItem(k) as HTMLInputElement | null;
                        if (input) input.value = String(v ?? "");
                    }
                    form.closest("details")!.open = true;
                },
            }, "✎"),
            el("button", {
                title: "Delete", onclick: async () => {
                    if (!confirm(`Delete bookmark ${label(b)}?`)) return;
                    bookmarks = bookmarks.filter((x) => x.id !== b.id);
                    await save("bookmarks", bookmarks);
                    renderBookmarks();
                },
            }, "×"))));
}

form.onsubmit = async (e) => {
    e.preventDefault();
    const b = readForm();
    const i = bookmarks.findIndex((x) => x.id === b.id);
    if (i >= 0) bookmarks[i] = b; else bookmarks.push(b);
    await save("bookmarks", bookmarks);
    form.reset();
    renderBookmarks();
};
$("#bm-connect").onclick = () => form.reportValidity() && connect(readForm());
$("#bm-filter").oninput = renderBookmarks;
$<HTMLSelectElement>("#bm-form select[name=type]").onchange = async (e) => {
    if ((e.target as HTMLSelectElement).value !== "serial") return;
    const ports = await TermService.SerialPorts().catch(() => []);
    $("#serial-ports").replaceChildren(...(ports ?? []).map((p) => el("option", {value: p})));
};

// ---- quick commands ----

function renderQuick() {
    $("#quick").replaceChildren(...quick.map((q, i) => el("li", {},
        el("span", {title: q.cmd, onclick: () => active && TermService.Write(active, q.cmd + "\r")}, q.name),
        el("button", {
            title: "Delete", onclick: async () => {
                quick.splice(i, 1);
                await save("quickcommands", quick);
                renderQuick();
            },
        }, "×"))));
}

$<HTMLFormElement>("#qc-form").onsubmit = async (e) => {
    e.preventDefault();
    const f = e.target as HTMLFormElement;
    const {name, cmd} = Object.fromEntries(new FormData(f)) as QuickCommand;
    quick.push({name, cmd});
    await save("quickcommands", quick);
    f.reset();
    renderQuick();
};

// ---- settings ----

const settingsForm = $<HTMLFormElement>("#settings");

function applySettings() {
    document.body.classList.toggle("light", settings.theme === "light");
    for (const [k, v] of Object.entries(settings)) {
        (settingsForm.elements.namedItem(k) as HTMLInputElement).value = String(v);
    }
    for (const t of tabs.values()) {
        Object.assign(t.term.options, termOptions());
        t.fit.fit();
    }
}

settingsForm.onchange = () => {
    const f = Object.fromEntries(new FormData(settingsForm)) as Record<string, string>;
    settings = {...settings, ...f, fontSize: Number(f.fontSize) || 14, scrollback: Number(f.scrollback) || 5000} as Settings;
    applySettings();
    save("settings", settings);
};

// ---- startup ----

$("#new-local").onclick = () => openTab("Local", (c, r) => TermService.OpenLocal(c, r));

(async () => {
    settings = {...settings, ...await load("settings", {})};
    applySettings();
    bookmarks = await load("bookmarks", []);
    quick = await load("quickcommands", []);
    renderBookmarks();
    renderQuick();
    openTab("Local", (c, r) => TermService.OpenLocal(c, r));
})();
