import {useEffect, useState} from "react";
import {Events} from "@wailsio/runtime";
import {App, Button, Dropdown, Input, Progress, Space, Table, Tooltip} from "antd";
import {
    ArrowLeftOutlined, ArrowRightOutlined, ArrowUpOutlined, DeleteOutlined, EditOutlined,
    FileOutlined, FolderAddOutlined, FolderFilled, HomeOutlined, ReloadOutlined,
} from "@ant-design/icons";
import {TermService} from "../bindings/wailsterm";
import type {FileEntry, Listing} from "../bindings/wailsterm/models";
import {fmtSize} from "./store";

// Prog mirrors Go's Progress; it is only sent as an event payload, so no binding is generated for it.
type Prog = {id: string; name: string; done: number; total: number; error: string; end: boolean};

type PaneProps = {
    title: string;
    fsId: string; // "" = local disk
    reload: number;
    onCwd: (cwd: string) => void;
    transfer: {label: string; icon: React.ReactNode; run: (paths: string[]) => void};
};

function FilePane({title, fsId, reload, onCwd, transfer}: PaneProps) {
    const {message, modal} = App.useApp();
    const [listing, setListing] = useState<Listing | null>(null);
    const [pathInput, setPathInput] = useState("");
    const [loading, setLoading] = useState(false);
    const [selected, setSelected] = useState<string[]>([]);
    const [filter, setFilter] = useState("");

    async function list(dir: string) {
        setLoading(true);
        try {
            const l = await TermService.FsList(fsId, dir);
            setListing(l);
            setPathInput(l.path);
            setSelected([]);
            onCwd(l.path);
        } catch (e) {
            message.error(`${e}`);
        } finally {
            setLoading(false);
        }
    }

    useEffect(() => { list(listing?.path ?? ""); }, [reload]);

    const cwd = listing?.path ?? "";
    const join = (name: string) => cwd + (cwd.endsWith(listing?.sep ?? "/") ? "" : listing?.sep ?? "/") + name;

    async function act(fn: () => Promise<unknown>) {
        try {
            await fn();
        } catch (e) {
            message.error(`${e}`);
        }
        list(cwd);
    }

    function ask(title: string, initial: string, then: (v: string) => void) {
        let v = initial;
        modal.confirm({
            title, icon: null,
            content: <Input defaultValue={initial} autoFocus onChange={(e) => (v = e.target.value)}/>,
            onOk: () => v && then(v),
        });
    }

    const remove = (paths: string[]) => modal.confirm({
        title: `Delete ${paths.length} item(s)?`,
        content: paths.map((p) => p.split(/[\\/]/).pop()).join(", "),
        okType: "danger",
        onOk: () => act(() => Promise.all(paths.map((p) => TermService.FsRemove(fsId, p)))),
    });
    const rename = (f: FileEntry) => ask("Rename", f.name, (n) => n !== f.name && act(() => TermService.FsRename(fsId, f.path, join(n))));
    const mkdir = () => ask("New folder", "", (n) => act(() => TermService.FsMkdir(fsId, join(n))));

    const entries = (listing?.entries ?? []).filter((f) => f.name.toLowerCase().includes(filter.toLowerCase()));

    return (
        <div className="file-pane">
            <div className="file-pane-title">{title}</div>
            <Space.Compact className="address-bar" size="small">
                <Tooltip title="Up"><Button icon={<ArrowUpOutlined/>} onClick={() => list(listing?.parent ?? "")}/></Tooltip>
                <Tooltip title="Home"><Button icon={<HomeOutlined/>} onClick={() => list("")}/></Tooltip>
                <Input value={pathInput} onChange={(e) => setPathInput(e.target.value)} onPressEnter={() => list(pathInput)}/>
                <Tooltip title="Refresh"><Button icon={<ReloadOutlined/>} onClick={() => list(cwd)}/></Tooltip>
                <Tooltip title="New folder"><Button icon={<FolderAddOutlined/>} onClick={mkdir}/></Tooltip>
            </Space.Compact>
            <Space.Compact className="address-bar" size="small">
                <Input placeholder="Filter" allowClear value={filter} onChange={(e) => setFilter(e.target.value)}/>
                <Button type="primary" icon={transfer.icon} disabled={!selected.length} onClick={() => transfer.run(selected)}>
                    {transfer.label} {selected.length || ""}
                </Button>
                <Button danger icon={<DeleteOutlined/>} disabled={!selected.length} onClick={() => remove(selected)}/>
            </Space.Compact>
            <div className="file-table">
                <Table<FileEntry>
                    size="small"
                    rowKey="path"
                    loading={loading}
                    pagination={false}
                    dataSource={entries}
                    sticky
                    rowSelection={{selectedRowKeys: selected, onChange: (k) => setSelected(k as string[]), columnWidth: 32}}
                    onRow={(f) => ({
                        onDoubleClick: () => f.isDir ? list(f.path) : transfer.run([f.path]),
                        onClick: (e) => !(e.target as HTMLElement).closest(".ant-checkbox-wrapper") &&
                            setSelected(e.ctrlKey ? [...new Set([...selected, f.path])] : [f.path]),
                    })}
                    columns={[
                        {
                            title: "Name", dataIndex: "name", ellipsis: true,
                            sorter: (a, b) => a.name.localeCompare(b.name),
                            render: (_, f) => (
                                <Dropdown trigger={["contextMenu"]} menu={{
                                    items: [
                                        {key: "t", label: transfer.label, icon: transfer.icon, onClick: () => transfer.run(selected.includes(f.path) ? selected : [f.path])},
                                        {key: "r", label: "Rename", icon: <EditOutlined/>, onClick: () => rename(f)},
                                        {key: "d", label: "Delete", icon: <DeleteOutlined/>, danger: true, onClick: () => remove(selected.includes(f.path) ? selected : [f.path])},
                                    ],
                                }}>
                                    <span className="file-name">{f.isDir ? <FolderFilled className="dir-icon"/> : <FileOutlined/>} {f.name}</span>
                                </Dropdown>
                            ),
                        },
                        {title: "Size", dataIndex: "size", width: 90, align: "right", sorter: (a, b) => a.size - b.size, render: (s, f) => f.isDir ? "" : fmtSize(s)},
                        {title: "Modified", dataIndex: "modTime", width: 150, sorter: (a, b) => a.modTime - b.modTime, render: (t) => new Date(t * 1000).toLocaleString()},
                        {title: "Mode", dataIndex: "mode", width: 100, responsive: ["xl"]},
                    ]}
                />
            </div>
        </div>
    );
}

export default function Sftp({sessionId}: {sessionId: string}) {
    const {message} = App.useApp();
    const [localCwd, setLocalCwd] = useState("");
    const [remoteCwd, setRemoteCwd] = useState("");
    const [reloadLocal, setReloadLocal] = useState(0);
    const [reloadRemote, setReloadRemote] = useState(0);
    const [transfers, setTransfers] = useState<Record<string, Prog>>({});

    useEffect(() => Events.On("transfer", (ev: any) => {
        const p = ev.data as Prog;
        if (!p.id.startsWith(sessionId + ":")) return;
        setTransfers((t) => ({...t, [p.id]: p}));
        if (p.end) setTimeout(() => setTransfers(({[p.id]: _, ...rest}) => rest), p.error ? 8000 : 2000);
    }), [sessionId]);

    async function transfer(upload: boolean, paths: string[]) {
        try {
            await TermService.Transfer(sessionId, upload, paths, upload ? remoteCwd : localCwd);
        } catch (e) {
            message.error(`Transfer failed: ${e}`);
        }
        (upload ? setReloadRemote : setReloadLocal)((n) => n + 1);
    }

    return (
        <div className="sftp">
            <div className="sftp-panes">
                <FilePane title="Local" fsId="" reload={reloadLocal} onCwd={setLocalCwd}
                          transfer={{label: "Upload", icon: <ArrowRightOutlined/>, run: (p) => transfer(true, p)}}/>
                <FilePane title="Remote" fsId={sessionId} reload={reloadRemote} onCwd={setRemoteCwd}
                          transfer={{label: "Download", icon: <ArrowLeftOutlined/>, run: (p) => transfer(false, p)}}/>
            </div>
            {Object.values(transfers).length > 0 && (
                <div className="transfers">
                    {Object.values(transfers).map((p) => (
                        <div key={p.id} className="transfer-item">
                            <span className="transfer-name">{p.error || p.name}</span>
                            <Progress size="small" status={p.error ? "exception" : p.end ? "success" : "active"}
                                      percent={p.total ? Math.floor((p.done / p.total) * 100) : 100}
                                      format={() => `${fmtSize(p.done)} / ${fmtSize(p.total)}`}/>
                        </div>
                    ))}
                </div>
            )}
        </div>
    );
}
