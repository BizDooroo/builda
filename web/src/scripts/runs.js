import {
  escapeHTML,
  formatElapsed,
  formatTime,
  initShell,
  renderRunListTimes,
  renderRunParams,
  renderStatusBadge,
  renderTimes,
} from "./shared.js";

const runsEl = document.querySelector("#runs");
const runCountEl = document.querySelector("#run-count");
const runFilterEl = document.querySelector("#run-filter");
const clearRunFilterEl = document.querySelector("#clear-run-filter");
const runPickerEl = document.querySelector("#run-picker");
const runSelectEl = document.querySelector("#run-select");
const summaryEl = document.querySelector("#summary");
const logEl = document.querySelector("#log");
const params = new URLSearchParams(window.location.search);
const taskFilter = params.get("task") || "";
let selectedRunID = params.get("run") || "";
let latestRuns = [];

runsEl.classList.add("responsive-picker");

document.addEventListener("click", async (event) => {
  const runButton = event.target.closest("[data-run-id]");
  if (runButton) {
    event.preventDefault();
    selectRun(runButton.dataset.runId, true);
  }

  const cancel = event.target.closest("[data-cancel]");
  if (cancel) {
    event.preventDefault();
    cancel.disabled = true;
    await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", { method: "POST" });
    await refresh();
  }
});

document.addEventListener("change", async (event) => {
  if (event.target === runSelectEl && runSelectEl.value) {
    await selectRun(runSelectEl.value, true);
  }
});

async function refresh() {
  const stateURL = taskFilter ? "/api/state?task=" + encodeURIComponent(taskFilter) : "/api/state";
  const response = await fetch(stateURL);
  const state = await response.json();
  latestRuns = state.runs || [];
  if (!latestRuns.length) {
    renderRuns(latestRuns);
    summaryEl.innerHTML = '<div class="empty">' + (taskFilter ? "No runs for this task yet." : "No runs yet.") + "</div>";
    logEl.textContent = taskFilter ? "No runs for this task yet." : "No runs yet.";
    return;
  }
  if (!selectedRunID || !latestRuns.some((run) => run.id === selectedRunID)) {
    selectedRunID = latestRuns[0].id;
    updateURL(false);
  }
  renderRuns(latestRuns);
  await renderSelectedRun();
}

function renderRuns(runs) {
  if (taskFilter) {
    runFilterEl.hidden = false;
    runFilterEl.textContent = "task " + taskFilter;
    clearRunFilterEl.hidden = false;
  } else {
    runFilterEl.hidden = true;
    runFilterEl.textContent = "";
    clearRunFilterEl.hidden = true;
  }
  runCountEl.textContent = runs.length + (runs.length === 1 ? " run" : " runs");
  if (!runs.length) {
    runPickerEl.hidden = true;
    runsEl.innerHTML = '<div class="empty">' + (taskFilter ? "No runs for this task yet." : "No runs yet.") + "</div>";
    return;
  }
  runPickerEl.hidden = false;
  runSelectEl.innerHTML = runs.map((run) => {
    return '<option value="' + escapeHTML(run.id) + '"' + (run.id === selectedRunID ? " selected" : "") + ">" +
      escapeHTML(run.task_name + " · " + run.status.toLowerCase() + " · request " + formatTime(run.requested_at) + " · elapsed " + formatElapsed(run)) +
      "</option>";
  }).join("");
  runsEl.innerHTML = runs.map((run) => {
    const active = run.id === selectedRunID ? " active" : "";
    return '<button class="run-item' + active + '" data-run-id="' + escapeHTML(run.id) + '">' +
      '<span class="run-title"><strong>' + escapeHTML(run.task_name) + '</strong><span class="meta">' + escapeHTML(run.id) + "</span></span>" +
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
  if (!selectedRunID) return;
  const [runResponse, logResponse] = await Promise.all([
    fetch("/api/runs/" + encodeURIComponent(selectedRunID)),
    fetch("/api/runs/" + encodeURIComponent(selectedRunID) + "/log"),
  ]);
  if (runResponse.ok) {
    const run = await runResponse.json();
    const canCancel = run.status === "QUEUED" || run.status === "RUNNING";
    const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">Cancel</button>' : "";
    summaryEl.innerHTML = '<div class="summary-head"><div class="summary-title"><h1>' + escapeHTML(run.task_name) + "</h1>" +
      '<div class="meta">' + escapeHTML(run.id) + "</div>" +
      renderRunParams(run) +
      "</div>" +
      '<div class="summary-actions">' + renderStatusBadge(run.status) + cancel + "</div></div>" +
      '<div class="kv"><span>Task <b>' + escapeHTML(run.task_id) + '</b></span><span>Exit <b>' + escapeHTML(run.exit_code) + "</b></span></div>" +
      "<code>" + escapeHTML(run.script) + "</code>" +
      renderTimes(run);
  }
  if (logResponse.ok) {
    const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
    logEl.textContent = await logResponse.text();
    if (atBottom) logEl.scrollTop = logEl.scrollHeight;
  }
}

function updateURL(push) {
  const next = new URLSearchParams();
  if (taskFilter) next.set("task", taskFilter);
  if (selectedRunID) next.set("run", selectedRunID);
  const url = "/runs" + (next.toString() ? "?" + next.toString() : "");
  if (push) {
    history.pushState(null, "", url);
  } else {
    history.replaceState(null, "", url);
  }
}

await initShell();
await refresh();
setInterval(refresh, 1500);
