// GrantsPanel lists the project's stored access grants (ADR 0053) with a
// Revoke button each (Phase 17). Revoking narrows the sandbox: running
// sessions lose the host at once, unless another grant still covers it.
import { type JSX, useCallback, useEffect, useState } from "react";
import { type AccessGrant, type AccessRequest, type ProjectApi } from "./api";
import { AgentName } from "./AgentName";

export function GrantsPanel({ api, requests }: {
  api: Pick<ProjectApi, "accessGrants" | "revokeGrant">;
  requests: readonly AccessRequest[]; // reloads when a decision changes them
}): JSX.Element {
  const [grants, setGrants] = useState<AccessGrant[] | undefined>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<number | undefined>();
  const load = useCallback((): void => {
    api.accessGrants().then(setGrants, (e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [api]);
  const decided = requests.filter((r) => r.status === "approved").length;
  useEffect(load, [load, decided]);

  const revoke = (g: AccessGrant): void => {
    setBusy(g.id);
    setError("");
    api.revokeGrant(g.id).then(
      () => { setBusy(undefined); load(); },
      (e: unknown) => { setBusy(undefined); setError(e instanceof Error ? e.message : String(e)); load(); },
    );
  };

  if (grants === undefined) return <p className="small muted">{error || "Loading access grants…"}</p>;
  return (
    <div className="grants" data-testid="grants">
      <p className="strong">Access grants</p>
      {grants.length === 0
        ? <p className="small muted">None: agents reach only the allowlist above.</p>
        : (
          <ul className="grant-list">
            {grants.map((g) => (
              <li key={g.id} className="row small">
                <span className="mono">{g.host}</span>
                <span className="chip">{g.scope === "project"
                  ? "every agent of the project"
                  : <>for <AgentName id={g.agent_id} /></>}</span>
                <span className="muted" title={g.decision_reason}>
                  {g.decided_by === "lead" ? <>approved by the Tech Lead <AgentName id={g.decider_agent_id} /></> : "approved by you"}
                  {" · "}{new Date(g.created_at).toLocaleString()}
                </span>
                <button className="danger" disabled={busy === g.id} onClick={() => revoke(g)}
                  title="New connections to this host are refused from now on, in running and later sessions">
                  Revoke
                </button>
              </li>
            ))}
          </ul>
        )}
      {error && <p className="hint" role="alert">{error}</p>}
    </div>
  );
}
