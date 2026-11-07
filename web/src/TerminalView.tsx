import { type JSX, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "./api";

interface Props {
  agentId: string;
}

type Status = { kind: "connecting" } | { kind: "live" } | { kind: "closed"; reason: string; exited: boolean };

// TerminalView attaches to an agent's PTY. The daemon first replays the
// scrollback, then streams live output; input and resize go back as JSON.
export function TerminalView({ agentId }: Props): JSX.Element {
  const host = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<Status>({ kind: "connecting" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!host.current) return;
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, 'JetBrains Mono', Menlo, monospace",
      fontSize: 13,
      theme: { background: "#0f1115" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host.current);
    fit.fit();

    const ws = new WebSocket(wsURL(`/api/agents/${agentId}/terminal`));
    ws.binaryType = "arraybuffer";
    const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m));
    let last = { cols: 0, rows: 0 };
    const sendSize = () => {
      if (term.cols === last.cols && term.rows === last.rows) return;
      last = { cols: term.cols, rows: term.rows };
      send({ type: "resize", cols: term.cols, rows: term.rows });
    };

    ws.onopen = () => {
      setStatus({ kind: "live" });
      sendSize();
      term.focus();
    };
    ws.onmessage = (e) => term.write(new Uint8Array(e.data as ArrayBuffer));
    ws.onclose = (e) => {
      const exited = e.reason === "process exited";
      const reason = e.reason || `connection closed (${e.code})`;
      term.write(`\r\n\x1b[2m[${reason}]\x1b[0m\r\n`);
      setStatus({ kind: "closed", reason, exited });
    };
    const input = term.onData((data) => send({ type: "input", data }));

    const observer = new ResizeObserver(() => {
      fit.fit();
      sendSize();
    });
    observer.observe(host.current);

    return () => {
      observer.disconnect();
      input.dispose();
      ws.onclose = null;
      ws.close();
      term.dispose();
    };
  }, [agentId, attempt]);

  return (
    <div className="terminal-wrap">
      <div className="terminal" ref={host} />
      {status.kind === "closed" && (
        <div className="terminal-banner">
          {status.exited ? (
            <span>
              The agent process has exited.{" "}
              <span className="muted" data-testid="resume-note">
                The “Resume this session with: claude --resume …” line above cannot be used with agent-office yet:
                the session belongs to this agent’s own config{" "}(and sandbox). Resuming from agent-office comes in Phase 9.
              </span>
            </span>
          ) : `Disconnected: ${status.reason}.`}
          {!status.exited && <button onClick={() => setAttempt((n) => n + 1)}>Reconnect</button>}
        </div>
      )}
    </div>
  );
}
