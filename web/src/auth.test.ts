import { describe, expect, it } from "vitest";
import { codeFromHash, deviceName, formatCode, normalizeCode } from "./auth";

describe("pairing codes", () => {
  it("normalizes what people type like the daemon does", () => {
    expect(normalizeCode("abcd-efgh")).toBe("ABCDEFGH");
    expect(normalizeCode(" ABCD EFGH ")).toBe("ABCDEFGH");
    expect(normalizeCode("O1IL-0000")).toBe("01110000");
    expect(normalizeCode("ABCU-EFGH")).toBe("");
    expect(normalizeCode("ABCD-EFG")).toBe("");
    expect(normalizeCode("ABCD-EFGHJ")).toBe("");
  });
  it("formats while typing", () => {
    expect(formatCode("abcd")).toBe("ABCD");
    expect(formatCode("abcde")).toBe("ABCD-E");
    expect(formatCode("ab-cd efgh!x")).toBe("ABCD-EFGH");
  });
  it("reads the code from the link agent-office pair prints", () => {
    expect(codeFromHash("#pair=ABCD-EFGH")).toBe("ABCD-EFGH");
    expect(codeFromHash("#x=1&pair=abcdefgh")).toBe("ABCD-EFGH");
    expect(codeFromHash("#pair=<script>")).toBe("");
    expect(codeFromHash("")).toBe("");
  });
});

describe("deviceName", () => {
  it("names common browsers", () => {
    expect(deviceName("Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Mobile Safari/537.36"))
      .toBe("Chrome on Android");
    expect(deviceName("Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1"))
      .toBe("Safari on iOS");
    expect(deviceName("Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")).toBe("Firefox on Linux");
    expect(deviceName("curl/8")).toBe("Browser");
  });
});
