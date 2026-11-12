// The avatar creator in Settings → Profile (Phase 18 review, ADR 0058): a
// name for the user's nameplate and a look, drawn with the office's own
// pixel maps. Randomize picks a new look on each click; Save keeps it
// daemon-wide, for every project's floor.
import { type JSX, useEffect, useRef, useState } from "react";
import { api } from "../api";
import * as art from "./art";
import { armRects } from "./arms";
import {
  type AvatarLook, type Profile, avatarPalette, defaultLook, defaultName, displayName, lookOfProfile, parts, randomLook,
} from "./avatar";
import { drawMap } from "./pixels";

// The preview's world size, in pixels of the office's art.
const W = 24;
const H = 26;
const zoom = 8;

// AvatarPreview draws a standing figure in a look, as in the office.
export function AvatarPreview({ look, name }: { look: AvatarLook; name: string }): JSX.Element {
  const canvas = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const ctx = canvas.current?.getContext("2d");
    if (!ctx) return;
    ctx.clearRect(0, 0, W, H);
    const p = avatarPalette(look);
    const x = 4, y = 3;
    drawMap(ctx, art.legs[0], p, x, y + art.body.h);
    drawMap(ctx, art.body, p, x, y);
    drawMap(ctx, art.hair[art.hairStyles[look.style]], p, x, y);
    for (const r of armRects("walk0", p)) {
      ctx.fillStyle = `#${r.color.toString(16).padStart(6, "0")}`;
      ctx.fillRect(x + r.x, y + r.y, r.w, r.h);
    }
  }, [look]);
  return (
    <figure className="avatar-preview" data-testid="avatar-preview"
      data-look={`${look.skin}-${look.hair}-${look.style}-${look.shirt}-${look.pants}`}>
      <canvas ref={canvas} width={W} height={H} style={{ width: W * zoom, height: H * zoom }} role="img"
        aria-label={`Your character: ${parts.map((pt) => `${pt.label.toLowerCase()} ${look[pt.key] + 1} of ${pt.count}`).join(", ")}`} />
      <figcaption><span className="user-plate static"><span className="dot" />{name}</span></figcaption>
    </figure>
  );
}

const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

export default function AvatarCreator({ profile }: { profile?: Profile }): JSX.Element {
  const [name, setName] = useState(profile?.name ?? "");
  const [look, setLook] = useState<AvatarLook>(() => lookOfProfile(profile));
  const [state, setState] = useState<"idle" | "saving" | "saved">("idle");
  const [error, setError] = useState("");
  const saved = lookOfProfile(profile);
  const dirty = name.trim() !== (profile?.name ?? "") || parts.some((p) => look[p.key] !== saved[p.key]) || !profile?.set;
  const save = (): void => {
    setState("saving");
    setError("");
    api.setProfile({ name: name.trim(), look }).then(() => setState("saved"), (e: unknown) => { setError(errText(e)); setState("idle"); });
  };
  return (
    <div className="avatar-creator" data-testid="avatar-creator">
      <AvatarPreview look={look} name={displayName({ name, look, set: true })} />
      <div className="stack avatar-controls">
        <label className="setting">
          <span>Name on your nameplate</span>
          <input value={name} maxLength={24} placeholder={defaultName} aria-label="Name on your nameplate"
            onChange={(e) => { setName(e.target.value); setState("idle"); }} />
          <span className="field-hint">Empty: “{defaultName}”.</span>
        </label>
        <dl className="avatar-parts small">
          {parts.map((p) => (
            <div key={p.key}><dt className="muted">{p.label}</dt><dd>{look[p.key] + 1} of {p.count}</dd></div>
          ))}
        </dl>
        <div className="row">
          <button onClick={() => { setLook((l) => randomLook(l)); setState("idle"); }}>Randomize</button>
          <button className="ghost" onClick={() => { setLook(defaultLook); setState("idle"); }}>Default look</button>
        </div>
        <div className="row">
          <button className="primary" disabled={!dirty || state === "saving"} onClick={save}>{state === "saving" ? "Saving…" : "Save"}</button>
          {state === "saved" && !dirty && <span className="small muted" role="status">Saved: you look like this on every floor.</span>}
          {error && <span className="error small" role="alert">{error}</span>}
        </div>
      </div>
    </div>
  );
}
