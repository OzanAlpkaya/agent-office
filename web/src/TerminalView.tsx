import { type JSX, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { type Agent, type SessionInfo, api, signalUnauthorized, statusSignedOut, wsURL } from "./api";
import { barKeys, keyAction } from "./terminalKeys";

interface Props {
  agent: Agent;
  onResume: () => void;
}

type Status = { kind: "connecting" } | { kind: "live" } | { kind: "closed"; reason: string; exited: boolean };

const termOptions = {
  fontFamily: "ui-monospace, 'JetBrains Mono', Menlo, monospace",
  fontSize: 13,
  theme: { background: "#0f1115" },
};

// copyText writes to the clipboard; it needs a secure context (loopback or
// https through tailscale serve). A refusal (no focus, no permission) is ignored.
function copyText(text: string): void {
  if (!text || !navigator.clipboard) return;
  navigator.clipboard.writeText(text).catch(() => undefined);
}

// TerminalView shows an agent's terminal: the live one while its process
// runs, else a saved session's output, read-only (ADR 0036). A picker lists
// every session of the agent.
export function TerminalView({ agent, onResume }: Props): JSX.Element {
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [choice, setChoice] = useState(""); // "": the live session, else the newest saved one
  const exited = agent.state === "exited";
  useEffect(() => {
    let alive = true;
    // Fire and forget: the effect's cleanup drops a late answer.
    void api.agentSessions(agent.id).then((ss) => { if (alive) setSessions(ss); }).catch(() => { if (alive) setSessions([]); });
    return () => { alive = false; };
  }, [agent.id, agent.session_id, exited]);

  const saved = sessions.filter((s) => s.terminal && !s.live);
  const live = agent.has_terminal && (choice === "" || choice === agent.session_id);
  const shown = live ? agent.session_id : (choice || saved[0]?.id);
  const pickable = sessions.filter((s) => s.terminal);
  return (
    <div className="terminal-col">
      {(pickable.length > 1 || !agent.has_terminal) && pickable.length > 0 && (
        <div className="session-picker row small">
          <label>
            Session{" "}
            <select value={shown ?? ""} onChange={(e) => setChoice(e.target.value)} aria-label="Session">
              {pickable.map((s) => (
                <option key={s.id} value={s.id}>{sessionLabel(s)}</option>
              ))}
            </select>
          </label>
          {!live && <span className="muted">Saved output, read-only.</span>}
        </div>
      )}
      {live && <LiveTerminal key={agent.session_id} agent={agent} onResume={onResume} />}
      {!live && shown && <SavedTerminal key={shown} agentId={agent.id} sid={shown} agent={agent} onResume={onResume} />}
      {!live && !shown && (
        <div className="terminal-wrap">
          <div className="terminal" />
          <div className="terminal-banner" data-testid="no-history">
            <span>
              No terminal output was saved for this agent{agent.has_terminal ? "" : " (it ran before the daemon restarted)"}.
            </span>
            {agent.resumable && <button onClick={onResume}>Resume</button>}
          </div>
        </div>
      )}
    </div>
  );
}

function sessionLabel(s: SessionInfo): string {
  const t = new Date(s.started_at).toLocaleString();
  const parts = [s.live ? `${t} (live)` : t];
  if (s.resumed_from) parts.push("resumed");
  parts.push(`${s.turns} turn${s.turns === 1 ? "" : "s"}`);
  return parts.join(" · ");
}

// LiveTerminal attaches to an agent's PTY. The daemon first replays the
// scrollback, then streams live output; input and resize go back as JSON.
function LiveTerminal({ agent, onResume }: Props): JSX.Element {
  const agentId = agent.id;
  const host = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<Status>({ kind: "connecting" });
  const [attempt, setAttempt] = useState(0);
  // The key bar's way into the live terminal (ADR 0040).
  const io = useRef<{ term: Terminal; input: (data: string) => void }>(undefined);

  useEffect(() => {
    if (!host.current) return;
    const term = new Terminal({ ...termOptions, cursorBlink: true });
    // Copy and paste like a desktop terminal (ADR 0040): Ctrl+Shift+C copies the
    // selection, Ctrl+Shift+V lets the browser's paste event reach xterm, and a
    // selection is copied as it is made. Ctrl+C still goes to the agent.
    term.attachCustomKeyEventHandler((e) => {
      const action = keyAction(e);
      if (action === "copy") {
        e.preventDefault(); // not devtools
        if (e.type === "keydown") copyText(term.getSelection());
        return false;
      }
      return action !== "paste";
    });
    const selection = term.onSelectionChange(() => { if (term.hasSelection()) copyText(term.getSelection()); });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host.current);
    fit.fit();

    const ws = new WebSocket(wsURL(`/api/agents/${agentId}/terminal`));
    ws.binaryType = "arraybuffer";
    const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m));
    let last = { cols: 0, rows: 0 };
    const sendSize = (force = false) => {
      if (!force && term.cols === last.cols && term.rows === last.rows) return;
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
      if (e.code === statusSignedOut) signalUnauthorized();
      const exited = e.reason === "process exited";
      const reason = e.reason || `connection closed (${e.code})`;
      term.write(`\r\n\x1b[2m[${reason}]\x1b[0m\r\n`);
      setStatus({ kind: "closed", reason, exited });
    };
    const input = term.onData((data) => send({ type: "input", data }));
    io.current = { term, input: (data) => send({ type: "input", data }) };
    // The terminal in use sets the PTY's size: a phone and a desktop watching
    // the same agent take turns instead of fighting (ADR 0040).
    const onFocus = (): void => { fit.fit(); sendSize(true); };
    term.textarea?.addEventListener("focus", onFocus);

    const observer = new ResizeObserver(() => {
      fit.fit();
      sendSize();
    });
    observer.observe(host.current);

    return () => {
      observer.disconnect();
      input.dispose();
      selection.dispose();
      term.textarea?.removeEventListener("focus", onFocus);
      io.current = undefined;
      ws.onclose = null;
      ws.close();
      term.dispose();
    };
  }, [agentId, attempt]);

  const sendKey = (data: string): void => {
    io.current?.input(data);
    io.current?.term.focus();
  };
  const paste = (): void => {
    const cur = io.current;
    if (!cur || !navigator.clipboard) return;
    navigator.clipboard.readText().then((t) => { if (t) cur.term.paste(t); cur.term.focus(); }, () => undefined);
  };
  return (
    <div className="terminal-live">
    <div className="terminal-wrap">
      <div className="terminal" ref={host} />
      {status.kind === "closed" && (
        <div className="terminal-banner">
          {status.exited ? (
            <span>
              The agent process has exited.{" "}
              <ResumeNote agent={agent} />{" "}
              {agent.resumable && <button onClick={onResume}>Resume</button>}
            </span>
          ) : `Disconnected: ${status.reason}.`}
          {!status.exited && <button onClick={() => setAttempt((n) => n + 1)}>Reconnect</button>}
        </div>
      )}
    </div>
    {status.kind === "live" && (
      <div className="key-bar" role="toolbar" aria-label="Terminal keys">
        {barKeys.map((k) => (
          // pointerdown keeps the focus (and the phone keyboard) in the terminal.
          <button key={k.label} title={k.title} onPointerDown={(e) => e.preventDefault()}
            onClick={() => sendKey(k.data(io.current?.term.modes.applicationCursorKeysMode ?? false))}>
            {k.label}
          </button>
        ))}
        <button title="Paste from the clipboard" onPointerDown={(e) => e.preventDefault()} onClick={paste}>Paste</button>
      </div>
    )}
    </div>
  );
}

function ResumeNote({ agent }: { agent: Agent }): JSX.Element {
  return (
    <span className="muted" data-testid="resume-note">
      Claude Code’s “Resume this session with: claude --resume …” line does not apply here: the
      session belongs to this agent’s workspace and config{agent.sandbox === "podman" ? " in its sandbox" : ""}.
      {agent.resumable ? " Use Resume to continue it in agent-office." : ""}
    </span>
  );
}

// SavedTerminal replays a session's saved output into a read-only terminal.
function SavedTerminal({ agentId, sid, agent, onResume }: { agentId: string; sid: string } & Props): JSX.Element {
  const host = useRef<HTMLDivElement>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!host.current) return;
    const term = new Terminal({ ...termOptions, disableStdin: true, cursorBlink: false });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host.current);
    fit.fit();
    let alive = true;
    // Fire and forget: the effect's cleanup drops a late answer.
    void api.sessionTerminal(agentId, sid)
      .then((data) => { if (alive) term.write(data); })
      .catch((e: unknown) => { if (alive) setError(e instanceof Error ? e.message : String(e)); });
    const observer = new ResizeObserver(() => fit.fit());
    observer.observe(host.current);
    return () => {
      alive = false;
      observer.disconnect();
      term.dispose();
    };
  }, [agentId, sid]);
  return (
    <div className="terminal-wrap" data-testid="saved-terminal">
      <div className="terminal" ref={host} />
      {(error || agent.resumable) && (
        <div className="terminal-banner">
          <span>
            {error ? `Saved output not available: ${error}.` : "This session has ended."}{" "}
            <ResumeNote agent={agent} />
          </span>
          {agent.resumable && <button onClick={onResume}>Resume</button>}
        </div>
      )}
    </div>
  );
}
