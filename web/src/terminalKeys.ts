// Terminal keys (ADR 0040): copy and paste like a desktop terminal, and the
// phone key bar. Pure functions, unit-tested.

// browser: the key is the browser's (devtools), neither the agent's nor ours.
export type KeyAction = "copy" | "paste" | "browser" | "pass";

export interface KeyLike {
  key: string;
  code?: string;
  ctrlKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
  metaKey: boolean;
}

// keyAction decides what a key event in the terminal means. Ctrl+Shift+C
// copies (and must not reach the browser, which would open devtools);
// Ctrl+Shift+V pastes through the browser's paste event; F12 and
// Ctrl+Shift+I / J open devtools as on any page (Ozan's manual test: F12
// reached the agent); everything else, Ctrl+C included, goes to the agent.
export function keyAction(e: KeyLike): KeyAction {
  if (e.key === "F12" && !e.ctrlKey && !e.altKey && !e.metaKey) return "browser";
  if (!e.ctrlKey || !e.shiftKey || e.altKey || e.metaKey) return "pass";
  // The physical key decides when the browser says which (any keyboard layout), else the character.
  const letter = e.code?.startsWith("Key") ? e.code.slice(3).toLowerCase() : e.key.toLowerCase();
  switch (letter) {
    case "c": return "copy";
    case "v": return "paste";
    case "i": case "j": return "browser";
    default: return "pass";
  }
}

export interface BarKey {
  label: string;
  title: string;
  data: (appCursor: boolean) => string;
}

const arrow = (letter: string) => (appCursor: boolean): string => (appCursor ? `\x1bO${letter}` : `\x1b[${letter}`);

// barKeys are what a phone keyboard lacks and Claude Code needs. Esc is a
// lone \x1b, which the daemon reads as an interrupt (ADR 0008); arrows follow
// the terminal's cursor key mode.
export const barKeys: readonly BarKey[] = [
  { label: "Esc", title: "Escape (interrupts a turn)", data: () => "\x1b" },
  { label: "Tab", title: "Tab", data: () => "\t" },
  { label: "⇧Tab", title: "Shift+Tab (Claude Code's mode switch)", data: () => "\x1b[Z" },
  { label: "↑", title: "Up", data: arrow("A") },
  { label: "↓", title: "Down", data: arrow("B") },
  { label: "←", title: "Left", data: arrow("D") },
  { label: "→", title: "Right", data: arrow("C") },
  { label: "Ctrl-C", title: "Ctrl+C", data: () => "\x03" },
  { label: "⏎", title: "Enter", data: () => "\r" },
];
