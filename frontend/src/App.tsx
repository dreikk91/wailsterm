import {useEffect, useState} from "react";
import {App as AntApp, ConfigProvider, Empty, Input, Modal, theme as antTheme} from "antd";
import {TermService} from "../bindings/wailsterm";
import {
    type Bookmark, bookmarkLabel, defaultSettings, type HistoryItem, kindOf, load, parseQuickConnect,
    type QuickCommand, save, type Settings, type Tab, uiThemes, uid,
} from "./store";
import Sidebar from "./Sidebar";
import Tabs from "./Tabs";
import Session from "./Session";
import Footer from "./Footer";
import SettingModal, {type SettingTab} from "./SettingModal";

const localTab = (): Tab => ({key: uid(), title: "Local", kind: "local", gen: 0});

function Main({onTheme}: {onTheme: (t: Settings["theme"]) => void}) {
    const {message} = AntApp.useApp();
    const [tabs, setTabs] = useState<Tab[]>([]);
    const [active, setActive] = useState("");
    const [bookmarks, setBookmarks] = useState<Bookmark[]>([]);
    const [history, setHistory] = useState<HistoryItem[]>([]);
    const [quick, setQuick] = useState<QuickCommand[]>([]);
    const [settings, setSettings] = useState<Settings>(defaultSettings);
    const [settingOpen, setSettingOpen] = useState(false);
    const [settingTab, setSettingTab] = useState<SettingTab>("bookmarks");
    const [editing, setEditing] = useState<Bookmark | null>(null);
    const [quickConnect, setQuickConnect] = useState(false);
    const [about, setAbout] = useState(false);

    useEffect(() => {
        (async () => {
            setSettings({...defaultSettings, ...await load("settings", {})});
            setBookmarks(await load("bookmarks", []));
            setHistory(await load("history", []));
            setQuick(await load("quickcommands", []));
        })().catch((e) => message.error(`Failed to load config: ${e}`));
        addTab(localTab());
    }, []);

    useEffect(() => {
        for (const [k, v] of Object.entries(uiThemes[settings.theme])) document.documentElement.style.setProperty(`--${k}`, v);
        onTheme(settings.theme);
    }, [settings.theme]);

    function persist<T>(name: string, set: (v: T) => void, v: T) {
        set(v);
        save(name, v).catch((e) => message.error(`Save failed: ${e}`));
    }

    function addTab(t: Tab) {
        setTabs((ts) => [...ts, t]);
        setActive(t.key);
    }

    function connect(b: Bookmark) {
        addTab({key: uid(), title: bookmarkLabel(b), kind: kindOf(b), bookmark: b, gen: 0});
        const same = (h: Bookmark) => kindOf(h) === kindOf(b) && h.host === b.host && h.port === b.port && h.user === b.user;
        persist("history", setHistory, [{...b, time: Date.now()}, ...history.filter((h) => !same(h))].slice(0, 50));
    }

    function closeTabs(keys: string[]) {
        const rest = tabs.filter((t) => !keys.includes(t.key));
        setTabs(rest);
        if (keys.includes(active)) setActive(rest.at(-1)?.key ?? "");
    }

    const setSession = (key: string, sessionId: string) =>
        setTabs((ts) => ts.map((t) => (t.key === key ? {...t, sessionId} : t)));

    function send(cmd: string, all: boolean) {
        const targets = all ? tabs : tabs.filter((t) => t.key === active);
        for (const t of targets) if (t.sessionId) TermService.Write(t.sessionId, cmd + "\r");
    }

    function openSettings(tab: SettingTab, b: Bookmark | null = null) {
        setSettingTab(tab);
        setEditing(b);
        setSettingOpen(true);
    }

    function saveBookmark(b: Bookmark, andConnect: boolean) {
        const exists = bookmarks.some((x) => x.id === b.id);
        persist("bookmarks", setBookmarks, exists ? bookmarks.map((x) => (x.id === b.id ? b : x)) : [...bookmarks, b]);
        setEditing(b);
        message.success("Saved");
        if (andConnect) {
            setSettingOpen(false);
            connect(b);
        }
    }

    return (
        <div className="main">
            <Sidebar bookmarks={bookmarks} history={history} onConnect={connect}
                     onEditBookmark={(b) => openSettings("bookmarks", b)}
                     onClearHistory={() => persist("history", setHistory, [])}
                     onQuickConnect={() => setQuickConnect(true)}
                     onSettings={(t) => openSettings(t)} onAbout={() => setAbout(true)}/>
            <div className="content">
                <Tabs tabs={tabs} active={active} onActivate={setActive} onClose={closeTabs}
                      onDuplicate={(t) => addTab({...t, key: uid(), sessionId: undefined, gen: 0})}
                      onReconnect={(key) => setTabs((ts) => ts.map((t) => (t.key === key ? {...t, sessionId: undefined, gen: t.gen + 1} : t)))}
                      onNewLocal={() => addTab(localTab())}
                      onNewBookmark={() => openSettings("bookmarks")}
                      onQuickConnect={() => setQuickConnect(true)}/>
                <div className="sessions">
                    {tabs.length === 0 && (
                        <div className="no-session">
                            <Empty description="No session. Open a local terminal or connect to a bookmark."/>
                        </div>
                    )}
                    {tabs.map((t) => (
                        <Session key={`${t.key}:${t.gen}`} tab={t} active={t.key === active} settings={settings}
                                 onSession={(id) => setSession(t.key, id)}/>
                    ))}
                </div>
                <Footer quick={quick} onSend={send} onEditQuick={() => openSettings("quick")}/>
            </div>

            <SettingModal open={settingOpen} tab={settingTab} editing={editing} bookmarks={bookmarks} quick={quick}
                          settings={settings} onTab={setSettingTab} onEditing={setEditing}
                          onClose={() => setSettingOpen(false)} onSaveBookmark={saveBookmark}
                          onDeleteBookmark={(id) => { persist("bookmarks", setBookmarks, bookmarks.filter((b) => b.id !== id)); setEditing(null); }}
                          onQuick={(q) => persist("quickcommands", setQuick, q)}
                          onSettings={(s) => persist("settings", setSettings, s)}/>

            <Modal open={quickConnect} title="Quick connect" footer={null} destroyOnHidden onCancel={() => setQuickConnect(false)}>
                <Input.Search autoFocus enterButton="Connect" placeholder="user@host:22  or  telnet://host:23"
                              onSearch={(v) => {
                                  const b = parseQuickConnect(v);
                                  if (!b) {
                                      message.error("Format: [ssh|telnet://]user@host[:port]");
                                      return;
                                  }
                                  setQuickConnect(false);
                                  connect(b);
                              }}/>
                <p className="hint">Password will be asked by keyboard-interactive auth if the server supports it; otherwise save a bookmark.</p>
            </Modal>

            <Modal open={about} title="About wailsterm" footer={null} onCancel={() => setAbout(false)}>
                <p>wailsterm — terminal / SSH / SFTP client.</p>
                <p>Go + Wails 3 port of <b>electerm</b> (MIT, github.com/electerm/electerm).</p>
            </Modal>
        </div>
    );
}

export default function App() {
    const [theme, setTheme] = useState<Settings["theme"]>("dark");
    return (
        <ConfigProvider theme={{
            algorithm: theme === "dark" ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm,
            token: {colorPrimary: "#08c", borderRadius: 3, fontSize: 13},
        }}>
            <AntApp className="app-root">
                <Main onTheme={setTheme}/>
            </AntApp>
        </ConfigProvider>
    );
}
