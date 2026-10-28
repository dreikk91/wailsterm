import {useEffect, useRef} from "react";
import {Events} from "@wailsio/runtime";
import {Terminal} from "@xterm/xterm";
import {FitAddon} from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {TermService} from "../bindings/wailsterm";
import {type Settings, termThemes} from "./store";

type Sink = (d: Uint8Array | string) => void;
const sinks = new Map<string, Sink>();
// Output can arrive before the terminal has registered its sink (the backend starts pumping
// before Open* returns), so it is held here until then.
const pending = new Map<string, (Uint8Array | string)[]>();

function deliver(id: string, d: Uint8Array | string) {
    const sink = sinks.get(id);
    if (sink) sink(d);
    else pending.set(id, [...(pending.get(id) ?? []), d]);
}

Events.On("term:data", (ev: any) => {
    const {id, data} = ev.data;
    deliver(id, Uint8Array.from(atob(data), (c) => c.charCodeAt(0)));
});
Events.On("term:exit", (ev: any) => deliver(ev.data, "\r\n\x1b[33m[session closed]\x1b[0m\r\n"));

const options = (s: Settings) => ({
    fontFamily: s.fontFamily,
    fontSize: s.fontSize,
    scrollback: s.scrollback,
    cursorBlink: s.cursorBlink,
    theme: termThemes[s.theme],
});

type Props = {
    open: (cols: number, rows: number) => Promise<string>;
    settings: Settings;
    active: boolean;
    onSession: (id: string) => void;
};

export default function Term({open, settings, active, onSession}: Props) {
    const ref = useRef<HTMLDivElement>(null);
    const termRef = useRef<Terminal>(null);
    const settingsRef = useRef(settings);
    settingsRef.current = settings;

    useEffect(() => {
        const el = ref.current!;
        const term = new Terminal(options(settingsRef.current));
        const fit = new FitAddon();
        term.loadAddon(fit);
        term.open(el);
        fit.fit();
        termRef.current = term;

        let id = "";
        let disposed = false;
        open(term.cols, term.rows).then((sid) => {
            if (disposed) {
                TermService.Close(sid);
                return;
            }
            id = sid;
            sinks.set(id, (d) => term.write(d));
            for (const d of pending.get(id) ?? []) term.write(d);
            pending.delete(id);
            onSession(id);
        }, (e) => term.write(`\x1b[31m${e}\x1b[0m\r\n`));

        term.onData((d) => id && TermService.Write(id, d));
        term.onResize(({cols, rows}) => id && TermService.Resize(id, cols, rows));
        term.onSelectionChange(() => {
            if (settingsRef.current.copyOnSelect && term.hasSelection()) navigator.clipboard.writeText(term.getSelection());
        });
        // Ctrl+Shift+C / Ctrl+Shift+V copy and paste, as in electerm.
        term.attachCustomKeyEventHandler((e) => {
            if (e.type !== "keydown" || !e.ctrlKey || !e.shiftKey) return true;
            if (e.code === "KeyC" && term.hasSelection()) {
                navigator.clipboard.writeText(term.getSelection());
                return false;
            }
            if (e.code === "KeyV") {
                navigator.clipboard.readText().then((t) => term.paste(t));
                return false;
            }
            return true;
        });
        el.oncontextmenu = (e) => {
            e.preventDefault();
            if (term.hasSelection()) {
                navigator.clipboard.writeText(term.getSelection());
                term.clearSelection();
            } else navigator.clipboard.readText().then((t) => term.paste(t));
        };

        const ro = new ResizeObserver(() => el.offsetWidth && fit.fit());
        ro.observe(el);
        return () => {
            disposed = true;
            ro.disconnect();
            sinks.delete(id);
            if (id) TermService.Close(id);
            term.dispose();
        };
    }, []);

    useEffect(() => {
        if (termRef.current) Object.assign(termRef.current.options, options(settings));
    }, [settings]);

    useEffect(() => {
        if (active) termRef.current?.focus();
    }, [active]);

    return <div className="term" ref={ref}/>;
}
