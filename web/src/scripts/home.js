import {
  copyText,
  escapeHTML,
  flashButtonText,
  initShell,
  renderStatusBadge,
  renderTimes,
  taskInputs,
  taskRunAPI,
} from "./shared.js";

const tasksEl = document.querySelector("#tasks");
const taskCountEl = document.querySelector("#task-count");
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
let pendingTask = null;

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
    renderTasks(latestTasks);
  }

  const start = event.target.closest("[data-start]");
  if (start) {
    event.preventDefault();
    const task = latestTasks.find((candidate) => candidate.ID === start.dataset.start);
    if (task && taskInputs(task).length) {
      openRunModal(task);
    } else {
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
      flashButtonText(copy, "Copied");
    } catch (error) {
      flashButtonText(copy, "Failed");
      console.error("copy failed", error);
    } finally {
      copy.disabled = false;
    }
  }

  const cancel = event.target.closest("[data-cancel]");
  if (cancel) {
    event.preventDefault();
    await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", { method: "POST" });
    await refresh();
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
  const response = await fetch("/api/state");
  const state = await response.json();
  latestTasks = state.tasks || [];
  renderTasks(state.tasks || []);
  renderRuns(state.runs || []);
}

async function runTask(taskID, values) {
  try {
    const response = await fetch(taskRunAPI(taskID, values), { method: "POST" });
    if (!response.ok) throw new Error(await response.text());
    await refresh();
    return true;
  } catch (error) {
    alert(error.message);
    return false;
  }
}

function renderTasks(tasks) {
  taskCountEl.textContent = tasks.length + (tasks.length === 1 ? " task" : " tasks");
  if (!tasks.length) {
    tasksEl.innerHTML = '<div class="empty">No tasks configured.</div>';
    return;
  }
  tasksEl.innerHTML = tasks.map((task) => {
    const description = task.Description || task.ID;
    const timeout = task.Timeout ? '<div class="detail-line"><span>Timeout</span><span>' + escapeHTML(task.Timeout) + "</span></div>" : "";
    const api = taskRunAPI(task.ID);
    const inputDetails = renderTaskInputs(task);
    const isExpanded = expandedTasks.has(task.ID);
    const details = isExpanded ? '<div class="task-details">' +
      '<div class="task-description-full"><span>Description</span><span>' + escapeHTML(description) + "</span></div>" +
      '<div class="detail-line"><span>Task ID</span><span>' + escapeHTML(task.ID) + "</span></div>" +
      timeout +
      inputDetails +
      "<code>" + escapeHTML(task.Command) + "</code>" +
      '<div class="api-row"><span>POST ' + escapeHTML(api) + '</span><button class="secondary" data-copy-api="' + escapeHTML(api) + '">Copy</button></div>' +
      '<div class="actions"><a class="button secondary" href="/runs?task=' + encodeURIComponent(task.ID) + '">View runs</a></div>' +
      "</div>" : "";
    return '<article class="task">' +
      '<div class="row task-summary"><div class="task-copy"><strong>' + escapeHTML(task.Name || task.ID) + "</strong>" +
      '<div class="meta task-description">' + escapeHTML(description) + "</div></div>" +
      '<div class="task-actions"><button class="secondary icon-button detail-toggle" data-toggle-task="' + escapeHTML(task.ID) + '" aria-label="' + (isExpanded ? "Hide details" : "Show details") + '" title="' + (isExpanded ? "Hide details" : "Show details") + '" aria-expanded="' + String(isExpanded) + '">' +
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"></path></svg>' +
      '</button><button data-start="' + escapeHTML(task.ID) + '">Run</button></div></div>' +
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

function openRunModal(task) {
  pendingTask = task;
  runModalTitleEl.textContent = "Run " + (task.Name || task.ID);
  runModalMetaEl.textContent = task.ID;
  runModalFieldsEl.innerHTML = taskInputs(task).map(renderRunInputField).join("");
  runModalEl.hidden = false;
  const first = runFormEl.querySelector("input, select");
  if (first) first.focus();
}

function closeRunModal() {
  pendingTask = null;
  runFormEl.reset();
  runModalEl.hidden = true;
  runModalFieldsEl.innerHTML = "";
}

function renderRunInputField(input) {
  const id = "input-" + input.ID.replaceAll(/[^a-zA-Z0-9_-]/g, "-");
  const required = input.Required ? " required" : "";
  const description = input.Description ? '<div class="meta">' + escapeHTML(input.Description) + "</div>" : "";
  const label = '<label for="' + escapeHTML(id) + '">' + escapeHTML(input.Name || input.ID) + "</label>";
  if (input.Type === "choice") {
    const blank = !input.Required && !input.Default ? '<option value=""></option>' : "";
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

function renderRuns(runs) {
  const latestRuns = runs.slice(0, latestRunLimit);
  runCountEl.textContent = latestRuns.length + " latest";
  if (!latestRuns.length) {
    runsEl.innerHTML = '<div class="empty">No runs yet.</div>';
    return;
  }
  runsEl.innerHTML = latestRuns.map((run) => {
    const canCancel = run.status === "QUEUED" || run.status === "RUNNING";
    const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">Cancel</button>' : "";
    return '<article class="run">' +
      '<div class="row"><div><strong>' + escapeHTML(run.task_name) + '</strong><div class="meta">' + escapeHTML(run.id) + "</div></div>" +
      renderStatusBadge(run.status) + "</div>" +
      "<code>" + escapeHTML(run.command) + "</code>" +
      renderTimes(run) +
      '<div class="actions"><a class="button secondary" href="/runs?run=' + encodeURIComponent(run.id) + '">View log</a>' + cancel + "</div>" +
      "</article>";
  }).join("");
}

await initShell();
await refresh();
setInterval(refresh, 1500);
