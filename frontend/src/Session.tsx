import {useState} from "react";
import {Segmented} from "antd";
import {CodeOutlined, FolderOutlined, SplitCellsOutlined} from "@ant-design/icons";
import {TermService} from "../bindings/wailsterm";
import {type Settings, type Tab, termThemes} from "./store";
import Term from "./Term";
import Sftp from "./Sftp";

type Pane = "terminal" | "sftp" | "split";

function opener(tab: Tab): (cols: number, rows: number) => Promise<string> {
    const b = tab.bookmark!;
    switch (tab.kind) {
        case "ssh": return (c, r) => TermService.OpenSSH(b, c, r);
        case "telnet": return () => TermService.OpenTelnet(b);
        case "serial": return () => TermService.OpenSerial(b);
        default: return (c, r) => TermService.OpenLocal(c, r);
    }
}

type Props = {tab: Tab; active: boolean; settings: Settings; onSession: (id: string) => void};

export default function Session({tab, active, settings, onSession}: Props) {
    const [pane, setPane] = useState<Pane>("terminal");
    const [sftpOpened, setSftpOpened] = useState(false);
    const ssh = tab.kind === "ssh";
    const showSftp = ssh && pane !== "terminal" && !!tab.sessionId;

    return (
        <div className={`session pane-${pane}`} style={{display: active ? "flex" : "none"}}>
            {ssh && (
                <div className="session-control">
                    <Segmented<Pane> size="small" value={pane} disabled={!tab.sessionId}
                        onChange={(v) => { setPane(v); setSftpOpened(true); }}
                        options={[
                            {value: "terminal", icon: <CodeOutlined/>, label: "Terminal"},
                            {value: "sftp", icon: <FolderOutlined/>, label: "SFTP"},
                            {value: "split", icon: <SplitCellsOutlined/>, label: "Split"},
                        ]}/>
                </div>
            )}
            <div className="session-body">
                <div className="term-wrap" style={{display: pane === "sftp" ? "none" : "flex", background: termThemes[settings.theme].background}}>
                    <Term open={opener(tab)} settings={settings} active={active && pane !== "sftp"} onSession={onSession}/>
                </div>
                {sftpOpened && tab.sessionId && (
                    <div className="sftp-wrap" style={{display: showSftp ? "flex" : "none"}}>
                        <Sftp sessionId={tab.sessionId}/>
                    </div>
                )}
            </div>
        </div>
    );
}
