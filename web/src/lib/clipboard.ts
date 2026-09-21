/** Copy text without exposing it to logs or persistent browser storage. */
export async function copyText(value: string): Promise<boolean> {
  const clipboard = typeof navigator !== "undefined" ? navigator.clipboard : undefined;
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(value);
      return true;
    } catch {
      // Fall through for non-secure contexts and browsers that deny permission.
    }
  }

  if (typeof document === "undefined" || typeof document.execCommand !== "function") return false;

  const active = document.activeElement as HTMLElement | null;
  const selection = document.getSelection();
  const savedRange = selection?.rangeCount ? selection.getRangeAt(0).cloneRange() : null;
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.setAttribute("aria-hidden", "true");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  document.body.appendChild(textarea);

  let copied = false;
  try {
    textarea.focus();
    textarea.select();
    copied = document.execCommand("copy");
  } catch {
    copied = false;
  } finally {
    textarea.remove();
    if (active && typeof active.focus === "function") active.focus();
    if (savedRange && selection) {
      selection.removeAllRanges();
      selection.addRange(savedRange);
    }
  }
  return copied;
}
