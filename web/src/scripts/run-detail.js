import { deleteJSON, getJSON, postJSON } from "./api.js";
import { escapeHTML, formatTime, isActiveStatus, renderChips, renderStatusBadge, showNotice } from "./format.js";
import { t } from "./i18n.js";
import { confirmAction, initShell } from "./shell.js";
import { LogView } from "./logview.js";
import { copyText, flashButtonText } from "./clipboard.js";

const notice = document.getElementById("run-status");
const titleNode = document.getElementById("run-title");
const idNode = document.getElementById("run-id");
const agentNode = document.getElementById("run-agent");
const copyIDButton = document.getElementById("copy-run-id");
const paramsNode = document.getElementById("params");
const badgeHost = document.getElementById("badge-host");
const scriptNode = document.getElementById("script");
const timesNode = document.getElementById("times");
const attentionNode = document.getElementById("attention");
const cancelButton = document.getElementById("cancel-run");
const rerunButton = document.getElementById("rerun-run");
const deleteButton = document.getElementById("delete-run");
const resolveButton = document.getElementById("resolve-run");

const runID = decodeURIComponent(window.location.pathname.replace(/^\/runs\//, "").replace(/\/$/, ""));

const logView = new LogView({
  pre: document.getElementById("log"),
  copyButton: document.getElementById("copy-log"),
  followButton: document.getElementById("follow-log"),
  notice,
});
logView.select(runID);

let run = null;
let renderedRun = "";
let loading = false;

try {
  const previous = JSON.parse(sessionStorage.getItem("builda.runs.return") || "null");
  const back = document.querySelector("[data-run-back]");
  if (previous?.href && back) back.href = previous.href;
} catch {
  // The default history link remains available when session storage is disabled.
}

async function load() {
  if (loading || document.visibilityState === "hidden") return;
  loading = true;
  try {
    run = await getJSON("/api/runs/" + encodeURIComponent(runID));
    render();
    await logView.refresh();
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  } finally {
    loading = false;
  }
}

function render() {
  if (!run) return;
  const signature = JSON.stringify([
    run.id, run.job_name, run.job_id, run.agent_id, run.status, run.parameters,
    run.timeout_text, run.requested_at, run.started_at, run.finished_at,
    run.needs_attention, run.attention, run.script, run.error,
  ]);
  if (signature === renderedRun) return;
  const detailsOpen = document.querySelector(".script-details")?.open;
  const focusedKey = document.activeElement?.dataset?.focusKey || "";
  renderedRun = signature;
  titleNode.textContent = run.job_name || run.job_id;
  idNode.textContent = run.id;
  agentNode.textContent = run.agent_id || "-";
  paramsNode.innerHTML = renderChips(run.parameters);
  paramsNode.hidden = !paramsNode.innerHTML;
  badgeHost.innerHTML = renderStatusBadge(run.status);
  scriptNode.textContent = run.script || "";
  timesNode.innerHTML =
    "<span>" + escapeHTML(t("time.request")) + " " + escapeHTML(formatTime(run.requested_at)) + "</span>" +
    "<span>" + escapeHTML(t("time.start")) + " " + escapeHTML(formatTime(run.started_at)) + "</span>" +
    "<span>" + escapeHTML(t("time.finished")) + " " + escapeHTML(formatTime(run.finished_at)) + "</span>" +
    "<span>" + escapeHTML(t("field.timeout")) + " " + escapeHTML(run.timeout_text || "-") + "</span>";
  if (run.needs_attention) {
    attentionNode.hidden = false;
    attentionNode.textContent = t("run.attention", { message: run.attention || "" });
  } else {
    attentionNode.hidden = true;
  }
  cancelButton.hidden = !isActiveStatus(run.status) || run.status === "CANCELING";
  deleteButton.hidden = isActiveStatus(run.status);
  resolveButton.hidden = !run.needs_attention;
  rerunButton.hidden = isActiveStatus(run.status) || run.needs_attention;
  const details = document.querySelector(".script-details");
  if (details && detailsOpen) details.open = true;
  if (focusedKey) document.querySelector('[data-focus-key="' + focusedKey + '"]')?.focus({ preventScroll: true });
}

cancelButton?.addEventListener("click", async () => {
  if (cancelButton.disabled) return;
  cancelButton.disabled = true;
  try {
    await postJSON("/api/runs/" + encodeURIComponent(runID) + "/cancel", {});
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    cancelButton.disabled = false;
  }
});

rerunButton?.addEventListener("click", async () => {
  if (rerunButton.disabled) return;
  rerunButton.disabled = true;
  try {
    const result = await postJSON("/api/runs/" + encodeURIComponent(runID) + "/rerun", {});
    window.location.href = "/runs/" + encodeURIComponent(result.run.id);
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    rerunButton.disabled = false;
  }
});

resolveButton?.addEventListener("click", async () => {
  if (resolveButton.disabled) return;
  resolveButton.disabled = true;
  try {
    await postJSON("/api/runs/" + encodeURIComponent(runID) + "/resolve", {});
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    resolveButton.disabled = false;
  }
});

deleteButton?.addEventListener("click", async () => {
  if (deleteButton.disabled) return;
  if (!confirmAction(t("action.deleteRun") + "?")) return;
  deleteButton.disabled = true;
  try {
    await deleteJSON("/api/runs/" + encodeURIComponent(runID));
    window.location.href = "/runs";
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    deleteButton.disabled = false;
  }
});

copyIDButton?.addEventListener("click", async () => {
  try {
    await copyText(run?.id || "");
    flashButtonText(copyIDButton, t("common.copied"));
  } catch (error) {
    showNotice(notice, t("notice.copyFailed", { error: error.message }), "error");
  }
});

document.addEventListener("builda:localechange", () => {
  renderedRun = "";
  render();
  logView.localeChanged();
});
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") load();
});

await initShell();
await load();
setInterval(load, 2000);
