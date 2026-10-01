// Copying must keep working outside a secure context, where the async
// Clipboard API is unavailable, so a textarea selection is the fallback.
export async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.width = "1px";
  textarea.style.height = "1px";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  try {
    if (!document.execCommand("copy")) {
      throw new Error("copy command was rejected");
    }
  } finally {
    textarea.remove();
  }
}

export function flashButtonText(button, text) {
  const previous = button.textContent;
  button.textContent = text;
  setTimeout(() => {
    if (button.isConnected) {
      button.textContent = previous;
    }
  }, 1200);
}
