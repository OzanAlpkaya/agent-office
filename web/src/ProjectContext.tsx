// The current project's API client (ADR 0046, 0048): the App provides it, so
// every component calls the routes of the project it shows.
import { createContext, useContext } from "react";
import type { ProjectApi } from "./api";

export const ProjectApiContext = createContext<ProjectApi | undefined>(undefined);

export function useApi(): ProjectApi {
  const a = useContext(ProjectApiContext);
  if (!a) throw new Error("useApi: no project (ProjectApiContext is missing)");
  return a;
}
