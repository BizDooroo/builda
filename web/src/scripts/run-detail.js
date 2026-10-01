import { deleteJSON, getJSON, postJSON } from "./api.js";
import { escapeHTML, formatTime, isActiveStatus, renderChips, renderStatusBadge, showNotice } from "./format.js";
import { t } from "./i18n.js";
import { confirmAction, initShell } from "./shell.js";
import { LogView } from "./logview.js";

const notice = document.getElementById("run-status");
const titleNode = document.getElementById("run-title");
const idNode = document.getElementById("run-id");
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

async function load() {
  try {
    run = await getJSON("/api/runs/" + encodeURIComponent(runID));
    render();
    await logView.refresh();
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

function render() {
  if (!run) return;
  titleNode.textContent = run.job_name || run.job_id;
  idNode.textContent = run.id + " · " + (run.agent_id || "-");
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
  cancelButton.hidden = !isActiveStatus(run.status);
  deleteButton.hidden = isActiveStatus(run.status);
  resolveButton.hidden = !run.needs_attention;
}

cancelButton?.addEventListener("click", async () => {
  try {
    await postJSON("/api/runs/" + encodeURIComponent(runID) + "/cancel", {});
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

rerunButton?.addEventListener("click", async () => {
  try {
    const result = await postJSON("/api/runs/" + encodeURIComponent(runID) + "/rerun", {});
    window.location.href = "/runs/" + encodeURIComponent(result.run.id);
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

resolveButton?.addEventListener("click", async () => {
  try {
    await postJSON("/api/runs/" + encodeURIComponent(runID) + "/resolve", {});
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

deleteButton?.addEventListener("click", async () => {
  if (!confirmAction(t("action.deleteRun") + "?")) return;
  try {
    await deleteJSON("/api/runs/" + encodeURIComponent(runID));
    window.location.href = "/runs";
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

document.addEventListener("builda:localechange", render);

await initShell();
await load();
setInterval(load, 2000);
