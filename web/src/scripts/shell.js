import { getJSON, postJSON, setCSRF } from "./api.js";
import { applyTranslations, t } from "./i18n.js";
import { initPreferences } from "./prefs.js";
import { escapeHTML } from "./format.js";

// initShell prepares preferences, resolves the session, and renders the build
// identity. Every page calls it before its first request.
export async function initShell() {
  initPreferences();
  bindLogout();
  let session = {};
  try {
    session = await getJSON("/api/session");
    setCSRF(session.csrf);
  } catch {
    return {};
  }
  let meta = {};
  try {
    meta = await getJSON("/api/meta");
  } catch {
    meta = {};
  }
  renderShellMeta(meta, session);
  applyTranslations();
  document.addEventListener("builda:localechange", () => renderShellMeta(meta, session));
  return meta;
}

function bindLogout() {
  document.querySelectorAll("[data-logout]").forEach((button) => {
    button.addEventListener("click", async (event) => {
      event.preventDefault();
      try {
        await postJSON("/api/logout", {});
      } finally {
        window.location.href = "/login";
      }
    });
  });
}

export function renderShellMeta(meta, session = {}) {
  const version = String(meta.version || "").trim() || "dev";
  let commit = String(meta.commit || "").trim() || "unknown";
  if (commit !== "unknown" && meta.build_modified) commit += " dirty";
  const buildID = "Builda " + version + " @ " + commit;
  document.querySelectorAll("[data-build-id]").forEach((node) => {
    node.textContent = buildID;
    node.title = meta.version_info || buildID;
    node.setAttribute("aria-label", buildID);
    node.hidden = false;
  });
  document.querySelectorAll("[data-config-path]").forEach((node) => {
    node.textContent = meta.config_path || "";
  });
  const user = session.user || meta.user || "";
  document.querySelectorAll("[data-session-user]").forEach((node) => {
    node.textContent = user;
    node.hidden = !user;
  });
}

// mountRecords renders a list of records into a container, or an empty note.
export function mountRecords(container, items, render) {
  if (!container) return;
  if (!items.length) {
    container.innerHTML = '<div class="empty">' + escapeHTML(t("common.none")) + "</div>";
    return;
  }
  container.innerHTML = items.map(render).join("");
}

// confirmAction keeps destructive buttons behind a browser confirmation.
export function confirmAction(message) {
  return window.confirm(message);
}
