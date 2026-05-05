import {
  copyText,
  escapeHTML,
  flashButtonText,
  formatElapsed,
  formatTime,
  initShell,
  isActiveStatus,
  isFailedStatus,
  renderLogText,
  renderRunListTimes,
  renderRunParams,
  renderStatusBadge,
  renderTimes,
  runLogDisplayText,
  showNotice,
  statusLabel,
  t,
} from "./shared.js";

const runsStatusEl = document.querySelector("#runs-status");
const runsEl = document.querySelector("#runs");
const runCountEl = document.querySelector("#run-count");
const runFilterEl = document.querySelector("#run-filter");
const clearRunFilterEl = document.querySelector("#clear-run-filter");
const statusFiltersEl = document.querySelector("#status-filters");
const runPickerEl = document.querySelector("#run-picker");
const runSelectEl = document.querySelector("#run-select");
const summaryEl = document.querySelector("#summary");
const logEl = document.querySelector("#log");
const copyLogEl = document.querySelector("#copy-log");
const deleteRunEl = document.querySelector("#delete-run");
const followLogEl = document.querySelector("#follow-log");
const params = new URLSearchParams(window.location.search);
const taskFilter = params.get("task") || "";
let statusFilter = params.get("status") || "all";
let selectedRunID = params.get("run") || "";
let latestRuns = [];
let visibleRuns = [];
let followLog = true;
let currentLogText = logEl.textContent;

runsEl.classList.add("responsive-picker");

document.addEventListener("click", async (event) => {
  const statusButton = event.target.closest("[data-status-filter]");
  if (statusButton) {
    event.preventDefault();
    statusFilter = statusButton.dataset.statusFilter;
    const nextVisibleRuns = filterRuns(latestRuns);
    if (!selectedRunID || !nextVisibleRuns.some((run) => run.id === selectedRunID)) {
      selectedRunID = nextVisibleRuns.length ? nextVisibleRuns[0].id : "";
    }
    updateURL(true);
    renderRuns(latestRuns);
    await renderSelectedRun();
  }

  const runButton = event.target.closest("[data-run-id]");
  if (runButton) {
    event.preventDefault();
    selectRun(runButton.dataset.runId, true);
  }

  const cancel = event.target.closest("[data-cancel]");
  if (cancel) {
    event.preventDefault();
    if (!confirm(t("confirm.cancelRun"))) return;
    cancel.disabled = true;
    try {
      const response = await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", { method: "POST" });
      if (!response.ok) throw new Error(await response.text());
      showNotice(runsStatusEl, t("notice.runCancelRequested"), "ok");
      await refresh();
    } catch (error) {
      showNotice(runsStatusEl, t("notice.runCancelFailed", { error: error.message }), "error");
    } finally {
      cancel.disabled = false;
    }
  }
});

copyLogEl.addEventListener("click", async () => {
  copyLogEl.disabled = true;
  try {
    await copyText(currentLogText);
    flashButtonText(copyLogEl, t("common.copied"));
    showNotice(runsStatusEl, t("notice.logCopied"), "ok");
  } catch (error) {
    flashButtonText(copyLogEl, t("common.failed"));
    showNotice(runsStatusEl, t("notice.logCopyFailed", { error: error.message }), "error");
  } finally {
    copyLogEl.disabled = false;
  }
});

deleteRunEl.addEventListener("click", async () => {
  if (!selectedRunID || !confirm(t("confirm.deleteRun"))) return;
  const deletedRunID = selectedRunID;
  deleteRunEl.disabled = true;
  try {
    const response = await fetch("/api/runs/" + encodeURIComponent(deletedRunID), { method: "DELETE" });
    if (!response.ok) throw new Error(await response.text());
    latestRuns = latestRuns.filter((run) => run.id !== deletedRunID);
    visibleRuns = filterRuns(latestRuns);
    selectedRunID = visibleRuns.length ? visibleRuns[0].id : "";
    updateURL(false);
    renderRuns(latestRuns);
    await renderSelectedRun();
    showNotice(runsStatusEl, t("notice.deleteRunSuccess"), "ok");
  } catch (error) {
    showNotice(runsStatusEl, t("notice.deleteRunFailed", { error: error.message }), "error");
  } finally {
    renderLogActions(latestRuns.find((run) => run.id === selectedRunID));
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

document.addEventListener("change", async (event) => {
  if (event.target === runSelectEl && runSelectEl.value) {
    await selectRun(runSelectEl.value, true);
  }
});

async function refresh() {
  try {
    const stateURL = taskFilter ? "/api/state?task=" + encodeURIComponent(taskFilter) : "/api/state";
    const response = await fetch(stateURL);
    if (!response.ok) throw new Error(await response.text());
    const state = await response.json();
    latestRuns = state.runs || [];
    visibleRuns = filterRuns(latestRuns);
    if (!visibleRuns.length) {
      renderRuns(latestRuns);
      summaryEl.innerHTML = '<div class="empty">' + emptyRunMessage() + "</div>";
      currentLogText = emptyRunMessage();
      logEl.textContent = currentLogText;
      renderLogActions(null);
      return;
    }
    if (!selectedRunID || !visibleRuns.some((run) => run.id === selectedRunID)) {
      selectedRunID = visibleRuns[0].id;
      updateURL(false);
    }
    renderRuns(latestRuns);
    await renderSelectedRun();
  } catch (error) {
    showNotice(runsStatusEl, t("notice.loadRunsFailed", { error: error.message }), "error");
  }
}

function renderRuns(runs) {
  visibleRuns = filterRuns(runs);
  if (taskFilter) {
    runFilterEl.hidden = false;
    runFilterEl.textContent = t("runs.taskFilter", { task: taskFilter });
    clearRunFilterEl.hidden = false;
  } else {
    runFilterEl.hidden = true;
    runFilterEl.textContent = "";
    clearRunFilterEl.hidden = true;
  }
  renderStatusFilters(runs);
  runCountEl.textContent = t("count.runs", { count: visibleRuns.length });
  if (!visibleRuns.length) {
    runPickerEl.hidden = true;
    runsEl.innerHTML = '<div class="empty">' + emptyRunMessage() + "</div>";
    return;
  }
  runPickerEl.hidden = false;
  runSelectEl.innerHTML = visibleRuns.map((run) => {
    return '<option value="' + escapeHTML(run.id) + '"' + (run.id === selectedRunID ? " selected" : "") + ">" +
      escapeHTML(run.task_name + " · " + statusLabel(run.status) + " · " + t("time.request") + " " + formatTime(run.requested_at) + " · " + t("time.elapsed") + " " + formatElapsed(run)) +
      "</option>";
  }).join("");
  runsEl.innerHTML = visibleRuns.map((run) => {
    const active = run.id === selectedRunID ? " active" : "";
    return '<button class="run-item' + active + '" data-run-id="' + escapeHTML(run.id) + '">' +
      '<span class="run-title"><span class="run-name">' + escapeHTML(run.task_name) + '</span><span class="meta">' + escapeHTML(run.id) + "</span></span>" +
      renderStatusBadge(run.status) +
      renderRunListTimes(run) +
      "</button>";
  }).join("");
}

async function selectRun(runID, push) {
  selectedRunID = runID;
  renderRuns(latestRuns);
  updateURL(push);
  await renderSelectedRun();
}

async function renderSelectedRun() {
  if (!selectedRunID) {
    summaryEl.innerHTML = '<div class="empty">' + emptyRunMessage() + "</div>";
    currentLogText = emptyRunMessage();
    logEl.textContent = currentLogText;
    renderLogActions(null);
    return;
  }
  try {
    const [runResponse, logResponse] = await Promise.all([
      fetch("/api/runs/" + encodeURIComponent(selectedRunID)),
      fetch("/api/runs/" + encodeURIComponent(selectedRunID) + "/log"),
    ]);
    if (!runResponse.ok) throw new Error(await runResponse.text());
    const run = await runResponse.json();
    const canCancel = isActiveStatus(run.status);
    const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">' + escapeHTML(t("action.cancel")) + "</button>" : "";
    summaryEl.innerHTML = '<div class="summary-head"><div class="summary-title"><h1>' + escapeHTML(run.task_name) + "</h1>" +
      '<div class="meta">' + escapeHTML(run.id) + "</div>" +
      renderRunParams(run) +
      "</div>" +
      '<div class="summary-actions">' + renderStatusBadge(run.status) + cancel + "</div></div>" +
      '<div class="kv"><span>' + escapeHTML(t("field.taskID")) + " <b>" + escapeHTML(run.task_id) + '</b></span><span>' + escapeHTML(t("field.exit")) + " <b>" + escapeHTML(run.exit_code) + "</b></span></div>" +
      renderTimes(run) +
      '<details class="script-details"><summary>' + escapeHTML(t("script.view")) + "</summary><code>" + escapeHTML(run.script) + "</code></details>";
    if (!logResponse.ok) throw new Error(await logResponse.text());
    const shouldFollow = followLog || logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
    const fetchedLogText = await logResponse.text();
    currentLogText = fetchedLogText;
    logEl.innerHTML = renderLogText(runLogDisplayText(fetchedLogText));
    renderLogActions(run);
    if (shouldFollow) logEl.scrollTop = logEl.scrollHeight;
  } catch (error) {
    showNotice(runsStatusEl, t("notice.loadRunFailed", { error: error.message }), "error");
  }
}

function updateURL(push) {
  const next = new URLSearchParams();
  if (taskFilter) next.set("task", taskFilter);
  if (statusFilter !== "all") next.set("status", statusFilter);
  if (selectedRunID) next.set("run", selectedRunID);
  const url = "/runs" + (next.toString() ? "?" + next.toString() : "");
  if (push) {
    history.pushState(null, "", url);
  } else {
    history.replaceState(null, "", url);
  }
}

function filterRuns(runs) {
  if (statusFilter === "active") return runs.filter((run) => isActiveStatus(run.status));
  if (statusFilter === "failed") return runs.filter((run) => isFailedStatus(run.status));
  if (statusFilter === "success") return runs.filter((run) => run.status === "SUCCESS");
  return runs;
}

function renderStatusFilters(runs) {
  const filters = [
    ["all", t("filter.all"), runs.length],
    ["active", t("filter.active"), runs.filter((run) => isActiveStatus(run.status)).length],
    ["failed", t("filter.failed"), runs.filter((run) => isFailedStatus(run.status)).length],
    ["success", t("filter.success"), runs.filter((run) => run.status === "SUCCESS").length],
  ];
  statusFiltersEl.innerHTML = filters.map(([key, label, count]) => {
    const active = key === statusFilter ? " active" : "";
    return '<button class="secondary compact status-filter' + active + '" type="button" data-status-filter="' + key + '" aria-pressed="' + String(key === statusFilter) + '">' +
      escapeHTML(label + " " + count) +
      "</button>";
  }).join("");
}

function emptyRunMessage() {
  if (taskFilter && statusFilter === "failed") return t("empty.taskFailedRuns");
  if (taskFilter && statusFilter === "active") return t("empty.taskActiveRuns");
  if (taskFilter && statusFilter === "success") return t("empty.taskSuccessRuns");
  if (taskFilter) return t("empty.taskRuns");
  if (statusFilter === "failed") return t("empty.failedRuns");
  if (statusFilter === "active") return t("empty.activeRuns");
  if (statusFilter === "success") return t("empty.successRuns");
  return t("empty.noRuns");
}

function renderFollowButton() {
  followLogEl.classList.toggle("active", followLog);
  followLogEl.setAttribute("aria-pressed", String(followLog));
  followLogEl.textContent = followLog ? t("action.followOn") : t("action.followOff");
}

function renderLogActions(run) {
  deleteRunEl.disabled = !run || isActiveStatus(run.status);
}

await initShell();
document.addEventListener("builda:localechange", () => {
  renderRuns(latestRuns);
  renderFollowButton();
  void renderSelectedRun();
});
renderFollowButton();
renderLogActions(null);
await refresh();
setInterval(refresh, 1500);
