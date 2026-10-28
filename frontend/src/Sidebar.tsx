import {useState} from "react";
import {Button, Empty, Input, Segmented, Tooltip} from "antd";
import {
    BookOutlined, CloseOutlined, DeleteOutlined, EditOutlined, InfoCircleOutlined, PlusCircleOutlined,
    PushpinOutlined, SettingOutlined, ThunderboltOutlined, BgColorsOutlined,
} from "@ant-design/icons";
import {type Bookmark, bookmarkLabel, type HistoryItem, kindOf} from "./store";

type Props = {
    bookmarks: Bookmark[];
    history: HistoryItem[];
    onConnect: (b: Bookmark) => void;
    onEditBookmark: (b: Bookmark | null) => void;
    onClearHistory: () => void;
    onQuickConnect: () => void;
    onSettings: (tab: "bookmarks" | "quick" | "settings") => void;
    onAbout: () => void;
};

function SideIcon({title, icon, active, onClick}: {title: string; icon: React.ReactNode; active?: boolean; onClick: () => void}) {
    return (
        <Tooltip title={title} placement="right">
            <div className={`side-icon${active ? " active" : ""}`} onClick={onClick}>{icon}</div>
        </Tooltip>
    );
}

export default function Sidebar(p: Props) {
    const [open, setOpen] = useState(false);
    const [pinned, setPinned] = useState(false);
    const [tab, setTab] = useState<"bookmarks" | "history">("bookmarks");
    const [q, setQ] = useState("");

    const match = (b: Bookmark) => `${bookmarkLabel(b)} ${b.host} ${b.user}`.toLowerCase().includes(q.toLowerCase());
    const connect = (b: Bookmark) => {
        p.onConnect(b);
        if (!pinned) setOpen(false);
    };
    const items = tab === "bookmarks" ? p.bookmarks.filter(match) : p.history.filter(match);

    return (
        <>
            <div className="sidebar">
                <SideIcon title="New bookmark" icon={<PlusCircleOutlined/>} onClick={() => p.onEditBookmark(null)}/>
                <SideIcon title="Quick connect" icon={<ThunderboltOutlined/>} onClick={p.onQuickConnect}/>
                <SideIcon title="Bookmarks" icon={<BookOutlined/>} active={open} onClick={() => setOpen(!open)}/>
                <SideIcon title="Terminal themes" icon={<BgColorsOutlined/>} onClick={() => p.onSettings("settings")}/>
                <SideIcon title="Settings" icon={<SettingOutlined/>} onClick={() => p.onSettings("settings")}/>
                <div className="sidebar-spacer"/>
                <SideIcon title="About" icon={<InfoCircleOutlined/>} onClick={p.onAbout}/>
            </div>
            {open && (
                <div className={`sidebar-panel${pinned ? " pinned" : ""}`}>
                    <div className="sidebar-panel-head">
                        <Segmented size="small" value={tab} onChange={setTab}
                                   options={[{value: "bookmarks", label: "Bookmarks"}, {value: "history", label: "History"}]}/>
                        <span>
                            <Tooltip title={pinned ? "Unpin" : "Pin"}>
                                <Button type="text" size="small" icon={<PushpinOutlined rotate={pinned ? 0 : -45}/>} onClick={() => setPinned(!pinned)}/>
                            </Tooltip>
                            <Button type="text" size="small" icon={<CloseOutlined/>} onClick={() => setOpen(false)}/>
                        </span>
                    </div>
                    <div className="sidebar-panel-search">
                        <Input.Search size="small" placeholder="Search" allowClear value={q} onChange={(e) => setQ(e.target.value)}/>
                        {tab === "bookmarks"
                            ? <Button size="small" icon={<PlusCircleOutlined/>} onClick={() => p.onEditBookmark(null)}/>
                            : <Tooltip title="Clear history"><Button size="small" icon={<DeleteOutlined/>} onClick={p.onClearHistory}/></Tooltip>}
                    </div>
                    <div className="item-list">
                        {items.length === 0 && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}/>}
                        {items.map((b, i) => (
                            <div key={tab === "history" ? i : b.id} className="item-list-unit" onClick={() => connect(b)}
                                 title={`${kindOf(b)} ${b.user ? b.user + "@" : ""}${b.host}${b.port ? ":" + b.port : ""}`}>
                                <span className="item-type">{kindOf(b)}</span>
                                <span className="item-title">{bookmarkLabel(b)}</span>
                                {tab === "bookmarks" && (
                                    <EditOutlined className="item-edit" onClick={(e) => { e.stopPropagation(); p.onEditBookmark(b); }}/>
                                )}
                                {tab === "history" && <span className="item-time">{new Date((b as HistoryItem).time).toLocaleDateString()}</span>}
                            </div>
                        ))}
                    </div>
                </div>
            )}
        </>
    );
}
