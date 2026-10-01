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
import { selectRow } from "./forms.js";

const RETURN_KEY = "builda.runs.return";
const notice = document.getElementById("runs-status");
const list = document.getElementById("runs");
const summary = document.getElementById("summary");
const countNode = document.getElementById("run-count");
const filterHost = document.getElementById("run-filters");
const previousButton = document.getElementById("previous-runs");
const nextButton = document.getElementById("next-runs");
const pageNode = document.getElementById("run-page");
const logView = new LogView({
  pre: document.getElementById("log"),
  copyButton: document.getElementById("copy-log"),
  followButton: document.getElementById("follow-log"),
  wrapButton: document.getElementById("wrap-log"),
  updateNode: document.getElementById("log-update"),
  notice,
});

let runs = [];
let jobs = [];
let agents = [];
let projects = [];
let total = 0;
let pageSize = 200;
let requestGeneration = 0;
let loading = false;
let returnState = readReturnState();
if (returnState) window.history.scrollRestoration = "manual";
let selected = returnState?.selected || "";
let filters = readFiltersFromURL();
let offset = Number(new URLSearchParams(window.location.search).get("offset")) || 0;

function readReturnState() {
  try {
    const value = sessionStorage.getItem(RETURN_KEY);
    return value ? JSON.parse(value) : null;
  } catch {
    return null;
  }
}

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
          t("runs.active"),
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
    ) + '<button id="reset-run-filters" class="secondary" type="button" data-i18n="action.clearFilters">Clear filters</button>';
  applyTranslations(filterHost);
  filterHost.querySelectorAll("select").forEach((select) => {
    select.addEventListener("change", () => {
      filters[select.name] = select.value;
      offset = 0;
      updateURL();
      load(true);
    });
  });
  document.getElementById("reset-run-filters")?.addEventListener("click", () => {
    filters = { job: "", project: "", agent: "", status: "" };
    offset = 0;
    renderFilters();
    updateURL();
    load(true);
  });
}

function updateURL() {
  window.history.replaceState({}, "", "/runs" + query({ ...filters, offset: offset || "" }));
}

async function load(force = false) {
  if (loading && !force) return;
  loading = true;
  const generation = ++requestGeneration;
  try {
    const response = await getJSON("/api/runs" + query({ ...filters, limit: pageSize, offset }));
    if (generation !== requestGeneration) return;
    const responseTotal = Number(response.total) || 0;
    const responseLimit = Number(response.limit) || pageSize;
    if (offset >= responseTotal && offset > 0) {
      offset = responseTotal ? Math.floor((responseTotal - 1) / responseLimit) * responseLimit : 0;
      updateURL();
      return load(true);
    }
    runs = response.runs || [];
    total = responseTotal;
    pageSize = responseLimit;
    offset = Number(response.offset) || 0;
    countNode.textContent = t("runs.count", { count: total });
    renderPagination();
    if (!runs.some((run) => run.id === selected)) {
      selected = runs.some((run) => run.id === returnState?.selected)
        ? returnState.selected
        : (runs.length ? runs[0].id : "");
      logView.select(selected);
    }
    renderList();
    list.setAttribute("aria-busy", "false");
    renderSummary();
    if (returnState) {
      const restoreY = Number(returnState.scrollY) || 0;
      requestAnimationFrame(() => {
        window.scrollTo(0, restoreY);
        requestAnimationFrame(() => { window.history.scrollRestoration = "auto"; });
      });
      try { sessionStorage.removeItem(RETURN_KEY); } catch { /* storage may be unavailable */ }
      returnState = null;
    }
    if (selected) await logView.refresh();
    showNotice(notice, "");
  } catch (error) {
    if (generation === requestGeneration) {
      list.setAttribute("aria-busy", "false");
      showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
    }
  } finally {
    if (generation === requestGeneration) loading = false;
  }
}

function renderPagination() {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const page = Math.min(pages, Math.floor(offset / pageSize) + 1);
  if (pageNode) pageNode.textContent = t("runs.page", { page, pages });
  if (previousButton) previousButton.disabled = page <= 1;
  if (nextButton) nextButton.disabled = page >= pages;
}

function renderList() {
  if (!runs.length) {
    const html =
      '<div class="empty"><strong>' + escapeHTML(t("runs.emptyTitle")) + "</strong><span>" + escapeHTML(t("runs.emptyHint")) + "</span>" +
      (Object.values(filters).some(Boolean) ? '<button type="button" class="secondary compact" data-clear-filters>' + escapeHTML(t("action.clearFilters")) + "</button>" : "") +
      "</div>";
    if (list.innerHTML !== html) list.innerHTML = html;
    return;
  }
  const html = runs.map((run) =>
    '<button type="button" class="run' + (run.id === selected ? " active" : "") + '" data-run="' + escapeHTML(run.id) + '" title="' + escapeHTML(run.id) + '" aria-pressed="' + (run.id === selected) + '">' +
    '<span class="run-title"><span class="run-name">' + escapeHTML(run.job_name || run.job_id) + "</span>" +
    renderStatusBadge(run.status) + "</span>" +
    '<span class="run-meta-row">' + escapeHTML(run.id) + " · " + escapeHTML(run.agent_id || "-") + "</span>" +
    '<span class="param-list">' + renderChips(run.parameters) + "</span>" +
    renderRunTimes(run) +
    "</button>",
  ).join("");
  if (list.innerHTML === html) return;
  const scrollTop = list.scrollTop;
  const focusedRunID = list.contains(document.activeElement) ? document.activeElement.dataset.run : "";
  list.innerHTML = html;
  list.scrollTop = scrollTop;
  if (focusedRunID) {
    Array.from(list.querySelectorAll("[data-run]")).find((node) => node.dataset.run === focusedRunID)?.focus({ preventScroll: true });
  }
}

function renderSummary() {
  const run = runs.find((entry) => entry.id === selected);
  if (!run) {
    const html = '<div class="empty">' + escapeHTML(t("common.notSelected")) + "</div>";
    if (summary.innerHTML !== html) summary.innerHTML = html;
    return;
  }
  const attention = run.needs_attention
    ? '<div class="attention">' + escapeHTML(t("run.attention", { message: run.attention || "" })) +
      ' <button type="button" class="secondary compact" data-resolve data-focus-key="resolve">' + escapeHTML(t("action.resolve")) + "</button></div>"
    : "";
  const html =
    '<div class="summary-head"><div class="summary-title"><h2>' + escapeHTML(run.job_name || run.job_id) + "</h2>" +
    '<div class="meta identifier">' + escapeHTML(run.id) + "</div>" +
    '<div class="param-list" aria-label="' + escapeHTML(t("params.aria")) + '">' + renderChips(run.parameters) + "</div>" +
    '</div><div class="summary-actions">' + renderStatusBadge(run.status) +
    '<a class="button secondary compact" data-focus-key="detail" href="/runs/' + escapeHTML(run.id) + '">' + escapeHTML(t("action.viewRuns")) + "</a>" +
    '<button type="button" class="secondary compact" data-rerun data-focus-key="rerun">' + escapeHTML(t("action.rerun")) + "</button>" +
    (isActiveStatus(run.status)
      ? '<button type="button" class="danger compact" data-cancel data-focus-key="cancel">' + escapeHTML(t("action.cancel")) + "</button>"
      : '<button type="button" class="danger compact" data-delete data-focus-key="delete">' + escapeHTML(t("action.deleteRun")) + "</button>") +
    "</div></div>" + attention +
    '<div class="kv"><span>' + escapeHTML(t("field.agent")) + "</span><b>" + escapeHTML(run.agent_id || "-") + "</b>" +
    "<span>" + escapeHTML(t("field.timeout")) + "</span><b>" + escapeHTML(run.timeout_text || "-") + "</b>" +
    "<span>" + escapeHTML(t("time.request")) + "</span><b>" + escapeHTML(formatTime(run.requested_at)) + "</b>" +
    "<span>" + escapeHTML(t("time.finished")) + "</span><b>" + escapeHTML(formatTime(run.finished_at)) + "</b>" +
    (run.error ? "<span>" + escapeHTML(t("common.failed")) + "</span><b>" + escapeHTML(run.error) + "</b>" : "") +
    "</div>" +
    '<details class="script-details"><summary>' + escapeHTML(t("script.view")) + "</summary><code>" +
    escapeHTML(run.script || "") + "</code></details>";
  if (summary.innerHTML === html) return;
  const opened = summary.querySelector(".script-details")?.open;
  const focusedKey = summary.contains(document.activeElement) ? document.activeElement.dataset.focusKey : "";
  summary.innerHTML = html;
  const details = summary.querySelector(".script-details");
  if (details && opened) details.open = true;
  if (focusedKey) summary.querySelector('[data-focus-key="' + focusedKey + '"]')?.focus({ preventScroll: true });
  applyTranslations(summary);
}

list?.addEventListener("click", async (event) => {
  const clear = event.target.closest("[data-clear-filters]");
  if (clear) {
    filters = { job: "", project: "", agent: "", status: "" };
    offset = 0;
    renderFilters();
    updateURL();
    await load(true);
    return;
  }
  const button = event.target.closest("[data-run]");
  if (!button) return;
  selected = button.dataset.run;
  if (window.matchMedia("(max-width: 960px)").matches) {
    try {
      sessionStorage.setItem(RETURN_KEY, JSON.stringify({
        href: window.location.pathname + window.location.search,
        scrollY: window.scrollY,
        selected,
      }));
    } catch { /* navigation still works without session storage */ }
    window.location.href = "/runs/" + encodeURIComponent(selected);
    return;
  }
  logView.select(selected);
  renderList();
  renderSummary();
  try { await logView.refresh(); } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
});

summary?.addEventListener("click", async (event) => {
  if (!selected) return;
  const action = event.target.closest("[data-rerun], [data-cancel], [data-resolve], [data-delete]");
  if (!action || action.disabled) return;
  action.disabled = true;
  try {
    if (event.target.closest("[data-rerun]")) {
      const result = await postJSON("/api/runs/" + encodeURIComponent(selected) + "/rerun", {});
      showNotice(notice, t("notice.queued"), "ok");
      selected = result.run.id;
      logView.select(selected);
      await load(true);
      return;
    }
    if (event.target.closest("[data-cancel]")) {
      await postJSON("/api/runs/" + encodeURIComponent(selected) + "/cancel", {});
      await load(true);
      return;
    }
    if (event.target.closest("[data-resolve]")) {
      await postJSON("/api/runs/" + encodeURIComponent(selected) + "/resolve", {});
      await load(true);
      return;
    }
    if (event.target.closest("[data-delete]")) {
      if (!confirmAction(t("action.deleteRun") + "?")) return;
      await deleteJSON("/api/runs/" + encodeURIComponent(selected));
      selected = "";
      logView.select("");
      showNotice(notice, t("notice.deleted"), "ok");
      await load(true);
    }
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    if (action.isConnected) action.disabled = false;
  }
});

document.addEventListener("builda:localechange", () => {
  renderFilters();
  renderList();
  renderSummary();
  countNode.textContent = t("runs.count", { count: total });
  renderPagination();
  logView.localeChanged();
});

previousButton?.addEventListener("click", () => {
  offset = Math.max(0, offset - pageSize);
  updateURL();
  load(true);
});

nextButton?.addEventListener("click", () => {
  if (offset + pageSize >= total) return;
  offset += pageSize;
  updateURL();
  load(true);
});

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") load(true);
});

await initShell();
await loadOptions();
await load();
setInterval(() => {
  if (document.visibilityState === "visible") load();
}, 2000);
