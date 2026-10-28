import {Dropdown} from "antd";
import {
    CloseOutlined, CodeOutlined, CopyOutlined, PlusOutlined, ReloadOutlined, ThunderboltOutlined, BookOutlined,
} from "@ant-design/icons";
import type {Tab} from "./store";

type Props = {
    tabs: Tab[];
    active: string;
    onActivate: (key: string) => void;
    onClose: (keys: string[]) => void;
    onDuplicate: (tab: Tab) => void;
    onReconnect: (key: string) => void;
    onNewLocal: () => void;
    onNewBookmark: () => void;
    onQuickConnect: () => void;
};

export default function Tabs(p: Props) {
    return (
        <div className="tabs">
            <div className="tabs-inner">
                {p.tabs.map((t, i) => (
                    <Dropdown key={t.key} trigger={["contextMenu"]} menu={{
                        items: [
                            {key: "dup", label: "Duplicate", icon: <CopyOutlined/>, onClick: () => p.onDuplicate(t)},
                            {key: "re", label: "Reconnect", icon: <ReloadOutlined/>, onClick: () => p.onReconnect(t.key)},
                            {type: "divider"},
                            {key: "close", label: "Close", icon: <CloseOutlined/>, onClick: () => p.onClose([t.key])},
                            {key: "others", label: "Close others", onClick: () => p.onClose(p.tabs.filter((x) => x.key !== t.key).map((x) => x.key))},
                            {key: "right", label: "Close tabs to the right", onClick: () => p.onClose(p.tabs.slice(i + 1).map((x) => x.key))},
                        ],
                    }}>
                        <div className={`tab${t.key === p.active ? " active" : ""}`}
                             onClick={() => p.onActivate(t.key)}
                             onMouseDown={(e) => e.button === 1 && p.onClose([t.key])}
                             title={t.title}>
                            <span className="tab-title">{t.title}</span>
                            <CloseOutlined className="tab-close" onClick={(e) => { e.stopPropagation(); p.onClose([t.key]); }}/>
                        </div>
                    </Dropdown>
                ))}
                <Dropdown trigger={["click", "hover"]} menu={{
                    items: [
                        {key: "local", label: "New local terminal", icon: <CodeOutlined/>, onClick: p.onNewLocal},
                        {key: "bm", label: "New bookmark", icon: <BookOutlined/>, onClick: p.onNewBookmark},
                        {key: "qc", label: "Quick connect", icon: <ThunderboltOutlined/>, onClick: p.onQuickConnect},
                    ],
                }}>
                    <div className="tab-add" onClick={p.onNewLocal}><PlusOutlined/></div>
                </Dropdown>
            </div>
        </div>
    );
}
