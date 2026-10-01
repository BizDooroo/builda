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

let jobs = [];
let catalogs = [];
let editing = null;
let draftParameters = [];
let runningJob = null;

async function load() {
  try {
    const [jobResponse, catalogResponse] = await Promise.all([getJSON("/api/jobs"), getJSON("/api/catalogs")]);
    jobs = jobResponse.jobs || [];
    catalogs = catalogResponse.catalogs || [];
    render();
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

function render() {
  mountRecords(list, jobs, (job) => {
    const agents = (job.eligible_agents || [])
      .map((agent) => {
        const state = agent.online ? (agent.busy ? "busy" : t("common.online")) : t("common.offline");
        return '<span class="chip">' + escapeHTML(agent.id + " · " + state) + "</span>";
      })
      .join("");
    const warning = job.runnable ? "" : '<div class="hint">' + escapeHTML(t("jobs.noAgent")) + "</div>";
    return (
      '<div class="record" data-job="' + escapeHTML(job.id) + '">' +
      '<div class="record-head"><div class="record-title"><strong>' + escapeHTML(job.name || job.id) + "</strong>" +
      '<div class="meta">' + escapeHTML(job.id) + (job.enabled ? "" : " · " + escapeHTML(t("common.disabled"))) + "</div>" +
      (job.description ? '<div class="meta">' + escapeHTML(job.description) + "</div>" : "") +
      "</div><div class=\"record-actions\">" +
      '<button type="button" data-run="' + escapeHTML(job.id) + '"' + (job.enabled ? "" : " disabled") + ">" + escapeHTML(t("action.execute")) + "</button>" +
      '<button type="button" class="secondary compact" data-edit="' + escapeHTML(job.id) + '">' + escapeHTML(t("action.edit")) + "</button>" +
      '<button type="button" class="secondary compact" data-toggle="' + escapeHTML(job.id) + '">' +
      escapeHTML(job.enabled ? t("common.disabled") : t("field.enabled")) + "</button>" +
      '<a class="button secondary compact" href="/runs' + query({ job: job.id }) + '">' + escapeHTML(t("action.viewRuns")) + "</a>" +
      '<button type="button" class="danger compact" data-delete="' + escapeHTML(job.id) + '">' + escapeHTML(t("action.delete")) + "</button>" +
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
  return draftParameters.map((_, index) => {
    const prefix = "param-" + index + "-";
    const type = values[prefix + "type"] || "string";
    const param = {
      id: (values[prefix + "id"] || "").trim(),
      name: (values[prefix + "name"] || "").trim(),
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
    } else if (type === "choice" && inline.length) {
      param.options = inline.map((value) => ({ value }));
    }
    return param;
  });
}

editorForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
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
  }
});

parameterHost?.addEventListener("click", (event) => {
  const remove = event.target.closest("[data-remove-item]");
  if (!remove) return;
  draftParameters.splice(Number(remove.dataset.removeItem), 1);
  renderParameters();
});

document.getElementById("add-parameter")?.addEventListener("click", () => {
  draftParameters.push({ id: "", name: "", type: "string" });
  renderParameters();
});

document.getElementById("new-job")?.addEventListener("click", () => openEditor(null));
document.getElementById("close-job-editor")?.addEventListener("click", () => {
  editor.hidden = true;
});

list?.addEventListener("click", async (event) => {
  const runButton = event.target.closest("[data-run]");
  if (runButton) {
    openRunModal(jobs.find((job) => job.id === runButton.dataset.run));
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
    try {
      await putJSON("/api/jobs/" + encodeURIComponent(job.id), jobPayload(job, { enabled: !job.enabled }));
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    }
    return;
  }
  const remove = event.target.closest("[data-delete]");
  if (remove) {
    if (!confirmAction(t("action.delete") + " " + remove.dataset.delete + "?")) return;
    try {
      await deleteJSON("/api/jobs/" + encodeURIComponent(remove.dataset.delete));
      showNotice(notice, t("notice.deleted"), "ok");
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    }
  }
});

function openRunModal(job) {
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
        if (!param.required) choices.unshift({ value: "", label: t("select.choose") });
        return selectRow("p-" + param.id, "field.value", param.default, choices, { labelText: label });
      }
      if (param.type === "boolean") {
        return checkRow("p-" + param.id, "field.value", param.default === "true", { labelText: label });
      }
      return textRow("p-" + param.id, "field.value", param.default, { labelText: label });
    })
    .join("");
  const agents = (job.eligible_agents || [])
    .map((agent) => {
      const state = agent.online ? (agent.busy ? "busy" : t("common.online")) : t("common.offline");
      return '<span class="chip">' + escapeHTML(agent.id + " · " + state) + "</span>";
    })
    .join("");
  runPreview.innerHTML =
    "<span>" + escapeHTML(t("jobs.eligible")) + "</span>" + (agents || '<span class="chip">-</span>');
  runModal.hidden = false;
  runForm.querySelector("input, select, button")?.focus();
}

runForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!runningJob) return;
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
  try {
    const run = await postJSON("/api/jobs/" + encodeURIComponent(runningJob.id) + "/runs", payload);
    runModal.hidden = true;
    window.location.href = "/runs/" + encodeURIComponent(run.id);
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

document.querySelectorAll("[data-close-run-modal]").forEach((button) => {
  button.addEventListener("click", () => {
    runModal.hidden = true;
  });
});

document.addEventListener("builda:localechange", render);

await initShell();
await load();
