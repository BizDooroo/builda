import { escapeHTML, formatRunParams, formatTime, initShell } from "./shared.js";

const runID = decodeURIComponent(window.location.pathname.replace(/^\/runs\//, "").replace(/\/$/, ""));
const titleEl = document.querySelector("#run-title");
const runIDEl = document.querySelector("#run-id");
const commandEl = document.querySelector("#command");
const logEl = document.querySelector("#log");
const badgeEl = document.querySelector("#badge");
const requestedEl = document.querySelector("#requested");
const startedEl = document.querySelector("#started");
const finishedEl = document.querySelector("#finished");
const canceledEl = document.querySelector("#canceled");
const paramsEl = document.querySelector("#params");
let timer = 0;

async function refresh() {
  const [runResponse, logResponse] = await Promise.all([
    fetch("/api/runs/" + encodeURIComponent(runID)),
    fetch("/api/runs/" + encodeURIComponent(runID) + "/log"),
  ]);
  if (runResponse.ok) {
    const run = await runResponse.json();
    document.title = run.task_name + " · Builda";
    titleEl.textContent = run.task_name;
    runIDEl.textContent = run.id;
    commandEl.textContent = run.command;
    badgeEl.textContent = String(run.status || "").toLowerCase();
    badgeEl.className = "badge status-" + escapeHTML(run.status);
    requestedEl.textContent = "request " + formatTime(run.requested_at);
    startedEl.textContent = "start " + formatTime(run.started_at);
    finishedEl.textContent = "finished " + formatTime(run.finished_at);
    canceledEl.textContent = "cancelled " + formatTime(run.canceled_at);
    const formattedParams = formatRunParams(run.inputs);
    if (formattedParams) {
      paramsEl.hidden = false;
      paramsEl.textContent = "params " + formattedParams;
    } else {
      paramsEl.hidden = true;
      paramsEl.textContent = "";
    }
    if (run.status !== "QUEUED" && run.status !== "RUNNING" && timer) {
      clearInterval(timer);
      timer = 0;
    }
  }
  if (logResponse.ok) {
    const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
    logEl.textContent = await logResponse.text();
    if (atBottom) logEl.scrollTop = logEl.scrollHeight;
  }
}

await initShell();
await refresh();
timer = setInterval(refresh, 1000);
