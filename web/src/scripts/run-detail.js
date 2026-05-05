import {
  copyText,
  escapeHTML,
  flashButtonText,
  formatTime,
  initShell,
  isActiveStatus,
  renderLogText,
  renderRunParamChips,
  runLogDisplayText,
  showNotice,
  statusLabel,
  t,
} from "./shared.js";

const runID = decodeURIComponent(window.location.pathname.replace(/^\/runs\//, "").replace(/\/$/, ""));
const runStatusEl = document.querySelector("#run-status");
const titleEl = document.querySelector("#run-title");
const runIDEl = document.querySelector("#run-id");
const scriptEl = document.querySelector("#script");
const logEl = document.querySelector("#log");
const badgeEl = document.querySelector("#badge");
const cancelRunEl = document.querySelector("#cancel-run");
const copyLogEl = document.querySelector("#copy-log");
const deleteRunEl = document.querySelector("#delete-run");
const followLogEl = document.querySelector("#follow-log");
const requestedEl = document.querySelector("#requested");
const startedEl = document.querySelector("#started");
const finishedEl = document.querySelector("#finished");
const canceledEl = document.querySelector("#canceled");
const paramsEl = document.querySelector("#params");
let timer = 0;
let followLog = true;
let currentLogText = logEl.textContent;
let latestRun = null;

cancelRunEl.addEventListener("click", async () => {
  if (!confirm(t("confirm.cancelRun"))) return;
  cancelRunEl.disabled = true;
  try {
    const response = await fetch("/api/runs/" + encodeURIComponent(runID) + "/cancel", { method: "POST" });
    if (!response.ok) throw new Error(await response.text());
    showNotice(runStatusEl, t("notice.runCancelRequested"), "ok");
    await refresh();
  } catch (error) {
    showNotice(runStatusEl, t("notice.runCancelFailed", { error: error.message }), "error");
  } finally {
    cancelRunEl.disabled = false;
  }
});

copyLogEl.addEventListener("click", async () => {
  copyLogEl.disabled = true;
  try {
    await copyText(currentLogText);
    flashButtonText(copyLogEl, t("common.copied"));
    showNotice(runStatusEl, t("notice.logCopied"), "ok");
  } catch (error) {
    flashButtonText(copyLogEl, t("common.failed"));
    showNotice(runStatusEl, t("notice.logCopyFailed", { error: error.message }), "error");
  } finally {
    copyLogEl.disabled = false;
  }
});

deleteRunEl.addEventListener("click", async () => {
  if (!confirm(t("confirm.deleteRun"))) return;
  deleteRunEl.disabled = true;
  try {
    const response = await fetch("/api/runs/" + encodeURIComponent(runID), { method: "DELETE" });
    if (!response.ok) throw new Error(await response.text());
    if (timer) clearInterval(timer);
    showNotice(runStatusEl, t("notice.deleteRunSuccess"), "ok");
    window.location.href = "/runs";
  } catch (error) {
    showNotice(runStatusEl, t("notice.deleteRunFailed", { error: error.message }), "error");
  } finally {
    renderLogActions(latestRun);
  }
});

followLogEl.addEventListener("click", () => {
  followLog = !followLog;
  renderFollowButton();
  if (followLog) logEl.scrollTop = logEl.scrollHeight;
});

logEl.addEventListener("scroll", () => {
  const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
  if (!atBottom && followLog) {
    followLog = false;
    renderFollowButton();
  }
});

async function refresh() {
  try {
    const [runResponse, logResponse] = await Promise.all([
      fetch("/api/runs/" + encodeURIComponent(runID)),
      fetch("/api/runs/" + encodeURIComponent(runID) + "/log"),
    ]);
    if (!runResponse.ok) throw new Error(await runResponse.text());
    const run = await runResponse.json();
    latestRun = run;
    renderRun(run);
    if (run.status !== "QUEUED" && run.status !== "RUNNING" && timer) {
      clearInterval(timer);
      timer = 0;
    }
    if (!logResponse.ok) throw new Error(await logResponse.text());
    const shouldFollow = followLog || logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
    const fetchedLogText = await logResponse.text();
    currentLogText = fetchedLogText;
    logEl.innerHTML = renderLogText(runLogDisplayText(fetchedLogText));
    if (shouldFollow) logEl.scrollTop = logEl.scrollHeight;
  } catch (error) {
    showNotice(runStatusEl, t("notice.loadRunFailed", { error: error.message }), "error");
  }
}

function renderRun(run) {
  document.title = run.task_name + " · Builda";
  titleEl.textContent = run.task_name;
  runIDEl.textContent = run.id;
  scriptEl.textContent = run.script;
  badgeEl.textContent = statusLabel(run.status);
  badgeEl.className = "badge status-" + escapeHTML(run.status);
  cancelRunEl.hidden = !isActiveStatus(run.status);
  requestedEl.textContent = t("time.request") + " " + formatTime(run.requested_at);
  startedEl.textContent = t("time.start") + " " + formatTime(run.started_at);
  finishedEl.textContent = t("time.finished") + " " + formatTime(run.finished_at);
  canceledEl.textContent = t("time.cancelled") + " " + formatTime(run.canceled_at);
  const paramChips = renderRunParamChips(run.inputs);
  if (paramChips) {
    paramsEl.hidden = false;
    paramsEl.innerHTML = paramChips;
  } else {
    paramsEl.hidden = true;
    paramsEl.innerHTML = "";
  }
  renderLogActions(run);
}

function renderLogActions(run) {
  deleteRunEl.disabled = !run || isActiveStatus(run.status);
}

function renderFollowButton() {
  followLogEl.classList.toggle("active", followLog);
  followLogEl.setAttribute("aria-pressed", String(followLog));
  followLogEl.textContent = followLog ? t("action.followOn") : t("action.followOff");
}

await initShell();
document.addEventListener("builda:localechange", () => {
  if (latestRun) renderRun(latestRun);
  renderFollowButton();
});
renderFollowButton();
renderLogActions(null);
await refresh();
timer = setInterval(refresh, 1000);
