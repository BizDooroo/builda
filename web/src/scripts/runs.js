import { deleteJSON, getJSON, postJSON, query } from "./api.js";
import {
  escapeHTML,
  formatTime,
  isActiveStatus,
  renderChips,
  renderRunTimes,
  renderStatusBadge,
  showNotice,
} from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { confirmAction, initShell } from "./shell.js";
import { LogView } from "./logview.js";
import { selectRow, textRow } from "./forms.js";

const notice = document.getElementById("runs-status");
const list = document.getElementById("runs");
const summary = document.getElementById("summary");
const countNode = document.getElementById("run-count");
const filterHost = document.getElementById("run-filters");

const logView = new LogView({
  pre: document.getElementById("log"),
  copyButton: document.getElementById("copy-log"),
  followButton: document.getElementById("follow-log"),
  notice,
});

let runs = [];
let jobs = [];
let agents = [];
let projects = [];
let selected = "";
let filters = readFiltersFromURL();

function readFiltersFromURL() {
  const params = new URLSearchParams(window.location.search);
  return {
    job: params.get("job") || "",
    project: params.get("project") || "",
    agent: params.get("agent") || "",
    status: params.get("status") || "",
  };
}

async function loadOptions() {
  try {
    const [jobResponse, agentResponse, catalogResponse] = await Promise.all([
      getJSON("/api/jobs"),
      getJSON("/api/agents"),
      getJSON("/api/catalogs"),
    ]);
    jobs = jobResponse.jobs || [];
    agents = agentResponse.agents || [];
    const values = new Set();
    (catalogResponse.catalogs || []).forEach((catalog) => {
      (catalog.options || []).forEach((option) => values.add(option.value));
    });
    projects = Array.from(values).sort();
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
  renderFilters();
}

function choicesFrom(values, labels) {
  return [{ value: "", label: t("filter.all") }].concat(
    values.map((value, index) => ({ value, label: labels ? labels[index] : value })),
  );
}

function renderFilters() {
  filterHost.innerHTML =
    selectRow("job", "field.job", filters.job, choicesFrom(jobs.map((job) => job.id), jobs.map((job) => job.name || job.id))) +
    selectRow("project", "field.project", filters.project, choicesFrom(projects)) +
    selectRow("agent", "field.agent", filters.agent, choicesFrom(agents.map((agent) => agent.id))) +
    selectRow(
      "status",
      "field.status",
      filters.status,
      choicesFrom(
        ["ACTIVE", "TERMINAL", "QUEUED", "ASSIGNED", "RUNNING", "CANCELING", "SUCCESS", "FAILED", "CANCELED", "ABORTED"],
        [
          t("status.RUNNING") + "/" + t("status.QUEUED"),
          t("time.finished"),
          t("status.QUEUED"),
          t("status.ASSIGNED"),
          t("status.RUNNING"),
          t("status.CANCELING"),
          t("status.SUCCESS"),
          t("status.FAILED"),
          t("status.CANCELED"),
          t("status.ABORTED"),
        ],
      ),
    );
  applyTranslations(filterHost);
  filterHost.querySelectorAll("select").forEach((select) => {
    select.addEventListener("change", () => {
      filters[select.name] = select.value;
      const url = "/runs" + query(filters);
      window.history.replaceState({}, "", url);
      load();
    });
  });
}

async function load() {
  try {
    const response = await getJSON("/api/runs" + query({ ...filters, limit: 200 }));
    runs = response.runs || [];
    countNode.textContent = response.total + " runs";
    if (!runs.some((run) => run.id === selected)) {
      selected = runs.length ? runs[0].id : "";
      logView.select(selected);
    }
    renderList();
    renderSummary();
    if (selected) await logView.refresh();
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

function renderList() {
  if (!runs.length) {
    list.innerHTML = '<div class="empty">' + escapeHTML(t("common.none")) + "</div>";
    return;
  }
  list.innerHTML = runs
    .map((run) => {
      return (
        '<button type="button" class="run' + (run.id === selected ? " active" : "") + '" data-run="' + escapeHTML(run.id) + '">' +
        '<span class="run-title"><span class="run-name">' + escapeHTML(run.job_name || run.job_id) + "</span>" +
        renderStatusBadge(run.status) + "</span>" +
        '<span class="run-meta-row">' + escapeHTML(run.id) + " · " + escapeHTML(run.agent_id || "-") + "</span>" +
        '<span class="param-list">' + renderChips(run.parameters) + "</span>" +
        renderRunTimes(run) +
        "</button>"
      );
    })
    .join("");
}

function renderSummary() {
  const run = runs.find((entry) => entry.id === selected);
  if (!run) {
    summary.innerHTML = '<div class="empty">' + escapeHTML(t("common.notSelected")) + "</div>";
    return;
  }
  const attention = run.needs_attention
    ? '<div class="attention">' + escapeHTML(t("run.attention", { message: run.attention || "" })) +
      ' <button type="button" class="secondary compact" data-resolve>' + escapeHTML(t("action.resolve")) + "</button></div>"
    : "";
  summary.innerHTML =
    '<div class="summary-head"><div class="summary-title"><h2>' + escapeHTML(run.job_name || run.job_id) + "</h2>" +
    '<div class="meta">' + escapeHTML(run.id) + "</div>" +
    '<div class="param-list" aria-label="' + escapeHTML(t("params.aria")) + '">' + renderChips(run.parameters) + "</div>" +
    '</div><div class="summary-actions">' + renderStatusBadge(run.status) +
    '<a class="button secondary compact" href="/runs/' + escapeHTML(run.id) + '">' + escapeHTML(t("action.viewRuns")) + "</a>" +
    '<button type="button" class="secondary compact" data-rerun>' + escapeHTML(t("action.rerun")) + "</button>" +
    (isActiveStatus(run.status)
      ? '<button type="button" class="danger compact" data-cancel>' + escapeHTML(t("action.cancel")) + "</button>"
      : '<button type="button" class="danger compact" data-delete>' + escapeHTML(t("action.deleteRun")) + "</button>") +
    "</div></div>" + attention +
    '<div class="kv"><span>' + escapeHTML(t("field.agent")) + "</span><b>" + escapeHTML(run.agent_id || "-") + "</b>" +
    "<span>" + escapeHTML(t("field.timeout")) + "</span><b>" + escapeHTML(run.timeout_text || "-") + "</b>" +
    "<span>" + escapeHTML(t("time.request")) + "</span><b>" + escapeHTML(formatTime(run.requested_at)) + "</b>" +
    "<span>" + escapeHTML(t("time.finished")) + "</span><b>" + escapeHTML(formatTime(run.finished_at)) + "</b>" +
    (run.error ? "<span>" + escapeHTML(t("common.failed")) + "</span><b>" + escapeHTML(run.error) + "</b>" : "") +
    "</div>" +
    '<details class="script-details"><summary>' + escapeHTML(t("script.view")) + "</summary><code>" +
    escapeHTML(run.script || "") + "</code></details>";
  applyTranslations(summary);
}

list?.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-run]");
  if (!button) return;
  selected = button.dataset.run;
  logView.select(selected);
  renderList();
  renderSummary();
  await logView.refresh();
});

summary?.addEventListener("click", async (event) => {
  if (!selected) return;
  try {
    if (event.target.closest("[data-rerun]")) {
      const result = await postJSON("/api/runs/" + encodeURIComponent(selected) + "/rerun", {});
      showNotice(notice, t("notice.queued"), "ok");
      selected = result.run.id;
      logView.select(selected);
      await load();
      return;
    }
    if (event.target.closest("[data-cancel]")) {
      await postJSON("/api/runs/" + encodeURIComponent(selected) + "/cancel", {});
      await load();
      return;
    }
    if (event.target.closest("[data-resolve]")) {
      await postJSON("/api/runs/" + encodeURIComponent(selected) + "/resolve", {});
      await load();
      return;
    }
    if (event.target.closest("[data-delete]")) {
      if (!confirmAction(t("action.deleteRun") + "?")) return;
      await deleteJSON("/api/runs/" + encodeURIComponent(selected));
      selected = "";
      logView.select("");
      showNotice(notice, t("notice.deleted"), "ok");
      await load();
    }
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

document.addEventListener("builda:localechange", () => {
  renderFilters();
  renderList();
  renderSummary();
});

await initShell();
await loadOptions();
await load();
setInterval(load, 2000);
