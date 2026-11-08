import { describe, expect, it } from "vitest";
import { barKeys, keyAction } from "./terminalKeys";

const k = (key: string, mods: { ctrl?: boolean; shift?: boolean; alt?: boolean; meta?: boolean } = {}, code?: string) => ({
  key, code, ctrlKey: !!mods.ctrl, shiftKey: !!mods.shift, altKey: !!mods.alt, metaKey: !!mods.meta,
});

describe("keyAction", () => {
  it("copies and pastes with Ctrl+Shift", () => {
    expect(keyAction(k("C", { ctrl: true, shift: true }, "KeyC"))).toBe("copy");
    expect(keyAction(k("V", { ctrl: true, shift: true }, "KeyV"))).toBe("paste");
    // A layout where the key is not C still copies on the physical C key.
    expect(keyAction(k("J", { ctrl: true, shift: true }, "KeyC"))).toBe("copy");
  });
  it("leaves Ctrl+C, Ctrl+V and everything else to the agent", () => {
    expect(keyAction(k("c", { ctrl: true }, "KeyC"))).toBe("pass");
    expect(keyAction(k("v", { ctrl: true }, "KeyV"))).toBe("pass");
    expect(keyAction(k("C", { ctrl: true, shift: true, alt: true }, "KeyC"))).toBe("pass");
    expect(keyAction(k("Escape"))).toBe("pass");
    expect(keyAction(k("A", { ctrl: true, shift: true }, "KeyA"))).toBe("pass");
  });
});

describe("barKeys", () => {
  it("sends what Claude Code expects", () => {
    const by = Object.fromEntries(barKeys.map((b) => [b.label, b]));
    expect(by.Esc.data(false)).toBe("\x1b");
    expect(by["Ctrl-C"].data(false)).toBe("\x03");
    expect(by["⇧Tab"].data(false)).toBe("\x1b[Z");
    expect(by["↑"].data(false)).toBe("\x1b[A");
    expect(by["↑"].data(true)).toBe("\x1bOA");
    expect(by["⏎"].data(false)).toBe("\r");
  });
});
