import { deleteJSON, getJSON, postJSON } from "./api.js";
import { escapeHTML, formatTime, showNotice } from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { confirmAction, initShell, mountRecords } from "./shell.js";
import { copyText, flashButtonText } from "./clipboard.js";

const notice = document.getElementById("settings-status");
const editorArea = document.getElementById("config-editor");
const tokenList = document.getElementById("tokens");
const secretHost = document.getElementById("token-secret");

async function loadConfig() {
  try {
    const document_ = await getJSON("/api/config");
    editorArea.value = document_.content || "";
    document.querySelectorAll("[data-config-path]").forEach((node) => {
      node.textContent = document_.path || "";
    });
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

async function loadTokens() {
  try {
    const response = await getJSON("/api/tokens");
    mountRecords(tokenList, response.api_tokens || [], (token) => {
      return (
        '<div class="record"><div class="record-head"><div class="record-title">' +
        "<strong>" + escapeHTML(token.name || token.id) + "</strong>" +
        '<div class="meta">' + escapeHTML(token.id) + " · " + escapeHTML(formatTime(token.created_at)) + "</div>" +
        "</div><div class=\"record-actions\">" +
        '<button type="button" class="danger compact" data-revoke="' + escapeHTML(token.id) + '">' +
        escapeHTML(t("action.revoke")) + "</button></div></div></div>"
      );
    });
    applyTranslations(tokenList);
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

document.getElementById("load-config")?.addEventListener("click", loadConfig);

document.getElementById("save-config")?.addEventListener("click", async () => {
  try {
    await postJSON("/api/config", { content: editorArea.value });
    showNotice(notice, t("notice.saved"), "ok");
    await loadConfig();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

document.getElementById("token-form")?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = document.getElementById("token-name");
  const name = input.value.trim();
  if (!name) return;
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

await initShell();
await loadConfig();
await loadTokens();
