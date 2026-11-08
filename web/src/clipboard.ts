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
