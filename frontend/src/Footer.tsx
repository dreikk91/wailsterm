import {useState} from "react";
import {Button, Input, Select, Space, Tooltip} from "antd";
import {SendOutlined, ThunderboltOutlined} from "@ant-design/icons";
import type {QuickCommand} from "./store";

type Props = {
    quick: QuickCommand[];
    onSend: (cmd: string, all: boolean) => void;
    onEditQuick: () => void;
};

// Footer is electerm's batch input: send a command to the current tab or to every tab, plus quick commands.
export default function Footer({quick, onSend, onEditQuick}: Props) {
    const [cmd, setCmd] = useState("");
    const [all, setAll] = useState(false);
    const send = () => {
        if (!cmd) return;
        onSend(cmd, all);
        setCmd("");
    };
    return (
        <div className="footer">
            <Space.Compact size="small" className="batch-input">
                <Select size="small" value={all} onChange={setAll} popupMatchSelectWidth={false}
                        options={[{value: false, label: "Current tab"}, {value: true, label: "All tabs"}]}/>
                <Input placeholder="Send command…" value={cmd} onChange={(e) => setCmd(e.target.value)} onPressEnter={send}/>
                <Button icon={<SendOutlined/>} onClick={send}/>
            </Space.Compact>
            <div className="quick-commands">
                <Tooltip title="Edit quick commands">
                    <Button size="small" type="text" icon={<ThunderboltOutlined/>} onClick={onEditQuick}/>
                </Tooltip>
                {quick.map((q) => (
                    <Tooltip key={q.id} title={q.cmd}>
                        <Button size="small" onClick={() => onSend(q.cmd, all)}>{q.name}</Button>
                    </Tooltip>
                ))}
            </div>
        </div>
    );
}
