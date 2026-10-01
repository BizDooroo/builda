import { deleteJSON, getJSON, postJSON } from "./api.js";
import { escapeHTML, formatTime, showNotice } from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { confirmAction, initShell, mountRecords } from "./shell.js";
import { copyText, flashButtonText } from "./clipboard.js";

const notice = document.getElementById("settings-status");
const editorArea = document.getElementById("config-editor");
const tokenList = document.getElementById("tokens");
const secretHost = document.getElementById("token-secret");
const configStatus = document.getElementById("config-status");
const tokenForm = document.getElementById("token-form");
const tokenSubmit = tokenForm?.querySelector('[type="submit"]');
const saveConfigButton = document.getElementById("save-config");
let savedConfig = "";
let tokenSubmitting = false;

function setConfigStatus(message, type = "") {
  configStatus.textContent = message;
  configStatus.className = "editor-status" + (type ? " " + type : "");
}

function configDirty() {
  return editorArea.value !== savedConfig;
}

async function loadConfig() {
  if (configDirty() && !confirmAction(t("settings.discardConfig"))) return;
  const loadButton = document.getElementById("load-config");
  if (loadButton) loadButton.disabled = true;
  try {
    const document_ = await getJSON("/api/config");
    editorArea.value = document_.content || "";
    savedConfig = editorArea.value;
    setConfigStatus("");
    document.querySelectorAll("[data-config-path]").forEach((node) => {
      node.textContent = document_.path || "";
    });
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  } finally {
    if (loadButton) loadButton.disabled = false;
  }
}

async function loadTokens() {
  try {
    const response = await getJSON("/api/tokens");
    mountRecords(tokenList, response.api_tokens || [], (token) => {
      return (
        '<div class="record"><div class="record-head"><div class="record-title">' +
        "<strong>" + escapeHTML(token.name || token.id) + "</strong>" +
        '<div class="meta"><span class="identifier">' + escapeHTML(token.id) + "</span> · " + escapeHTML(formatTime(token.created_at)) + "</div>" +
        "</div><div class=\"record-actions\">" +
        '<button type="button" class="danger compact" data-revoke="' + escapeHTML(token.id) + '">' +
        escapeHTML(t("action.revoke")) + "</button></div></div></div>"
      );
    });
    applyTranslations(tokenList);
  } catch (error) {
    tokenList?.setAttribute("aria-busy", "false");
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

document.getElementById("load-config")?.addEventListener("click", loadConfig);

editorArea?.addEventListener("input", () => {
  setConfigStatus(configDirty() ? t("settings.configModified") : "");
});

saveConfigButton?.addEventListener("click", async () => {
  if (saveConfigButton.disabled) return;
  saveConfigButton.disabled = true;
  setConfigStatus(t("common.loading"));
  try {
    await postJSON("/api/config", { content: editorArea.value });
    savedConfig = editorArea.value;
    setConfigStatus(t("settings.configSaved"), "ok");
    showNotice(notice, t("notice.saved"), "ok");
  } catch (error) {
    setConfigStatus(t("notice.requestFailed", { error: error.message }), "error");
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    saveConfigButton.disabled = false;
  }
});

tokenForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (tokenSubmitting) return;
  const input = document.getElementById("token-name");
  const name = input.value.trim();
  if (!name) return;
  tokenSubmitting = true;
  tokenSubmit.disabled = true;
  try {
    const issued = await postJSON("/api/tokens", { name });
    input.value = "";
    secretHost.hidden = false;
    secretHost.innerHTML =
      "<strong>" + escapeHTML(issued.name || name) + "</strong>" +
      "<div>" + escapeHTML(t("agents.tokenOnce")) + "</div>" +
      "<code>" + escapeHTML(issued.token) + "</code>" +
      '<div><button type="button" class="secondary compact" data-copy-token>' + escapeHTML(t("action.copy")) + "</button></div>";
    secretHost.querySelector("[data-copy-token]").addEventListener("click", async (clickEvent) => {
      try {
        await copyText(issued.token);
        flashButtonText(clickEvent.currentTarget, t("common.copied"));
      } catch (error) {
        showNotice(notice, t("notice.copyFailed", { error: error.message }), "error");
      }
    });
    await loadTokens();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    tokenSubmitting = false;
    tokenSubmit.disabled = false;
  }
});

tokenList?.addEventListener("click", async (event) => {
  const revoke = event.target.closest("[data-revoke]");
  if (!revoke) return;
  if (!confirmAction(t("action.revoke") + " " + revoke.dataset.revoke + "?")) return;
  try {
    await deleteJSON("/api/tokens/" + encodeURIComponent(revoke.dataset.revoke));
    showNotice(notice, t("notice.deleted"), "ok");
    await loadTokens();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

document.addEventListener("builda:localechange", loadTokens);
window.addEventListener("pagehide", () => {
  secretHost.replaceChildren();
  secretHost.hidden = true;
});

await initShell();
await loadConfig();
await loadTokens();
