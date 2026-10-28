import {Events} from "@wailsio/runtime";
import {Terminal} from "@xterm/xterm";
import {FitAddon} from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import "./style.css";
import {TermService} from "../bindings/wailsterm";
import type {Bookmark} from "../bindings/wailsterm/models";

type Tab = {id: string; term: Terminal; fit: FitAddon; el: HTMLDivElement; tabEl: HTMLDivElement};
const tabs = new Map<string, Tab>();
let active = "";
let bookmarks: Bookmark[] = [];

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;
const form = $<HTMLFormElement>("#bm-form");

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

async function openTab(title: string, open: (cols: number, rows: number) => Promise<string>) {
    const el = document.createElement("div");
    $("#terms").appendChild(el);
    el.classList.add("active");
    const term = new Terminal({fontFamily: "Consolas, Menlo, monospace", fontSize: 14, cursorBlink: true});
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();
    let id: string;
    try {
        id = await open(term.cols, term.rows);
    } catch (e) {
        term.dispose();
        el.remove();
        alert(`${title}: ${e}`);
        return;
    }
    const tabEl = document.createElement("div");
    tabEl.className = "tab";
    tabEl.textContent = title;
    const x = document.createElement("b");
    x.textContent = "×";
    x.onclick = (e) => { e.stopPropagation(); closeTab(id); };
    tabEl.appendChild(x);
    tabEl.onclick = () => activate(id);
    $("#tabs").appendChild(tabEl);
    term.onData((d) => TermService.Write(id, d));
    term.onResize(({cols, rows}) => TermService.Resize(id, cols, rows));
    tabs.set(id, {id, term, fit, el, tabEl});
    activate(id);
}

Events.On("term:data", (ev: any) => {
    const {id, data} = ev.data;
    const bin = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
    tabs.get(id)?.term.write(bin);
});
Events.On("term:exit", (ev: any) => {
    tabs.get(ev.data)?.term.write("\r\n\x1b[33m[session closed]\x1b[0m\r\n");
});

new ResizeObserver(() => tabs.get(active)?.fit.fit()).observe($("#terms"));

function readForm(): Bookmark {
    const f = Object.fromEntries(new FormData(form)) as Record<string, string>;
    return {...f, id: f.id || crypto.randomUUID(), port: Number(f.port) || 22} as Bookmark;
}

function connect(b: Bookmark) {
    openTab(b.title || `${b.user}@${b.host}`, (c, r) => TermService.OpenSSH(b, c, r));
}

function renderBookmarks() {
    const ul = $("#bookmarks");
    ul.replaceChildren(...bookmarks.map((b) => {
        const li = document.createElement("li");
        const name = document.createElement("span");
        name.textContent = b.title || `${b.user}@${b.host}`;
        name.title = `${b.user}@${b.host}:${b.port}`;
        name.ondblclick = () => connect(b);
        const edit = document.createElement("button");
        edit.textContent = "✎";
        edit.onclick = () => {
            for (const [k, v] of Object.entries(b)) {
                const input = form.elements.namedItem(k) as HTMLInputElement | null;
                if (input) input.value = String(v ?? "");
            }
        };
        const del = document.createElement("button");
        del.textContent = "×";
        del.onclick = async () => {
            bookmarks = bookmarks.filter((x) => x.id !== b.id);
            await TermService.SaveBookmarks(bookmarks);
            renderBookmarks();
        };
        li.append(name, edit, del);
        return li;
    }));
}

form.onsubmit = async (e) => {
    e.preventDefault();
    const b = readForm();
    const i = bookmarks.findIndex((x) => x.id === b.id);
    if (i >= 0) bookmarks[i] = b; else bookmarks.push(b);
    await TermService.SaveBookmarks(bookmarks);
    form.reset();
    renderBookmarks();
};
$("#bm-connect").onclick = () => form.reportValidity() && connect(readForm());
$("#new-local").onclick = () => openTab("Local", (c, r) => TermService.OpenLocal(c, r));

TermService.Bookmarks().then((bs) => { bookmarks = bs; renderBookmarks(); });
openTab("Local", (c, r) => TermService.OpenLocal(c, r));
