// Modal is a dialog over the page (a full-screen sheet on a phone), Phase 18.
// Like the Drawer (ADR 0044) it lies over the content, so opening it never
// moves the page; Escape or the backdrop closes it and focus returns to what
// opened it.
import { type JSX, type ReactNode, useLayoutEffect, useRef } from "react";
import { Icon } from "./icons";

export function Modal({ title, label, onClose, children, footer, className = "" }: {
  title: ReactNode;
  label: string; // the dialog's accessible name
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
}): JSX.Element {
  const panel = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useLayoutEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    panel.current?.focus({ preventScroll: true });
    const onKey = (e: KeyboardEvent): void => {
      // A dialog opened inside this one (none today) or a menu would handle its own Escape first.
      if (e.key === "Escape" && !e.defaultPrevented) close.current();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      before?.focus({ preventScroll: true });
    };
  }, []);
  return (
    <div className="modal-layer">
      <div className="drawer-backdrop" onClick={onClose} />
      <div className={`modal ${className}`} role="dialog" aria-modal="true" aria-label={label} tabIndex={-1} ref={panel}>
        <div className="modal-head">
          <h2>{title}</h2>
          <button className="ghost icon-button" onClick={onClose} aria-label="Close"><Icon name="close" /></button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>
  );
}
