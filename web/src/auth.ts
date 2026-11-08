// Pairing helpers (ADR 0039). Pure functions, unit-tested; the daemon checks
// everything again.

// The pairing code alphabet: Crockford's base32 (no I, L, O or U).
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

// normalizeCode mirrors auth.NormalizeCode in Go: upper case, no dashes or
// spaces, O as 0, I and L as 1. It returns "" for anything that cannot be a code.
export function normalizeCode(s: string): string {
  let out = "";
  for (const ch of s.toUpperCase()) {
    if (ch === "-" || ch === " ") continue;
    const c = ch === "O" ? "0" : ch === "I" || ch === "L" ? "1" : ch;
    if (!alphabet.includes(c)) return "";
    out += c;
    if (out.length > 8) return "";
  }
  return out.length === 8 ? out : "";
}

// formatCode shows a code as ABCD-EFGH while it is typed.
export function formatCode(s: string): string {
  const raw = s.toUpperCase().replace(/[^0-9A-Z]/g, "").slice(0, 8);
  return raw.length > 4 ? `${raw.slice(0, 4)}-${raw.slice(4)}` : raw;
}

// codeFromHash reads a code from a URL fragment such as "#pair=ABCD-EFGH"
// (agent-office pair prints that link; the fragment never reaches a server).
export function codeFromHash(hash: string): string {
  const m = /(?:^#|&)pair=([0-9A-Za-z-]{8,9})(?:&|$)/.exec(hash);
  return m ? formatCode(m[1]) : "";
}

// deviceName suggests a name for this browser from its user agent, e.g.
// "Chrome on Android"; the user can change it.
export function deviceName(ua: string): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox"
    : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Android/.test(ua) ? "Android" : /iPhone|iPad|iPod/.test(ua) ? "iOS"
    : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} on ${os}` : browser;
}
