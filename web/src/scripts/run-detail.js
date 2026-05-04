import {
  copyText,
  escapeHTML,
  flashButtonText,
  formatTime,
  initShell,
  isActiveStatus,
  renderLogText,
  renderRunParamChips,
  showNotice,
  statusLabel,
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
const followLogEl = document.querySelector("#follow-log");
const requestedEl = document.querySelector("#requested");
const startedEl = document.querySelector("#started");
const finishedEl = document.querySelector("#finished");
const canceledEl = document.querySelector("#canceled");
const paramsEl = document.querySelector("#params");
let timer = 0;
let followLog = true;
let currentLogText = logEl.textContent;

cancelRunEl.addEventListener("click", async () => {
  if (!confirm("이 실행을 취소할까요? 이미 시작된 스크립트가 중단될 수 있습니다.")) return;
  cancelRunEl.disabled = true;
  try {
    const response = await fetch("/api/runs/" + encodeURIComponent(runID) + "/cancel", { method: "POST" });
    if (!response.ok) throw new Error(await response.text());
    showNotice(runStatusEl, "취소 요청을 보냈습니다.", "ok");
    await refresh();
  } catch (error) {
    showNotice(runStatusEl, "취소에 실패했습니다: " + error.message, "error");
  } finally {
    cancelRunEl.disabled = false;
  }
});

copyLogEl.addEventListener("click", async () => {
  copyLogEl.disabled = true;
  try {
    await copyText(currentLogText);
    flashButtonText(copyLogEl, "복사됨");
    showNotice(runStatusEl, "로그를 복사했습니다.", "ok");
  } catch (error) {
    flashButtonText(copyLogEl, "실패");
    showNotice(runStatusEl, "로그 복사에 실패했습니다: " + error.message, "error");
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

async function refresh() {
  try {
    const [runResponse, logResponse] = await Promise.all([
      fetch("/api/runs/" + encodeURIComponent(runID)),
      fetch("/api/runs/" + encodeURIComponent(runID) + "/log"),
    ]);
    if (!runResponse.ok) throw new Error(await runResponse.text());
    const run = await runResponse.json();
    document.title = run.task_name + " · Builda";
    titleEl.textContent = run.task_name;
    runIDEl.textContent = run.id;
    scriptEl.textContent = run.script;
    badgeEl.textContent = statusLabel(run.status);
    badgeEl.className = "badge status-" + escapeHTML(run.status);
    cancelRunEl.hidden = !isActiveStatus(run.status);
    requestedEl.textContent = "request " + formatTime(run.requested_at);
    startedEl.textContent = "start " + formatTime(run.started_at);
    finishedEl.textContent = "finished " + formatTime(run.finished_at);
    canceledEl.textContent = "cancelled " + formatTime(run.canceled_at);
    const paramChips = renderRunParamChips(run.inputs);
    if (paramChips) {
      paramsEl.hidden = false;
      paramsEl.innerHTML = paramChips;
    } else {
      paramsEl.hidden = true;
      paramsEl.innerHTML = "";
    }
    if (run.status !== "QUEUED" && run.status !== "RUNNING" && timer) {
      clearInterval(timer);
      timer = 0;
    }
    if (!logResponse.ok) throw new Error(await logResponse.text());
    const shouldFollow = followLog || logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
    currentLogText = await logResponse.text();
    logEl.innerHTML = renderLogText(currentLogText);
    if (shouldFollow) logEl.scrollTop = logEl.scrollHeight;
  } catch (error) {
    showNotice(runStatusEl, "실행 정보를 불러오지 못했습니다: " + error.message, "error");
  }
}

function renderFollowButton() {
  followLogEl.classList.toggle("active", followLog);
  followLogEl.setAttribute("aria-pressed", String(followLog));
  followLogEl.textContent = followLog ? "Follow on" : "Follow off";
}

await initShell();
renderFollowButton();
await refresh();
timer = setInterval(refresh, 1000);
