import { getJSON, postJSON, setCSRF } from "./api.js";
import { applyTranslations, t } from "./i18n.js";
import { initPreferences } from "./prefs.js";
import { escapeHTML } from "./format.js";

// initShell prepares preferences, resolves the session, and renders the build
// identity. Every page calls it before its first request.
export async function initShell() {
  initPreferences();
  bindLogout();
  bindNavigation();
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

function bindNavigation() {
  const toggle = document.querySelector("[data-nav-toggle]");
  const nav = document.getElementById("primary-nav");
  const account = document.querySelector(".account-menu");

  const closeNav = (restoreFocus = false) => {
    if (!nav || !toggle || nav.dataset.open !== "true") return;
    nav.dataset.open = "false";
    toggle.setAttribute("aria-expanded", "false");
    toggle.setAttribute("aria-label", t("nav.open"));
    if (restoreFocus) toggle.focus();
  };

  if (toggle && nav) {
    toggle.addEventListener("click", () => {
      const open = nav.dataset.open !== "true";
      nav.dataset.open = String(open);
      toggle.setAttribute("aria-expanded", String(open));
      toggle.setAttribute("aria-label", t(open ? "nav.close" : "nav.open"));
      if (open) nav.querySelector("a")?.focus();
    });
    nav.addEventListener("click", (event) => {
      if (event.target.closest("a")) closeNav();
    });
  }
  document.addEventListener("click", (event) => {
    const eventPath = event.composedPath();
    if (nav && toggle && !nav.contains(event.target) && !toggle.contains(event.target)) closeNav();
    if (account?.open && !eventPath.includes(account)) account.open = false;
    document.querySelectorAll(".action-menu[open]").forEach((menu) => {
      if (!eventPath.includes(menu)) menu.open = false;
    });
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    const actionMenu = document.querySelector(".action-menu[open]");
    if (actionMenu) {
      actionMenu.open = false;
      actionMenu.querySelector("summary")?.focus();
      return;
    }
    if (account?.open) {
      account.open = false;
      account.querySelector("summary")?.focus();
      return;
    }
    closeNav(true);
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
    node.setAttribute("aria-label", user ? t("account.signedIn", { user }) : "");
  });
}

// mountRecords renders a list of records into a container, or an empty note.
export function mountRecords(container, items, render) {
  if (!container) return;
  container.setAttribute("aria-busy", "false");
  const html = items.length
    ? items.map(render).join("")
    : '<div class="empty"><strong>' + escapeHTML(t("common.none")) + "</strong><span>" + escapeHTML(t("common.emptyHint")) + "</span></div>";
  if (container.innerHTML === html) return;
  const scrollTop = container.scrollTop;
  const focused = container.contains(document.activeElement) ? document.activeElement : null;
  const focusData = focused ? Object.entries(focused.dataset)[0] : null;
  const openDetails = Array.from(container.querySelectorAll("details")).map((node) => node.open);
  container.innerHTML = html;
  container.scrollTop = scrollTop;
  Array.from(container.querySelectorAll("details")).forEach((node, index) => {
    node.open = Boolean(openDetails[index]);
  });
  if (focusData) {
    const [key, value] = focusData;
    Array.from(container.querySelectorAll("[data-" + key.replace(/[A-Z]/g, (letter) => "-" + letter.toLowerCase()) + "]"))
      .find((node) => node.dataset[key] === value)?.focus({ preventScroll: true });
  }
}

// confirmAction keeps destructive buttons behind a browser confirmation.
export function confirmAction(message) {
  return window.confirm(message);
}
