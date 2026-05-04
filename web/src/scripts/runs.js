import {
  copyText,
  escapeHTML,
  flashButtonText,
  formatElapsed,
  formatTime,
  initShell,
  isActiveStatus,
  isFailedStatus,
  renderRunListTimes,
  renderRunParams,
  renderStatusBadge,
  renderTimes,
  showNotice,
  statusLabel,
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
const followLogEl = document.querySelector("#follow-log");
const params = new URLSearchParams(window.location.search);
const taskFilter = params.get("task") || "";
let statusFilter = params.get("status") || "all";
let selectedRunID = params.get("run") || "";
let latestRuns = [];
let visibleRuns = [];
let followLog = true;

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
    if (!confirm("이 실행을 취소할까요? 이미 시작된 스크립트가 중단될 수 있습니다.")) return;
    cancel.disabled = true;
    try {
      const response = await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", { method: "POST" });
      if (!response.ok) throw new Error(await response.text());
      showNotice(runsStatusEl, "취소 요청을 보냈습니다.", "ok");
      await refresh();
    } catch (error) {
      showNotice(runsStatusEl, "취소에 실패했습니다: " + error.message, "error");
    } finally {
      cancel.disabled = false;
    }
  }
});

copyLogEl.addEventListener("click", async () => {
  copyLogEl.disabled = true;
  try {
    await copyText(logEl.textContent);
    flashButtonText(copyLogEl, "복사됨");
    showNotice(runsStatusEl, "로그를 복사했습니다.", "ok");
  } catch (error) {
    flashButtonText(copyLogEl, "실패");
    showNotice(runsStatusEl, "로그 복사에 실패했습니다: " + error.message, "error");
  } finally {
    copyLogEl.disabled = false;
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
      logEl.textContent = emptyRunMessage();
      return;
    }
    if (!selectedRunID || !visibleRuns.some((run) => run.id === selectedRunID)) {
      selectedRunID = visibleRuns[0].id;
      updateURL(false);
    }
    renderRuns(latestRuns);
    await renderSelectedRun();
  } catch (error) {
    showNotice(runsStatusEl, "실행 기록을 불러오지 못했습니다: " + error.message, "error");
  }
}

function renderRuns(runs) {
  visibleRuns = filterRuns(runs);
  if (taskFilter) {
    runFilterEl.hidden = false;
    runFilterEl.textContent = "task " + taskFilter;
    clearRunFilterEl.hidden = false;
  } else {
    runFilterEl.hidden = true;
    runFilterEl.textContent = "";
    clearRunFilterEl.hidden = true;
  }
  renderStatusFilters(runs);
  runCountEl.textContent = visibleRuns.length + (visibleRuns.length === 1 ? " run" : " runs");
  if (!visibleRuns.length) {
    runPickerEl.hidden = true;
    runsEl.innerHTML = '<div class="empty">' + emptyRunMessage() + "</div>";
    return;
  }
  runPickerEl.hidden = false;
  runSelectEl.innerHTML = visibleRuns.map((run) => {
    return '<option value="' + escapeHTML(run.id) + '"' + (run.id === selectedRunID ? " selected" : "") + ">" +
      escapeHTML(run.task_name + " · " + statusLabel(run.status) + " · request " + formatTime(run.requested_at) + " · elapsed " + formatElapsed(run)) +
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
    logEl.textContent = emptyRunMessage();
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
    const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">취소</button>' : "";
    summaryEl.innerHTML = '<div class="summary-head"><div class="summary-title"><h1>' + escapeHTML(run.task_name) + "</h1>" +
      '<div class="meta">' + escapeHTML(run.id) + "</div>" +
      renderRunParams(run) +
      "</div>" +
      '<div class="summary-actions">' + renderStatusBadge(run.status) + cancel + "</div></div>" +
      '<div class="kv"><span>Task <b>' + escapeHTML(run.task_id) + '</b></span><span>Exit <b>' + escapeHTML(run.exit_code) + "</b></span></div>" +
      renderTimes(run) +
      '<details class="script-details"><summary>스크립트 보기</summary><code>' + escapeHTML(run.script) + "</code></details>";
    if (!logResponse.ok) throw new Error(await logResponse.text());
    const shouldFollow = followLog || logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
    logEl.textContent = await logResponse.text();
    if (shouldFollow) logEl.scrollTop = logEl.scrollHeight;
  } catch (error) {
    showNotice(runsStatusEl, "선택한 실행을 불러오지 못했습니다: " + error.message, "error");
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
    ["all", "전체", runs.length],
    ["active", "대기/실행", runs.filter((run) => isActiveStatus(run.status)).length],
    ["failed", "실패", runs.filter((run) => isFailedStatus(run.status)).length],
    ["success", "성공", runs.filter((run) => run.status === "SUCCESS").length],
  ];
  statusFiltersEl.innerHTML = filters.map(([key, label, count]) => {
    const active = key === statusFilter ? " active" : "";
    return '<button class="secondary compact status-filter' + active + '" type="button" data-status-filter="' + key + '" aria-pressed="' + String(key === statusFilter) + '">' +
      escapeHTML(label + " " + count) +
      "</button>";
  }).join("");
}

function emptyRunMessage() {
  if (taskFilter && statusFilter === "failed") return "이 작업의 실패한 실행이 없습니다.";
  if (taskFilter && statusFilter === "active") return "이 작업의 대기 또는 실행 중인 기록이 없습니다.";
  if (taskFilter && statusFilter === "success") return "이 작업의 성공한 실행이 없습니다.";
  if (taskFilter) return "이 작업의 실행 기록이 없습니다.";
  if (statusFilter === "failed") return "실패한 실행이 없습니다.";
  if (statusFilter === "active") return "대기 또는 실행 중인 기록이 없습니다.";
  if (statusFilter === "success") return "성공한 실행이 없습니다.";
  return "아직 실행 기록이 없습니다.";
}

function renderFollowButton() {
  followLogEl.classList.toggle("active", followLog);
  followLogEl.setAttribute("aria-pressed", String(followLog));
  followLogEl.textContent = followLog ? "Follow on" : "Follow off";
}

await initShell();
renderFollowButton();
await refresh();
setInterval(refresh, 1500);
