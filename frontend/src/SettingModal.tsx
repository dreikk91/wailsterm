import {useEffect, useState} from "react";
import {AutoComplete, Button, Form, Input, InputNumber, Modal, Popconfirm, Radio, Select, Space, Switch, Table, Tabs} from "antd";
import {DeleteOutlined, PlusOutlined} from "@ant-design/icons";
import {TermService} from "../bindings/wailsterm";
import {type Bookmark, bookmarkLabel, emptyBookmark, kindOf, type QuickCommand, type Settings, uid} from "./store";

export type SettingTab = "bookmarks" | "quick" | "settings";

type Props = {
    open: boolean;
    tab: SettingTab;
    editing: Bookmark | null;
    bookmarks: Bookmark[];
    quick: QuickCommand[];
    settings: Settings;
    onTab: (t: SettingTab) => void;
    onEditing: (b: Bookmark | null) => void;
    onClose: () => void;
    onSaveBookmark: (b: Bookmark, connect: boolean) => void;
    onDeleteBookmark: (id: string) => void;
    onQuick: (q: QuickCommand[]) => void;
    onSettings: (s: Settings) => void;
};

function BookmarkForm({bookmark, onSave, onDelete}: {bookmark: Bookmark | null; onSave: (b: Bookmark, connect: boolean) => void; onDelete: (id: string) => void}) {
    const [form] = Form.useForm<Bookmark>();
    const type = Form.useWatch("type", form) ?? "ssh";
    const [ports, setPorts] = useState<string[]>([]);

    useEffect(() => {
        form.resetFields();
        form.setFieldsValue(bookmark ?? emptyBookmark());
    }, [bookmark]);
    useEffect(() => {
        if (type === "serial") TermService.SerialPorts().then((p) => setPorts(p ?? []), () => setPorts([]));
    }, [type]);

    const submit = (connect: boolean) => form.validateFields().then((v) =>
        onSave({...emptyBookmark(), ...v, id: bookmark?.id || uid(), port: Number(v.port) || 0}, connect));

    return (
        <Form form={form} layout="horizontal" labelCol={{span: 6}} wrapperCol={{span: 16}} className="bookmark-form">
            <Form.Item name="type" label="Type">
                <Radio.Group optionType="button" buttonStyle="solid" size="small"
                             onChange={(e) => form.setFieldValue("port", {ssh: 22, telnet: 23, serial: 9600}[e.target.value as string])}
                             options={[{value: "ssh", label: "SSH"}, {value: "telnet", label: "Telnet"}, {value: "serial", label: "Serial"}]}/>
            </Form.Item>
            <Form.Item name="title" label="Title"><Input/></Form.Item>
            {type === "serial" ? (
                <Form.Item name="host" label="Path" rules={[{required: true}]}>
                    <AutoComplete options={ports.map((p) => ({value: p}))} placeholder="COM3 / /dev/ttyUSB0"/>
                </Form.Item>
            ) : (
                <Form.Item name="host" label="Host" rules={[{required: true}]}><Input/></Form.Item>
            )}
            <Form.Item name="port" label={type === "serial" ? "Baud rate" : "Port"}><InputNumber min={1} style={{width: "100%"}}/></Form.Item>
            {type === "ssh" && (
                <>
                    <Form.Item name="user" label="Username" rules={[{required: true}]}><Input/></Form.Item>
                    <Form.Item name="password" label="Password"><Input.Password placeholder="Password or key passphrase"/></Form.Item>
                    <Form.Item name="keyPath" label="Private key"><Input placeholder="C:\Users\me\.ssh\id_ed25519"/></Form.Item>
                </>
            )}
            <Form.Item wrapperCol={{offset: 6}}>
                <Space>
                    <Button type="primary" onClick={() => submit(false)}>Save</Button>
                    <Button onClick={() => submit(true)}>Save and connect</Button>
                    {bookmark?.id && (
                        <Popconfirm title="Delete this bookmark?" onConfirm={() => onDelete(bookmark.id)}>
                            <Button danger icon={<DeleteOutlined/>}>Delete</Button>
                        </Popconfirm>
                    )}
                </Space>
            </Form.Item>
        </Form>
    );
}

function BookmarksTab(p: Props) {
    const [q, setQ] = useState("");
    return (
        <div className="setting-split">
            <div className="setting-list">
                <Space.Compact size="small" style={{width: "100%"}}>
                    <Input.Search placeholder="Search" allowClear value={q} onChange={(e) => setQ(e.target.value)}/>
                    <Button icon={<PlusOutlined/>} onClick={() => p.onEditing(null)}/>
                </Space.Compact>
                <div className="item-list">
                    {p.bookmarks.filter((b) => bookmarkLabel(b).toLowerCase().includes(q.toLowerCase())).map((b) => (
                        <div key={b.id} className={`item-list-unit${p.editing?.id === b.id ? " active" : ""}`} onClick={() => p.onEditing(b)}>
                            <span className="item-type">{kindOf(b)}</span>
                            <span className="item-title">{bookmarkLabel(b)}</span>
                        </div>
                    ))}
                </div>
            </div>
            <div className="setting-body">
                <h3>{p.editing ? `Edit: ${bookmarkLabel(p.editing)}` : "New bookmark"}</h3>
                <BookmarkForm bookmark={p.editing} onSave={p.onSaveBookmark} onDelete={p.onDeleteBookmark}/>
            </div>
        </div>
    );
}

function QuickTab({quick, onQuick}: Props) {
    const [form] = Form.useForm<QuickCommand>();
    return (
        <div className="setting-body">
            <Form form={form} layout="inline" onFinish={(v) => { onQuick([...quick, {...v, id: uid()}]); form.resetFields(); }}>
                <Form.Item name="name" rules={[{required: true}]}><Input placeholder="Name"/></Form.Item>
                <Form.Item name="cmd" rules={[{required: true}]} style={{flex: 1}}><Input placeholder="Command"/></Form.Item>
                <Button htmlType="submit" type="primary" icon={<PlusOutlined/>}>Add</Button>
            </Form>
            <Table<QuickCommand> size="small" rowKey="id" pagination={false} dataSource={quick} style={{marginTop: 12}}
                columns={[
                    {title: "Name", dataIndex: "name", width: 200},
                    {title: "Command", dataIndex: "cmd", render: (c) => <code>{c}</code>},
                    {width: 48, render: (_, r) => <Button type="text" danger icon={<DeleteOutlined/>} onClick={() => onQuick(quick.filter((x) => x.id !== r.id))}/>},
                ]}/>
        </div>
    );
}

function SettingsTab({settings, onSettings}: Props) {
    return (
        <div className="setting-body">
            <Form layout="horizontal" labelCol={{span: 6}} wrapperCol={{span: 14}} initialValues={settings}
                  onValuesChange={(_, all) => onSettings({...settings, ...all})}>
                <Form.Item name="theme" label="Theme">
                    <Select options={[{value: "dark", label: "Default (dark)"}, {value: "light", label: "Default light"}]}/>
                </Form.Item>
                <Form.Item name="fontSize" label="Font size"><InputNumber min={8} max={40}/></Form.Item>
                <Form.Item name="fontFamily" label="Font family"><Input/></Form.Item>
                <Form.Item name="scrollback" label="Scrollback lines"><InputNumber min={100} step={1000}/></Form.Item>
                <Form.Item name="cursorBlink" label="Cursor blink" valuePropName="checked"><Switch/></Form.Item>
                <Form.Item name="copyOnSelect" label="Copy on select" valuePropName="checked"><Switch/></Form.Item>
            </Form>
        </div>
    );
}

export default function SettingModal(p: Props) {
    return (
        <Modal open={p.open} onCancel={p.onClose} footer={null} width="min(1000px, 92vw)" className="setting-modal" destroyOnHidden>
            <Tabs tabPlacement="start" activeKey={p.tab} onChange={(k) => p.onTab(k as SettingTab)} items={[
                {key: "bookmarks", label: "Bookmarks", children: <BookmarksTab {...p}/>},
                {key: "quick", label: "Quick commands", children: <QuickTab {...p}/>},
                {key: "settings", label: "Settings", children: <SettingsTab {...p}/>},
            ]}/>
        </Modal>
    );
}
