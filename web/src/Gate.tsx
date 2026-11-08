import { type JSX, useCallback, useEffect, useState } from "react";
import { type AuthSession, api, unauthorizedEvent } from "./api";
import { App } from "./App";
import { codeFromHash } from "./auth";
import { PairScreen } from "./Pair";

type GateState = { kind: "checking" } | { kind: "paired"; session: AuthSession } | { kind: "unpaired"; remote: boolean } |
  { kind: "down"; error: string };

// Gate shows the app to a paired browser and the pairing screen otherwise
// (ADR 0039). Any 401 or signed-out WebSocket sends it back to checking.
export function Gate(): JSX.Element {
  // A pairing code in the URL (#pair=…) is read once and removed from the address bar and the
  // history, on a paired browser too.
  const [code] = useState(() => {
    const c = codeFromHash(location.hash);
    if (c) history.replaceState(null, "", location.pathname + location.search);
    return c;
  });
  const [state, setState] = useState<GateState>({ kind: "checking" });
  const check = useCallback((): void => {
    api.authSession().then(
      (s) => setState(s.authenticated ? { kind: "paired", session: s } : { kind: "unpaired", remote: s.remote }),
      (e: unknown) => setState({ kind: "down", error: e instanceof Error ? e.message : String(e) }),
    );
  }, []);
  useEffect(() => {
    check();
    const onUnauthorized = (): void => check();
    window.addEventListener(unauthorizedEvent, onUnauthorized);
    return () => window.removeEventListener(unauthorizedEvent, onUnauthorized);
  }, [check]);

  switch (state.kind) {
    case "checking":
      return <div className="empty">Connecting to agent-office…</div>;
    case "down":
      return (
        <div className="pair">
          <div className="pair-card">
            <h1>agent-office</h1>
            <p className="error">The daemon is not reachable: {state.error}</p>
            <button onClick={check}>Try again</button>
          </div>
        </div>
      );
    case "unpaired":
      return <PairScreen remote={state.remote} initialCode={code} onPaired={(s) => setState({ kind: "paired", session: s })} />;
    case "paired":
      return <App session={state.session} onSignedOut={() => setState({ kind: "unpaired", remote: state.session.remote })} />;
  }
}
