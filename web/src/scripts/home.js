import {
  copyText,
  escapeHTML,
  flashButtonText,
  initShell,
  isActiveStatus,
  renderStatusBadge,
  renderTimes,
  showNotice,
  statusLabel,
  taskInputs,
  taskRunAPI,
  taskRunCurl,
} from "./shared.js";

const homeStatusEl = document.querySelector("#home-status");
const tasksEl = document.querySelector("#tasks");
const taskCountEl = document.querySelector("#task-count");
const runningCountEl = document.querySelector("#running-count");
const queuedCountEl = document.querySelector("#queued-count");
const latestStatusEl = document.querySelector("#latest-status");
const runsEl = document.querySelector("#runs");
const runCountEl = document.querySelector("#run-count");
const runModalEl = document.querySelector("#run-modal");
const runFormEl = document.querySelector("#run-form");
const runModalTitleEl = document.querySelector("#run-modal-title");
const runModalMetaEl = document.querySelector("#run-modal-meta");
const runModalFieldsEl = document.querySelector("#run-modal-fields");
const runModalSubmitEl = document.querySelector("#run-modal-submit");
const latestRunLimit = 10;
const expandedTasks = new Set();
let latestTasks = [];
let latestRuns = [];
let pendingTask = null;
let returnFocusEl = null;
let returnFocusTaskID = "";

document.addEventListener("click", async (event) => {
  const toggle = event.target.closest("[data-toggle-task]");
  if (toggle) {
    event.preventDefault();
    const taskID = toggle.dataset.toggleTask;
    if (expandedTasks.has(taskID)) {
      expandedTasks.delete(taskID);
    } else {
      expandedTasks.add(taskID);
    }
    renderTasks(latestTasks, latestRuns);
  }

  const start = event.target.closest("[data-start]");
  if (start) {
    event.preventDefault();
    const task = latestTasks.find((candidate) => candidate.ID === start.dataset.start);
    if (task && taskInputs(task).length) {
      openRunModal(task, start);
    } else {
      const taskName = task ? task.Name || task.ID : start.dataset.start;
      if (!confirm("작업 '" + taskName + "'을(를) 실행할까요?")) return;
      start.disabled = true;
      try {
        await runTask(start.dataset.start, {});
      } finally {
        start.disabled = false;
      }
    }
  }

  const closeModal = event.target.closest("[data-close-run-modal]");
  if (closeModal) {
    event.preventDefault();
    closeRunModal();
  }

  const copy = event.target.closest("[data-copy-api]");
  if (copy) {
    event.preventDefault();
    copy.disabled = true;
    try {
      await copyText(window.location.origin + copy.dataset.copyApi);
      flashButtonText(copy, "복사됨");
      showNotice(homeStatusEl, "API URL을 복사했습니다.", "ok");
    } catch (error) {
      flashButtonText(copy, "실패");
      showNotice(homeStatusEl, "API URL 복사에 실패했습니다: " + error.message, "error");
      console.error("copy failed", error);
    } finally {
      copy.disabled = false;
    }
  }

  const copyCurl = event.target.closest("[data-copy-curl]");
  if (copyCurl) {
    event.preventDefault();
    copyCurl.disabled = true;
    try {
      await copyText(copyCurl.dataset.copyCurl);
      flashButtonText(copyCurl, "복사됨");
      showNotice(homeStatusEl, "curl 명령을 복사했습니다.", "ok");
    } catch (error) {
      flashButtonText(copyCurl, "실패");
      showNotice(homeStatusEl, "curl 복사에 실패했습니다: " + error.message, "error");
      console.error("copy failed", error);
    } finally {
      copyCurl.disabled = false;
    }
  }

  const cancel = event.target.closest("[data-cancel]");
  if (cancel) {
    event.preventDefault();
    if (!confirm("이 실행을 취소할까요? 이미 시작된 스크립트가 중단될 수 있습니다.")) return;
    cancel.disabled = true;
    try {
      const response = await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", { method: "POST" });
      if (!response.ok) throw new Error(await response.text());
      showNotice(homeStatusEl, "취소 요청을 보냈습니다.", "ok");
      await refresh();
    } catch (error) {
      showNotice(homeStatusEl, "취소에 실패했습니다: " + error.message, "error");
    } finally {
      cancel.disabled = false;
    }
  }
});

runModalEl.addEventListener("click", (event) => {
  if (event.target === runModalEl) {
    closeRunModal();
  }
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !runModalEl.hidden) {
    closeRunModal();
    return;
  }
  if (event.key !== "Tab" || runModalEl.hidden) return;
  const focusable = Array.from(runFormEl.querySelectorAll("button, input, select, textarea, a[href]"))
    .filter((node) => !node.disabled && node.offsetParent !== null);
  if (!focusable.length) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
});

runFormEl.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!pendingTask) return;
  const values = Object.fromEntries(new FormData(runFormEl).entries());
  runModalSubmitEl.disabled = true;
  try {
    if (await runTask(pendingTask.ID, values)) {
      closeRunModal();
    }
  } finally {
    runModalSubmitEl.disabled = false;
  }
});

async function refresh() {
  try {
    const response = await fetch("/api/state");
    if (!response.ok) throw new Error(await response.text());
    const state = await response.json();
    latestTasks = state.tasks || [];
    latestRuns = state.runs || [];
    renderOverview(latestTasks, latestRuns);
    renderTasks(latestTasks, latestRuns);
    renderRuns(latestRuns);
  } catch (error) {
    showNotice(homeStatusEl, "상태를 불러오지 못했습니다: " + error.message, "error");
  }
}

async function runTask(taskID, values) {
  try {
    const response = await fetch(taskRunAPI(taskID, values), { method: "POST" });
    if (!response.ok) throw new Error(await response.text());
    const run = await response.json();
    showNotice(homeStatusEl, "실행이 큐에 등록되었습니다. 로그 화면으로 이동합니다.", "ok");
    window.location.href = "/runs?run=" + encodeURIComponent(run.id);
    return true;
  } catch (error) {
    showNotice(homeStatusEl, "실행에 실패했습니다: " + error.message, "error");
    return false;
  }
}

function renderOverview(tasks, runs) {
  const running = runs.filter((run) => run.status === "RUNNING").length;
  const queued = runs.filter((run) => run.status === "QUEUED").length;
  taskCountEl.textContent = String(tasks.length);
  runningCountEl.textContent = String(running);
  queuedCountEl.textContent = String(queued);
  latestStatusEl.textContent = runs.length ? statusLabel(runs[0].status) : "-";
}

function renderTasks(tasks, runs) {
  if (!tasks.length) {
    tasksEl.innerHTML = '<div class="empty">설정된 작업이 없습니다.</div>';
    return;
  }
  const latestByTask = new Map();
  runs.forEach((run) => {
    if (!latestByTask.has(run.task_id)) latestByTask.set(run.task_id, run);
  });
  tasksEl.innerHTML = tasks.map((task) => {
    const description = task.Description || task.ID;
    const latestRun = latestByTask.get(task.ID);
    const timeout = task.Timeout ? '<div class="detail-line"><span>Timeout</span><span>' + escapeHTML(task.Timeout) + "</span></div>" : "";
    const api = taskRunAPI(task.ID);
    const curl = taskRunCurl(task.ID);
    const inputDetails = renderTaskInputs(task);
    const isExpanded = expandedTasks.has(task.ID);
    const details = isExpanded ? '<div class="task-details">' +
      '<div class="task-description-full"><span>Description</span><span>' + escapeHTML(description) + "</span></div>" +
      '<div class="detail-line"><span>Task ID</span><span>' + escapeHTML(task.ID) + "</span></div>" +
      timeout +
      inputDetails +
      "<code>" + escapeHTML(task.script) + "</code>" +
      '<div class="api-row"><span>POST ' + escapeHTML(api) + '</span><button class="secondary compact" data-copy-api="' + escapeHTML(api) + '">URL 복사</button><button class="secondary compact" data-copy-curl="' + escapeHTML(curl) + '">curl 복사</button></div>' +
      '<div class="actions"><a class="button secondary" href="/runs?task=' + encodeURIComponent(task.ID) + '">이 작업 기록</a></div>' +
      "</div>" : "";
    const latest = latestRun ? '<div class="task-meta-row">' + renderStatusBadge(latestRun.status) +
      '<span class="meta">마지막 실행 ' + escapeHTML(latestRun.id) + '</span></div>' : '<div class="task-meta-row"><span class="chip">실행 기록 없음</span></div>';
    return '<article class="task">' +
      '<div class="row task-summary"><div class="task-copy"><span class="task-name">' + escapeHTML(task.Name || task.ID) + "</span>" +
      '<div class="meta task-description">' + escapeHTML(description) + "</div>" + latest + "</div>" +
      '<div class="task-actions"><button class="secondary icon-button detail-toggle" data-toggle-task="' + escapeHTML(task.ID) + '" aria-label="' + (isExpanded ? "상세 닫기" : "상세 보기") + '" title="' + (isExpanded ? "상세 닫기" : "상세 보기") + '" aria-expanded="' + String(isExpanded) + '">' +
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"></path></svg>' +
      '</button><button data-start="' + escapeHTML(task.ID) + '">실행</button></div></div>' +
      details +
      "</article>";
  }).join("");
}

function renderTaskInputs(task) {
  const inputs = taskInputs(task);
  if (!inputs.length) return "";
  return '<div class="input-list">' + inputs.map((input) => {
    const type = input.Type === "choice" ? "choice" : "string";
    const required = input.Required ? "required" : "optional";
    const options = type === "choice" ? " · options " + (input.Options || []).join(", ") : "";
    const description = input.Description ? "<span>" + escapeHTML(input.Description) + "</span>" : "";
    return '<div class="input-item">' +
      '<div class="input-title"><span>' + escapeHTML(input.Name || input.ID) + '</span><span class="input-meta">' + escapeHTML(required) + "</span></div>" +
      '<span class="input-meta">' + escapeHTML(input.ID + " · " + type + options + " · env " + inputEnvName(input.ID)) + "</span>" +
      description +
      "</div>";
  }).join("") + "</div>";
}

function openRunModal(task, trigger) {
  pendingTask = task;
  returnFocusEl = trigger || document.activeElement;
  returnFocusTaskID = task.ID;
  runModalTitleEl.textContent = (task.Name || task.ID) + " 실행";
  runModalMetaEl.textContent = "작업 ID " + task.ID + " · 입력값은 실행 기록에 저장됩니다.";
  runModalFieldsEl.innerHTML = taskInputs(task).map(renderRunInputField).join("");
  runModalEl.hidden = false;
  const first = runFormEl.querySelector("input, select");
  if (first) {
    first.focus();
  } else {
    runFormEl.focus();
  }
}

function closeRunModal() {
  pendingTask = null;
  runFormEl.reset();
  runModalEl.hidden = true;
  runModalFieldsEl.innerHTML = "";
  const fallbackFocusEl = returnFocusTaskID ? findStartButton(returnFocusTaskID) : null;
  if (returnFocusEl && returnFocusEl.isConnected) {
    returnFocusEl.focus();
  } else if (fallbackFocusEl) {
    fallbackFocusEl.focus();
  }
  returnFocusEl = null;
  returnFocusTaskID = "";
}

function renderRunInputField(input) {
  const id = "input-" + input.ID.replaceAll(/[^a-zA-Z0-9_-]/g, "-");
  const required = input.Required ? " required" : "";
  const description = input.Description ? '<div class="meta">' + escapeHTML(input.Description) + "</div>" : "";
  const label = '<label for="' + escapeHTML(id) + '">' + escapeHTML(input.Name || input.ID) + "</label>";
  if (input.Type === "choice") {
    const blank = '<option value="">' + (input.Required ? "선택하세요" : "선택 안 함") + "</option>";
    const options = blank + (input.Options || []).map((option) => {
      const selected = option === input.Default ? " selected" : "";
      return '<option value="' + escapeHTML(option) + '"' + selected + ">" + escapeHTML(option) + "</option>";
    }).join("");
    return '<div class="field">' + label + '<select id="' + escapeHTML(id) + '" name="' + escapeHTML(input.ID) + '"' + required + ">" + options + "</select>" + description + "</div>";
  }
  return '<div class="field">' + label + '<input id="' + escapeHTML(id) + '" name="' + escapeHTML(input.ID) + '" value="' + escapeHTML(input.Default || "") + '" autocomplete="off"' + required + ">" + description + "</div>";
}

function inputEnvName(inputID) {
  return "BUILDA_INPUT_" + String(inputID).replaceAll("-", "_").toUpperCase();
}

function findStartButton(taskID) {
  return Array.from(document.querySelectorAll("[data-start]"))
    .find((button) => button.dataset.start === taskID) || null;
}

function renderRuns(runs) {
  const latestRuns = runs.slice(0, latestRunLimit);
  runCountEl.textContent = latestRuns.length + " latest";
  if (!latestRuns.length) {
    runsEl.innerHTML = '<div class="empty">아직 실행 기록이 없습니다.</div>';
    return;
  }
  runsEl.innerHTML = latestRuns.map((run) => {
    const canCancel = isActiveStatus(run.status);
    const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">취소</button>' : "";
    return '<article class="run">' +
      '<div class="row"><div class="run-title"><span class="run-name">' + escapeHTML(run.task_name) + '</span><div class="meta">' + escapeHTML(run.id) + "</div></div>" +
      renderStatusBadge(run.status) + "</div>" +
      renderTimes(run) +
      '<div class="actions"><a class="button secondary" href="/runs?run=' + encodeURIComponent(run.id) + '">로그 확인</a>' + cancel + "</div>" +
      "</article>";
  }).join("");
}

await initShell();
await refresh();
setInterval(refresh, 1500);
