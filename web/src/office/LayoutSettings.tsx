// Placeholder until the layout editor lands (Phase 18, sub-phase 5).
import type { JSX } from "react";
import type { Project } from "../api";

export default function LayoutSettings({ project }: { project: Project }): JSX.Element {
  return <p className="muted small">{project.office?.source === "custom" ? "Custom layout" : "Default layout"}</p>;
}
