import { type JSX, useEffect, useState } from "react";
import { type Device, type PairingCode, api } from "./api";

// DevicesPanel lists the paired devices and pairs new ones (ADR 0039).
export function DevicesPanel({ remoteHosts, onSignedOut, run }: {
  remoteHosts: string[];
  onSignedOut: () => void;
  run: <T>(fn: () => Promise<T>) => Promise<T | undefined>;
}): JSX.Element {
  const [devices, setDevices] = useState<Device[]>([]);
  const [code, setCode] = useState<PairingCode>();
  const [nowMs, setNowMs] = useState(() => Date.now());
  const reload = (): Promise<unknown> => run(async () => { setDevices(await api.devices()); return true; });
  useEffect(() => {
    void reload();
    const t = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  const left = code ? Math.max(0, Math.round((Date.parse(code.expires_at) - nowMs) / 1000)) : 0;
  const urls = code?.remote_urls.length ? code.remote_urls : remoteHosts.map((h) => `https://${h}/`);

  return (
    <section className="side-panel devices" aria-label="Devices">
      <h2>Devices</h2>
      <p className="muted small">
        Browsers and scripts that may use this agent-office. Every one of them acts as you; sign out any you do not
        recognise.
      </p>
      <div className="row">
        <button onClick={() => void run(async () => { setCode(await api.newPairingCode()); return true; })}>
          Pair a device
        </button>
      </div>
      {code && left > 0 && (
        <div className="pair-code" data-testid="pairing-code">
          <div>Code <span className="mono code">{code.code}</span> <span className="muted small">· {left} s left, works once</span></div>
          <div className="small">
            On the other device open{" "}
            {urls.length > 0
              ? urls.map((u) => <span key={u} className="mono">{u} </span>)
              : <span>agent-office (for a phone: set up <span className="mono">tailscale serve</span>, docs/remote.md)</span>}
            {" "}and enter the code.
          </div>
        </div>
      )}
      {code && left === 0 && <p className="muted small">The code expired. Create a new one.</p>}
      <ul>
        {devices.map((d) => (
          <li key={d.id} className="device">
            <div className="row">
              <span className="strong">{d.name}</span>
              {d.current && <span className="chip">this device</span>}
              {d.kind === "token" && <span className="chip">script token</span>}
            </div>
            <div className="small muted">
              Paired {new Date(d.created_at).toLocaleString()} {via(d.paired_via)} · last used{" "}
              {new Date(d.last_seen_at).toLocaleString()}
            </div>
            <div className="row">
              <button className="danger" onClick={() => void run(async () => {
                await api.revokeDevice(d.id);
                if (d.current) onSignedOut();
                else await reload();
                return true;
              })}>
                {d.current ? "Sign out" : "Sign out device"}
              </button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

function via(v: string): string {
  if (v === "local") return "on this machine";
  if (v.startsWith("tailscale:")) return `over Tailscale (${v.slice("tailscale:".length)})`;
  return "";
}
