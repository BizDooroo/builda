import { deleteJSON, getJSON, postJSON, putJSON, query } from "./api.js";
import { escapeHTML, renderLabels, showNotice } from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { confirmAction, initShell, mountRecords } from "./shell.js";
import {
  checkRow,
  formValues,
  listRow,
  parseList,
  repeaterItem,
  selectRow,
  textRow,
  textareaRow,
  typeChoices,
} from "./forms.js";

const notice = document.getElementById("jobs-status");
const list = document.getElementById("jobs");
const editor = document.getElementById("job-editor");
const editorForm = document.getElementById("job-form");
const editorTitle = document.getElementById("job-editor-title");
const parameterHost = document.getElementById("job-parameters");
const runModal = document.getElementById("run-modal");
const runForm = document.getElementById("run-form");
const runFields = document.getElementById("run-modal-fields");
const runTitle = document.getElementById("run-modal-title");
const runPreview = document.getElementById("run-modal-preview");
const runModalStatus = document.getElementById("run-modal-status");

let jobs = [];
let catalogs = [];
let editing = null;
let draftParameters = [];
let runningJob = null;
let modalOpener = null;
let modalScrollY = 0;
let modalBodyStyle = null;
let modalInertState = [];
let runSubmitting = false;
let savingJob = false;

async function load() {
  try {
    const [jobResponse, catalogResponse] = await Promise.all([getJSON("/api/jobs"), getJSON("/api/catalogs")]);
    jobs = jobResponse.jobs || [];
    catalogs = catalogResponse.catalogs || [];
    render();
  } catch (error) {
    list?.setAttribute("aria-busy", "false");
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

function render() {
  mountRecords(list, jobs, (job) => {
    const agents = (job.eligible_agents || [])
      .map((agent) => {
        const state = agent.online ? (agent.busy ? t("common.busy") : t("common.online")) : t("common.offline");
        return '<span class="chip">' + escapeHTML(agent.id + " · " + state) + "</span>";
      })
      .join("");
    const warning = job.runnable ? "" : '<div class="hint">' + escapeHTML(t("jobs.noAgent")) + "</div>";
    return (
      '<div class="record" data-job="' + escapeHTML(job.id) + '">' +
      '<div class="record-head"><div class="record-title"><strong>' + escapeHTML(job.name || job.id) + "</strong>" +
      '<div class="meta"><span class="identifier">' + escapeHTML(job.id) + "</span>" + (job.enabled ? "" : " · " + escapeHTML(t("common.disabled"))) + "</div>" +
      (job.description ? '<div class="meta">' + escapeHTML(job.description) + "</div>" : "") +
      "</div><div class=\"record-actions\">" +
      '<button type="button" data-run="' + escapeHTML(job.id) + '"' + (job.enabled ? "" : " disabled") + ">" + escapeHTML(t("action.execute")) + "</button>" +
      '<button type="button" class="secondary compact" data-edit="' + escapeHTML(job.id) + '">' + escapeHTML(t("action.edit")) + "</button>" +
      '<details class="action-menu"><summary data-i18n="action.more">More</summary><div class="action-menu-items">' +
      '<button type="button" class="secondary compact" data-toggle="' + escapeHTML(job.id) + '">' +
      escapeHTML(job.enabled ? t("common.disable") : t("common.enable")) + "</button>" +
      '<a class="button secondary compact" href="/runs' + query({ job: job.id }) + '">' + escapeHTML(t("action.viewRuns")) + "</a>" +
      '<button type="button" class="danger compact" data-delete="' + escapeHTML(job.id) + '">' + escapeHTML(t("action.delete")) + "</button>" +
      "</div></details>" +
      "</div></div>" +
      '<div class="record-grid">' +
      "<span>" + escapeHTML(t("field.labels")) + ": " + (renderLabels(job.labels) || "-") + "</span>" +
      "<span>" + escapeHTML(t("field.timeout")) + ": " + escapeHTML(job.timeout || "-") + "</span>" +
      "<span>" + escapeHTML(t("field.parameters")) + ": " + (job.parameters || []).length + "</span>" +
      "<span>" + escapeHTML(t("jobs.eligible")) + ": " + (agents || "-") + "</span>" +
      "</div>" + warning +
      '<details class="script-details"><summary>' + escapeHTML(t("script.view")) + "</summary><code>" +
      escapeHTML(job.script || "") + "</code></details>" +
      "</div>"
    );
  });
  applyTranslations(list);
}

// parameterPayload strips the view-only fields the API rejects. For a
// catalog-backed parameter the resolved option list must never be sent back,
// or the parameter would turn into a frozen inline list.
function parameterPayload(param) {
  const payload = {
    id: param.id,
    name: param.name || "",
    description: param.description || "",
    type: param.type || "string",
    default: param.default || "",
    required: Boolean(param.required),
  };
  if (param.type === "choice" && param.catalog) {
    payload.catalog = param.catalog;
    if (param.catalog_labels && param.catalog_labels.length) {
      payload.catalog_labels = param.catalog_labels;
    }
  } else if (param.type === "choice" && param.options && param.options.length) {
    payload.options = param.options.map((option) => ({
      value: option.value,
      label: option.label || "",
      labels: option.labels || [],
      values: option.values || undefined,
    }));
  }
  return payload;
}

// jobPayload rebuilds a job document from a job view.
function jobPayload(job, overrides = {}) {
  return {
    id: job.id,
    name: job.name || "",
    description: job.description || "",
    labels: job.labels || [],
    timeout: job.timeout || "",
    script: job.script || "",
    workdir_param: job.workdir_param || "",
    enabled: job.enabled !== false,
    parameters: (job.parameters || []).map(parameterPayload),
    ...overrides,
  };
}

function catalogChoices() {
  return [{ value: "", label: t("common.none") }].concat(
    catalogs.map((catalog) => ({ value: catalog.id, label: catalog.name || catalog.id })),
  );
}

function renderParameters() {
  parameterHost.innerHTML = draftParameters
    .map((param, index) => {
      const prefix = "param-" + index + "-";
      const body =
        '<div class="form-columns">' +
        textRow(prefix + "id", "field.id", param.id) +
        textRow(prefix + "name", "field.name", param.name) +
        textRow(prefix + "description", "field.description", param.description) +
        selectRow(prefix + "type", "field.type", param.type || "string", typeChoices()) +
        textRow(prefix + "default", "field.default", param.default) +
        selectRow(prefix + "catalog", "field.catalog", param.catalog, catalogChoices()) +
        listRow(prefix + "catalogLabels", "field.catalogLabels", param.catalog_labels, {
          hintKey: "hint.catalogLabels",
          placeholder: "android",
        }) +
        "</div>" +
        textRow(
          prefix + "options",
          "field.options",
          param.catalog ? "" : (param.options || []).map((option) => option.value).join(", "),
          { placeholder: "debug, release" },
        ) +
        checkRow(prefix + "required", "field.required", Boolean(param.required));
      return repeaterItem(index, "field.parameters", body);
    })
    .join("");
  applyTranslations(parameterHost);
}

function openEditor(job) {
  editing = job;
  draftParameters = job ? (job.parameters || []).map((param) => ({ ...param })) : [];
  editorTitle.textContent = job ? t("action.edit") : t("action.newJob");
  const fields = document.getElementById("job-fields");
  fields.innerHTML =
    '<div class="form-columns">' +
    textRow("id", "field.id", job ? job.id : "", { readonly: Boolean(job), required: true }) +
    textRow("name", "field.name", job ? job.name : "") +
    textRow("timeout", "field.timeout", job ? job.timeout : "", { hintKey: "hint.timeout" }) +
    listRow("labels", "field.labels", job ? job.labels : [], { hintKey: "hint.labels" }) +
    textRow("workdir_param", "field.workdirParam", job ? job.workdir_param : "", { hintKey: "hint.workdirParam" }) +
    "</div>" +
    textRow("description", "field.description", job ? job.description : "") +
    textareaRow("script", "field.script", job ? job.script : "", { hintKey: "hint.script", rows: 10 }) +
    checkRow("enabled", "field.enabled", job ? job.enabled !== false : true);
  renderParameters();
  applyTranslations(editorForm);
  editor.hidden = false;
  editor.scrollIntoView({ block: "nearest" });
}

function collectParameters(values) {
  return draftParameters.map((previous, index) => {
    const prefix = "param-" + index + "-";
    const type = values[prefix + "type"] || "string";
    const param = {
      ...previous,
      id: (values[prefix + "id"] || "").trim(),
      name: (values[prefix + "name"] || "").trim(),
      description: (values[prefix + "description"] || "").trim(),
      type,
      default: (values[prefix + "default"] || "").trim(),
      required: Boolean(values[prefix + "required"]),
    };
    const catalog = (values[prefix + "catalog"] || "").trim();
    const inline = parseList(values[prefix + "options"]);
    if (type === "choice" && catalog) {
      param.catalog = catalog;
      const labels = parseList(values[prefix + "catalogLabels"]);
      if (labels.length) param.catalog_labels = labels;
      else delete param.catalog_labels;
      delete param.options;
    } else if (type === "choice") {
      delete param.catalog;
      delete param.catalog_labels;
      const existing = new Map((previous.options || []).map((option) => [option.value, option]));
      param.options = inline.map((value) => ({ ...existing.get(value), value }));
    } else {
      delete param.catalog;
      delete param.catalog_labels;
      delete param.options;
    }
    return param;
  });
}

function syncParameterDrafts() {
  draftParameters = collectParameters(formValues(editorForm));
}

editorForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (savingJob) return;
  const values = formValues(editorForm);
  const payload = {
    id: (values.id || "").trim(),
    name: (values.name || "").trim(),
    description: (values.description || "").trim(),
    timeout: (values.timeout || "").trim(),
    script: values.script || "",
    workdir_param: (values.workdir_param || "").trim(),
    labels: parseList(values.labels),
    enabled: Boolean(values.enabled),
    parameters: collectParameters(values),
  };
  const submitButton = editorForm.querySelector('[type="submit"]');
  savingJob = true;
  if (submitButton) submitButton.disabled = true;
  try {
    if (editing) {
      await putJSON("/api/jobs/" + encodeURIComponent(editing.id), payload);
    } else {
      await postJSON("/api/jobs", payload);
    }
    editor.hidden = true;
    showNotice(notice, t("notice.saved"), "ok");
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    savingJob = false;
    if (submitButton?.isConnected) submitButton.disabled = false;
  }
});

parameterHost?.addEventListener("click", (event) => {
  const remove = event.target.closest("[data-remove-item]");
  if (!remove) return;
  const index = Number(remove.dataset.removeItem);
  syncParameterDrafts();
  draftParameters.splice(index, 1);
  renderParameters();
  const focusIndex = Math.min(index, draftParameters.length - 1);
  if (focusIndex >= 0) parameterHost.querySelector('[name="param-' + focusIndex + '-id"]')?.focus();
  else document.getElementById("add-parameter")?.focus();
});

document.getElementById("add-parameter")?.addEventListener("click", () => {
  syncParameterDrafts();
  draftParameters.push({ id: "", name: "", type: "string" });
  renderParameters();
  parameterHost.querySelector('[name="param-' + (draftParameters.length - 1) + '-id"]')?.focus();
});

document.getElementById("new-job")?.addEventListener("click", () => openEditor(null));
document.getElementById("close-job-editor")?.addEventListener("click", () => {
  editor.hidden = true;
});

list?.addEventListener("click", async (event) => {
  const runButton = event.target.closest("[data-run]");
  if (runButton) {
    openRunModal(jobs.find((job) => job.id === runButton.dataset.run), runButton);
    return;
  }
  const editButton = event.target.closest("[data-edit]");
  if (editButton) {
    openEditor(jobs.find((job) => job.id === editButton.dataset.edit));
    return;
  }
  const toggle = event.target.closest("[data-toggle]");
  if (toggle) {
    const job = jobs.find((entry) => entry.id === toggle.dataset.toggle);
    toggle.disabled = true;
    try {
      await putJSON("/api/jobs/" + encodeURIComponent(job.id), jobPayload(job, { enabled: !job.enabled }));
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    } finally {
      if (toggle.isConnected) toggle.disabled = false;
    }
    return;
  }
  const remove = event.target.closest("[data-delete]");
  if (remove) {
    if (!confirmAction(t("action.delete") + " " + remove.dataset.delete + "?")) return;
    remove.disabled = true;
    try {
      await deleteJSON("/api/jobs/" + encodeURIComponent(remove.dataset.delete));
      showNotice(notice, t("notice.deleted"), "ok");
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    } finally {
      if (remove.isConnected) remove.disabled = false;
    }
  }
});

function openRunModal(job, opener) {
  if (!job) return;
  runningJob = job;
  runTitle.textContent = job.name || job.id;
  runFields.innerHTML = (job.parameters || [])
    .map((param) => {
      const label = param.name || param.id;
      if (param.type === "choice") {
        const choices = (param.options || []).map((option) => ({
          value: option.value,
          label: option.label || option.value,
        }));
        choices.unshift({ value: "", label: t("select.choose") });
        return selectRow("p-" + param.id, "field.value", param.default, choices, { labelText: label, required: Boolean(param.required) });
      }
      if (param.type === "boolean") {
        return checkRow("p-" + param.id, "field.value", param.default === "true", { labelText: label });
      }
      return textRow("p-" + param.id, "field.value", param.default, { labelText: label, required: Boolean(param.required) });
    })
    .join("");
  const agents = (job.eligible_agents || [])
    .map((agent) => {
      const state = agent.online ? (agent.busy ? t("common.busy") : t("common.online")) : t("common.offline");
      return '<span class="chip">' + escapeHTML(agent.id + " · " + state) + "</span>";
    })
    .join("");
  runPreview.innerHTML =
    "<span>" + escapeHTML(t("jobs.eligible")) + "</span>" + (agents || '<span class="chip">-</span>');
  showNotice(runModalStatus, "");
  modalOpener = opener || document.activeElement;
  modalScrollY = window.scrollY;
  modalBodyStyle = {
    position: document.body.style.position,
    top: document.body.style.top,
    width: document.body.style.width,
  };
  modalInertState = Array.from(document.body.children)
    .filter((node) => node !== runModal)
    .map((node) => [node, node.inert]);
  modalInertState.forEach(([node]) => { node.inert = true; });
  document.body.style.position = "fixed";
  document.body.style.top = "-" + modalScrollY + "px";
  document.body.style.width = "100%";
  runModal.hidden = false;
  runForm.setAttribute("aria-busy", "false");
  runForm.querySelector("input:not([type=hidden]), select, button[type=submit]")?.focus();
}

function closeRunModal() {
  if (runSubmitting) return;
  runModal.hidden = true;
  modalInertState.forEach(([node, inert]) => { node.inert = inert; });
  modalInertState = [];
  if (modalBodyStyle) {
    document.body.style.position = modalBodyStyle.position;
    document.body.style.top = modalBodyStyle.top;
    document.body.style.width = modalBodyStyle.width;
  }
  window.scrollTo(0, modalScrollY);
  if (modalOpener?.isConnected) modalOpener.focus();
  else document.getElementById("new-job")?.focus();
}

runForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!runningJob || runSubmitting) return;
  const values = formValues(runForm);
  const payload = {};
  (runningJob.parameters || []).forEach((param) => {
    const raw = values["p-" + param.id];
    if (param.type === "boolean") {
      payload[param.id] = raw ? "true" : "false";
      return;
    }
    const text = String(raw ?? "").trim();
    if (text) payload[param.id] = text;
  });
  runSubmitting = true;
  runForm.setAttribute("aria-busy", "true");
  runForm.querySelectorAll("button[type=submit]").forEach((button) => { button.disabled = true; });
  showNotice(runModalStatus, "");
  try {
    const run = await postJSON("/api/jobs/" + encodeURIComponent(runningJob.id) + "/runs", payload);
    runSubmitting = false;
    closeRunModal();
    window.location.href = "/runs/" + encodeURIComponent(run.id);
  } catch (error) {
    showNotice(runModalStatus, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    runSubmitting = false;
    runForm.setAttribute("aria-busy", "false");
    runForm.querySelectorAll("button[type=submit]").forEach((button) => { button.disabled = false; });
  }
});

document.querySelectorAll("[data-close-run-modal]").forEach((button) => {
  button.addEventListener("click", () => {
    closeRunModal();
  });
});

runModal?.addEventListener("click", (event) => {
  if (event.target === runModal) closeRunModal();
});

runForm?.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    event.preventDefault();
    closeRunModal();
    return;
  }
  if (event.key !== "Tab") return;
  const focusable = Array.from(runForm.querySelectorAll('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])'))
    .filter((node) => !node.hidden && node.getClientRects().length);
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (!first || !last) return;
  if (event.shiftKey && (document.activeElement === first || !runForm.contains(document.activeElement))) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (document.activeElement === last || !runForm.contains(document.activeElement))) {
    event.preventDefault();
    first.focus();
  }
});

document.addEventListener("builda:localechange", render);

await initShell();
await load();
