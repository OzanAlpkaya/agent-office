// Drawer is a panel over the page's right edge (a bottom sheet on a phone).
// It lies over the content, so opening it or changing what it holds never
// moves the page (ADR 0044). Escape or the backdrop closes it.
import { type JSX, type ReactNode, useLayoutEffect, useRef } from "react";

export function Drawer({ title, label, onClose, children }: {
  title: ReactNode;
  label: string; // the dialog's accessible name
  onClose: () => void;
  children: ReactNode;
}): JSX.Element {
  const panel = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useLayoutEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    panel.current?.focus();
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") close.current();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      before?.focus({ preventScroll: true });
    };
  }, []);
  return (
    <div className="drawer-layer">
      <div className="drawer-backdrop" onClick={onClose} />
      <div className="drawer" role="dialog" aria-modal="true" aria-label={label} tabIndex={-1} ref={panel}>
        <div className="drawer-head">
          <h2>{title}</h2>
          <button className="link" onClick={onClose} aria-label="Close">✕</button>
        </div>
        <div className="drawer-body">{children}</div>
      </div>
    </div>
  );
}
