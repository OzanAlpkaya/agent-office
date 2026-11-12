import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Gate } from "./Gate";
import "./theme.css";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Gate />
  </StrictMode>,
);

// The service worker only serves an offline page (ADR 0040); it needs a
// secure context (https through tailscale serve, or loopback).
if ("serviceWorker" in navigator && window.isSecureContext) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch((e: unknown) => console.warn("agent-office: service worker", e));
  });
}
