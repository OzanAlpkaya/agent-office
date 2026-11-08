import { type FormEvent, type JSX, useState } from "react";
import { ApiError, type AuthSession, api } from "./api";
import { codeFromHash, deviceName, formatCode, normalizeCode } from "./auth";

// PairScreen pairs this browser with the daemon (ADR 0039). The code comes
// from `agent-office pair` on the daemon's machine, or from the Devices panel
// of a browser that is already paired.
export function PairScreen({ remote, onPaired }: { remote: boolean; onPaired: (s: AuthSession) => void }): JSX.Element {
  const [code, setCode] = useState(() => {
    const c = codeFromHash(location.hash);
    // The code must not stay in the address bar or the history.
    if (c) history.replaceState(null, "", location.pathname + location.search);
    return c;
  });
  const [name, setName] = useState(() => deviceName(navigator.userAgent));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const valid = normalizeCode(code) !== "" && name.trim() !== "";

  const submit = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onPaired(await api.pair(code, name.trim()));
    } catch (err) {
      setError(err instanceof ApiError || err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="pair">
      <form className="pair-card" onSubmit={(e) => void submit(e)}>
        <h1>agent-office</h1>
        <p>Pair this {remote ? "device" : "browser"} once to use agent-office here.</p>
        <label>
          Pairing code
          <input value={code} onChange={(e) => setCode(formatCode(e.target.value))} placeholder="ABCD-EFGH"
            autoComplete="one-time-code" autoCapitalize="characters" spellCheck={false} inputMode="text"
            aria-label="Pairing code" autoFocus={!code} />
        </label>
        <label>
          Name of this device
          <input value={name} maxLength={60} onChange={(e) => setName(e.target.value)} aria-label="Device name" />
        </label>
        {error && <p className="error" role="alert">{error}</p>}
        <button className="primary" type="submit" disabled={!valid || busy}>{busy ? "Pairing…" : "Pair"}</button>
        <div className="muted small pair-help">
          <p>Get a code (valid 5 minutes, works once):</p>
          <ul>
            <li>on the machine running agent-office: <span className="mono">agent-office pair</span></li>
            <li>or in a browser that is already paired: <span className="strong">Devices → Pair a device</span></li>
          </ul>
        </div>
      </form>
    </main>
  );
}
