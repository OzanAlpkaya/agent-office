// Drawer is a panel over the page's right edge (a bottom sheet on a phone).
// It lies over the content, so opening it or changing what it holds never
// moves the page (ADR 0044). Escape or the backdrop closes it.
import { type JSX, type ReactNode, useLayoutEffect, useRef } from "react";
import { Icon } from "./icons";

export function Drawer({ title, label, onClose, children, actions, className = "" }: {
  title: ReactNode;
  label: string; // the dialog's accessible name
  onClose: () => void;
  children: ReactNode;
  actions?: ReactNode; // buttons in the head, before the close button
  className?: string;
}): JSX.Element {
  const panel = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useLayoutEffect(() => {
    const active = document.activeElement;
    // what opened the dialog gets focus back (not a field inside it that focused itself)
    const before = active instanceof HTMLElement && !panel.current?.contains(active) ? active : undefined;
    if (!panel.current?.contains(document.activeElement)) panel.current?.focus();
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
      <div className={`drawer ${className}`} role="dialog" aria-modal="true" aria-label={label} tabIndex={-1} ref={panel}>
        <div className="drawer-head">
          <h2>{title}</h2>
          <span className="row">
            {actions}
            <button className="ghost icon-button" onClick={onClose} aria-label="Close"><Icon name="close" /></button>
          </span>
        </div>
        <div className="drawer-body">{children}</div>
      </div>
    </div>
  );
}
