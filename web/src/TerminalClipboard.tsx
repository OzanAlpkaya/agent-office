import { type JSX, type RefObject, useCallback, useEffect, useRef, useState } from "react";
import type { Terminal } from "@xterm/xterm";
import { copyToClipboard, readClipboard } from "./clipboard";
import { keyAction } from "./terminalKeys";

// Copy and paste for an xterm.js terminal (ADR 0040), without depending on
// browser shortcuts: Ctrl+Shift+C / Ctrl+Shift+V, copy when a mouse
// selection ends, and a right-click menu with Copy and Paste. F12 (and the
// other devtools shortcuts) reach the browser. Failures show a short notice.

export interface MenuState {
  x: number;
  y: number;
  selection: string; // captured when the menu opened
}

export interface TerminalClipboard {
  notice: string;
  menu?: MenuState;
  notify: (msg: string) => void;
  closeMenu: () => void;
  copy: (text: string) => void;
  paste: () => void;
}

// useTerminalClipboard wires a terminal created by an effect. Call attach from
// that effect with the terminal and its host element; it returns the cleanup.
export function useTerminalClipboard(pasteInto?: RefObject<((text: string) => void) | undefined>): TerminalClipboard & {
  attach: (term: Terminal, host: HTMLElement) => () => void;
} {
  const [notice, setNotice] = useState("");
  const [menu, setMenu] = useState<MenuState>();
  const closeMenu = useCallback((): void => setMenu(undefined), []);
  const timer = useRef<number>(undefined);
  const notify = (msg: string): void => {
    window.clearTimeout(timer.current);
    setNotice(msg);
    timer.current = window.setTimeout(() => setNotice(""), 6000);
  };
  useEffect(() => () => window.clearTimeout(timer.current), []);

  // copy must be called from a gesture's handler: its first attempt is synchronous.
  const copy = (text: string): void => {
    copyToClipboard(text).catch((e: unknown) => notify(`Copy failed: ${e instanceof Error ? e.message : String(e)}`));
  };
  const paste = (): void => {
    const into = pasteInto?.current;
    if (!into) return;
    readClipboard().then(
      (t) => { if (t) into(t); },
      (e: unknown) => notify(`Paste failed: ${e instanceof Error ? e.message : String(e)}. Ctrl+Shift+V pastes without a permission.`),
    );
  };

  const attach = (term: Terminal, host: HTMLElement): (() => void) => {
    term.attachCustomKeyEventHandler((e) => {
      const action = keyAction(e);
      if (action === "copy") {
        e.preventDefault(); // not devtools
        if (e.type === "keydown") copy(term.getSelection());
        return false;
      }
      // Ctrl+Shift+V: the browser's paste event reaches xterm (no ^V to the agent);
      // F12 and the devtools shortcuts: the browser's own.
      return action === "pass";
    });
    // Copy when a mouse selection ends (a gesture, so the copy event is allowed),
    // wherever the button is released: a drag often ends outside the terminal.
    const onMouseUp = (e: MouseEvent): void => {
      window.removeEventListener("mouseup", onMouseUp, true);
      if (e.button === 0 && term.hasSelection()) copy(term.getSelection());
    };
    const onMouseDown = (e: MouseEvent): void => {
      if (e.button === 0) window.addEventListener("mouseup", onMouseUp, true);
    };
    const onContextMenu = (e: MouseEvent): void => {
      e.preventDefault();
      const box = (host.closest(".terminal-wrap") ?? host).getBoundingClientRect();
      setMenu({ x: e.clientX - box.left, y: e.clientY - box.top, selection: term.getSelection() });
    };
    host.addEventListener("mousedown", onMouseDown, true);
    host.addEventListener("contextmenu", onContextMenu, true);
    return () => {
      host.removeEventListener("mousedown", onMouseDown, true);
      window.removeEventListener("mouseup", onMouseUp, true);
      host.removeEventListener("contextmenu", onContextMenu, true);
    };
  };

  return { notice, menu, notify, closeMenu, copy, paste, attach };
}

// TerminalOverlay is the right-click menu and the notice, inside .terminal-wrap.
export function TerminalOverlay({ clip, canPaste, onDone }: {
  clip: TerminalClipboard;
  canPaste: boolean;
  onDone: () => void; // e.g. focus the terminal again
}): JSX.Element {
  const ref = useRef<HTMLDivElement>(null);
  const done = useRef(onDone);
  done.current = onDone;
  const { menu, closeMenu } = clip;
  useEffect(() => {
    if (!menu) return;
    ref.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    const away = (e: Event): void => {
      if (!ref.current?.contains(e.target as Node)) closeMenu();
    };
    const key = (e: KeyboardEvent): void => { if (e.key === "Escape") { closeMenu(); done.current(); } };
    document.addEventListener("pointerdown", away, true);
    document.addEventListener("keydown", key, true);
    window.addEventListener("blur", closeMenu);
    return () => {
      document.removeEventListener("pointerdown", away, true);
      document.removeEventListener("keydown", key, true);
      window.removeEventListener("blur", closeMenu);
    };
  }, [menu, closeMenu]);
  return (
    <>
      {menu && (
        <div ref={ref} className="term-menu" role="menu" aria-label="Terminal" style={{ left: menu.x, top: menu.y }}>
          <button role="menuitem" disabled={!menu.selection}
            onClick={() => { clip.copy(menu.selection); closeMenu(); onDone(); }}>
            Copy
          </button>
          {canPaste && (
            <button role="menuitem" onClick={() => { clip.paste(); closeMenu(); onDone(); }}>Paste</button>
          )}
        </div>
      )}
      {clip.notice && <div className="copy-notice" role="status">{clip.notice}</div>}
    </>
  );
}
