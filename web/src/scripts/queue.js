import { getJSON, postJSON } from "./api.js";
import { escapeHTML, formatTime, renderChips, renderStatusBadge, showNotice } from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { initShell, mountRecords } from "./shell.js";

const notice = document.getElementById("queue-status");
const list = document.getElementById("queue");
const active = document.getElementById("active");
const cancelSelected = document.getElementById("cancel-selected");
const selection = new Set();

async function load() {
  try {
    const response = await getJSON("/api/queue");
    render(response.queue || [], response.active || []);
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

function reasonLabel(reason) {
  const label = t("reason." + reason);
  return label.startsWith("reason.") ? reason : label;
}

function render(queue, running) {
  const present = new Set(queue.map((entry) => entry.run.id));
  selection.forEach((id) => {
    if (!present.has(id)) selection.delete(id);
  });

  mountRecords(list, queue, (entry) => {
    const run = entry.run;
    const checked = selection.has(run.id) ? " checked" : "";
    const readyClass = entry.reason === "ready" ? " ready" : "";
    return (
      '<div class="queue-entry' + (selection.has(run.id) ? " selected" : "") + '">' +
      '<div class="record-head"><div class="record-title">' +
      '<label class="form-check"><input type="checkbox" data-select="' + escapeHTML(run.id) + '"' + checked + " /> " +
      '<span class="queue-position">#' + entry.position + "</span> <strong>" +
      escapeHTML(run.job_name || run.job_id) + "</strong></label>" +
      '<div class="meta">' + escapeHTML(run.id) + " · " + escapeHTML(formatTime(run.requested_at)) + "</div>" +
      "</div><div class=\"record-actions\">" +
      '<span class="reason' + readyClass + '">' + escapeHTML(t("queue.reason")) + ": " + escapeHTML(reasonLabel(entry.reason)) + "</span>" +
      '<a class="button secondary compact" href="/runs/' + escapeHTML(run.id) + '">' + escapeHTML(t("action.viewRuns")) + "</a>" +
      '<button type="button" class="danger compact" data-cancel="' + escapeHTML(run.id) + '">' + escapeHTML(t("action.cancel")) + "</button>" +
      "</div></div>" +
      '<div class="param-list" aria-label="' + escapeHTML(t("params.aria")) + '">' + renderChips(entry.parameters) + "</div>" +
      '<div class="record-grid"><span>' + escapeHTML(t("jobs.eligible")) + ": " +
      escapeHTML((entry.eligible_agents || []).join(", ") || "-") + "</span></div>" +
      "</div>"
    );
  });

  mountRecords(active, running, (run) => {
    const attention = run.needs_attention
      ? '<div class="attention">' + escapeHTML(t("run.attention", { message: run.attention || "" })) + "</div>"
      : "";
    return (
      '<div class="queue-entry">' +
      '<div class="record-head"><div class="record-title"><strong>' +
      escapeHTML(run.job_name || run.job_id) + "</strong>" +
      '<div class="meta">' + escapeHTML(run.id) + " · " + escapeHTML(run.agent_id || "-") + "</div></div>" +
      '<div class="record-actions">' + renderStatusBadge(run.status) +
      '<a class="button secondary compact" href="/runs/' + escapeHTML(run.id) + '">' + escapeHTML(t("log.heading")) + "</a>" +
      '<button type="button" class="danger compact" data-cancel="' + escapeHTML(run.id) + '">' + escapeHTML(t("action.cancel")) + "</button>" +
      "</div></div>" +
      '<div class="param-list" aria-label="' + escapeHTML(t("params.aria")) + '">' + renderChips(run.parameters) + "</div>" +
      attention +
      "</div>"
    );
  });

  cancelSelected.disabled = selection.size === 0;
  cancelSelected.textContent = t("action.cancelSelected") + (selection.size ? " (" + selection.size + ")" : "");
  applyTranslations(list);
  applyTranslations(active);
}

async function cancelOne(id) {
  try {
    await postJSON("/api/runs/" + encodeURIComponent(id) + "/cancel", {});
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
}

document.addEventListener("change", (event) => {
  const box = event.target.closest("[data-select]");
  if (!box) return;
  if (box.checked) {
    selection.add(box.dataset.select);
  } else {
    selection.delete(box.dataset.select);
  }
  cancelSelected.disabled = selection.size === 0;
  cancelSelected.textContent = t("action.cancelSelected") + (selection.size ? " (" + selection.size + ")" : "");
  box.closest(".queue-entry")?.classList.toggle("selected", box.checked);
});

document.addEventListener("click", async (event) => {
  const cancel = event.target.closest("[data-cancel]");
  if (cancel) {
    await cancelOne(cancel.dataset.cancel);
  }
});

cancelSelected?.addEventListener("click", async () => {
  if (!selection.size) return;
  try {
    await postJSON("/api/queue/cancel", { ids: Array.from(selection) });
    selection.clear();
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

document.getElementById("refresh")?.addEventListener("click", load);

await initShell();
await load();
setInterval(load, 2000);
