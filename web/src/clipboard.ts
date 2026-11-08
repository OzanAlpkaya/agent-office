// Clipboard access for the terminals (ADR 0040).
//
// Copying first goes through the browser's own copy event, inside the user's
// gesture (a key, a click, the end of a mouse selection): that needs no
// permission. Only if the browser refuses it does it fall back to the async
// Clipboard API, which a browser may deny ("Write permission denied", found in
// Ozan's manual test: the first version only used it and dropped the error).
// Failures are thrown with a short reason for the UI to show, never swallowed.

// copyToClipboard copies text. Call it from the event handler of a user
// gesture: the first attempt runs synchronously, before any await.
export async function copyToClipboard(text: string): Promise<void> {
  if (!text) throw new Error("nothing is selected");
  if (copyWithEvent(text)) return;
  if (!navigator.clipboard) throw new Error("the clipboard needs https or localhost");
  try {
    await navigator.clipboard.writeText(text);
  } catch (e) {
    throw new Error(reason(e));
  }
}

// readClipboard reads text for the Paste buttons; the browser may ask for
// permission. Ctrl+Shift+V pastes without one.
export async function readClipboard(): Promise<string> {
  if (!navigator.clipboard) throw new Error("the clipboard needs https or localhost");
  try {
    return await navigator.clipboard.readText();
  } catch (e) {
    throw new Error(reason(e));
  }
}

// copyWithEvent asks the browser to copy and fills the copy event with text.
function copyWithEvent(text: string): boolean {
  let filled = false;
  const onCopy = (e: ClipboardEvent): void => {
    if (!e.clipboardData) return;
    e.clipboardData.setData("text/plain", text);
    e.preventDefault();
    e.stopImmediatePropagation(); // xterm.js's own copy handler would write its selection again
    filled = true;
  };
  document.addEventListener("copy", onCopy, true);
  try {
    // Deprecated but the one way to copy within a gesture without a permission.
    return document.execCommand("copy") && filled;
  } catch {
    return false; // the async Clipboard API is tried next; its error is the one shown
  } finally {
    document.removeEventListener("copy", onCopy, true);
  }
}

// reason is an error's message without the browser's boilerplate prefix.
export function reason(e: unknown): string {
  const msg = e instanceof Error ? e.message : String(e);
  return msg.replace(/^Failed to execute '\w+' on '\w+': /, "") || "unknown error";
}

// OSC 52 (ADR 0040): a terminal program asks to set the clipboard with
// ESC ] 52 ; <targets> ; <base64> BEL. Claude Code's fullscreen mode handles
// the mouse itself and copies its selection this way (seen in Ozan's
// sessions). "?" asks to read the clipboard, which agent-office never answers.

// maxOsc52 caps the base64 payload (about 750 KB of text).
export const maxOsc52 = 1 << 20;

export type Osc52 =
  | { kind: "write"; text: string }
  | { kind: "read" }
  | { kind: "invalid"; reason: string };

// parseOsc52 reads the data of an OSC 52 sequence (what follows "52;").
export function parseOsc52(data: string): Osc52 {
  const i = data.indexOf(";");
  if (i < 0) return { kind: "invalid", reason: "no payload" };
  const targets = data.slice(0, i);
  const payload = data.slice(i + 1);
  if (!/^[cpqs0-7]*$/.test(targets)) return { kind: "invalid", reason: "unknown clipboard" };
  if (payload === "?") return { kind: "read" };
  if (payload === "") return { kind: "invalid", reason: "empty" }; // "clear the clipboard": ignored
  if (payload.length > maxOsc52) return { kind: "invalid", reason: "too large" };
  if (!/^[A-Za-z0-9+/]*={0,2}$/.test(payload)) return { kind: "invalid", reason: "not base64" };
  let bytes: Uint8Array;
  try {
    bytes = Uint8Array.from(atob(payload), (c) => c.charCodeAt(0));
  } catch {
    return { kind: "invalid", reason: "not base64" };
  }
  return { kind: "write", text: new TextDecoder().decode(bytes) };
}
