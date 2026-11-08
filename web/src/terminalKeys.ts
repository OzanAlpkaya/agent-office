// Terminal keys (ADR 0040): copy and paste like a desktop terminal, and the
// phone key bar. Pure functions, unit-tested.

export type KeyAction = "copy" | "paste" | "pass";

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
// Ctrl+Shift+V pastes through the browser's paste event; everything else,
// Ctrl+C included, goes to the agent.
export function keyAction(e: KeyLike): KeyAction {
  if (!e.ctrlKey || !e.shiftKey || e.altKey || e.metaKey) return "pass";
  const k = e.code === "KeyC" || e.key.toLowerCase() === "c" ? "c" : e.code === "KeyV" || e.key.toLowerCase() === "v" ? "v" : "";
  return k === "c" ? "copy" : k === "v" ? "paste" : "pass";
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
