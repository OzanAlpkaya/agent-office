import { describe, expect, it } from "vitest";
import { parseScope, scopeLabel } from "./memory";

describe("memory scopes", () => {
  it("parses comma- or line-separated lists", () => {
    expect(parseScope(" worker, qa ,\nworker\n\n")).toEqual(["worker", "qa"]);
    expect(parseScope("")).toEqual([]);
  });
  it("labels whom an entry reaches", () => {
    expect(scopeLabel({ roles: [], paths: [] })).toBe("every agent");
    expect(scopeLabel({ roles: ["worker"], paths: ["web/", "*.go"] })).toBe("roles worker · files web/, *.go");
  });
});
