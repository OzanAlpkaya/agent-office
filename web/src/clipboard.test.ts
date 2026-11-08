import { describe, expect, it } from "vitest";
import { maxOsc52, parseOsc52, reason } from "./clipboard";

const b64 = (s: string): string => btoa(String.fromCharCode(...new TextEncoder().encode(s)));

describe("parseOsc52", () => {
  it("decodes a clipboard write, UTF-8 included", () => {
    expect(parseOsc52(`c;${b64("● What can I help you with?")}`)).toEqual({ kind: "write", text: "● What can I help you with?" });
    expect(parseOsc52(`;${b64("x")}`)).toEqual({ kind: "write", text: "x" });
    expect(parseOsc52(`pc;${b64("y")}`)).toEqual({ kind: "write", text: "y" });
  });
  it("recognises a read request, which is never answered", () => {
    expect(parseOsc52("c;?")).toEqual({ kind: "read" });
  });
  it("refuses everything else", () => {
    expect(parseOsc52("c;").kind).toBe("invalid");
    expect(parseOsc52("c").kind).toBe("invalid");
    expect(parseOsc52("x;aGk=").kind).toBe("invalid");
    expect(parseOsc52("c;not base64!").kind).toBe("invalid");
    expect(parseOsc52("c;" + "A".repeat(maxOsc52 + 4))).toEqual({ kind: "invalid", reason: "too large" });
  });
});

describe("reason", () => {
  it("drops the browser's prefix", () => {
    expect(reason(new Error("Failed to execute 'writeText' on 'Clipboard': Write permission denied."))).toBe("Write permission denied.");
    expect(reason("plain")).toBe("plain");
  });
});
