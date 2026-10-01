import { deleteJSON, getJSON, postJSON, putJSON } from "./api.js";
import { escapeHTML, renderLabels, showNotice } from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { confirmAction, initShell, mountRecords } from "./shell.js";
import { formValues, listRow, parseList, repeaterItem, textRow } from "./forms.js";

const notice = document.getElementById("catalogs-status");
const list = document.getElementById("catalogs");
const editor = document.getElementById("catalog-editor");
const editorForm = document.getElementById("catalog-form");
const editorTitle = document.getElementById("catalog-editor-title");
const optionHost = document.getElementById("catalog-options");

let catalogs = [];
let editing = null;
let draftOptions = [];

async function load() {
  try {
    const response = await getJSON("/api/catalogs");
    catalogs = response.catalogs || [];
    render();
  } catch (error) {
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  }
}

function render() {
  mountRecords(list, catalogs, (catalog) => {
    const options = (catalog.options || [])
      .map((option) => {
        const path = option.values && option.values.path ? " · " + option.values.path : "";
        return (
          '<div class="detail-line"><span>' + escapeHTML(option.label || option.value) + "</span>" +
          "<span>" + escapeHTML(option.value + path) + " " + (renderLabels(option.labels) || "") + "</span></div>"
        );
      })
      .join("");
    const usage = (catalog.used_by || [])
      .map((entry) =>
        '<span class="chip">' +
        escapeHTML(entry.job_id + " · " + entry.parameter + " · " + entry.matches) +
        "</span>",
      )
      .join("");
    return (
      '<div class="record">' +
      '<div class="record-head"><div class="record-title"><strong>' + escapeHTML(catalog.name || catalog.id) + "</strong>" +
      '<div class="meta">' + escapeHTML(catalog.id) + "</div></div>" +
      '<div class="record-actions">' +
      '<button type="button" class="secondary compact" data-edit="' + escapeHTML(catalog.id) + '">' + escapeHTML(t("action.edit")) + "</button>" +
      '<button type="button" class="danger compact" data-delete="' + escapeHTML(catalog.id) + '">' + escapeHTML(t("action.delete")) + "</button>" +
      "</div></div>" +
      '<div class="record-grid"><span>' + escapeHTML(t("catalogs.usedBy")) + ": " + (usage || "-") + "</span></div>" +
      '<div class="input-list">' + (options || '<div class="empty">' + escapeHTML(t("common.none")) + "</div>") + "</div>" +
      "</div>"
    );
  });
  applyTranslations(list);
}

function renderOptions() {
  optionHost.innerHTML = draftOptions
    .map((option, index) => {
      const prefix = "option-" + index + "-";
      const body =
        '<div class="form-columns">' +
        textRow(prefix + "value", "field.value", option.value) +
        textRow(prefix + "label", "field.label", option.label) +
        listRow(prefix + "labels", "field.labels", option.labels, { placeholder: "android, ios" }) +
        textRow(prefix + "path", "field.path", option.values ? option.values.path : "", { hintKey: "hint.optionPath" }) +
        "</div>";
      return repeaterItem(index, "catalogs.options", body);
    })
    .join("");
  applyTranslations(optionHost);
}

function openEditor(catalog) {
  editing = catalog;
  draftOptions = catalog ? (catalog.options || []).map((option) => ({ ...option })) : [];
  editorTitle.textContent = catalog ? t("action.edit") : t("action.newCatalog");
  document.getElementById("catalog-fields").innerHTML =
    '<div class="form-columns">' +
    textRow("id", "field.id", catalog ? catalog.id : "", { readonly: Boolean(catalog), required: true }) +
    textRow("name", "field.name", catalog ? catalog.name : "") +
    "</div>" +
    textRow("description", "field.description", catalog ? catalog.description : "");
  renderOptions();
  applyTranslations(editorForm);
  editor.hidden = false;
  editor.scrollIntoView({ block: "nearest" });
}

editorForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const values = formValues(editorForm);
  const options = draftOptions.map((_, index) => {
    const prefix = "option-" + index + "-";
    const option = {
      value: (values[prefix + "value"] || "").trim(),
      label: (values[prefix + "label"] || "").trim(),
      labels: parseList(values[prefix + "labels"]),
    };
    const path = (values[prefix + "path"] || "").trim();
    if (path) option.values = { path };
    return option;
  });
  const payload = {
    id: (values.id || "").trim(),
    name: (values.name || "").trim(),
    description: (values.description || "").trim(),
    options,
  };
  try {
    if (editing) {
      await putJSON("/api/catalogs/" + encodeURIComponent(editing.id), payload);
    } else {
      await postJSON("/api/catalogs", payload);
    }
    editor.hidden = true;
    showNotice(notice, t("notice.saved"), "ok");
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  }
});

optionHost?.addEventListener("click", (event) => {
  const remove = event.target.closest("[data-remove-item]");
  if (!remove) return;
  draftOptions.splice(Number(remove.dataset.removeItem), 1);
  renderOptions();
});

document.getElementById("add-option")?.addEventListener("click", () => {
  draftOptions.push({ value: "", label: "", labels: [] });
  renderOptions();
});

document.getElementById("new-catalog")?.addEventListener("click", () => openEditor(null));
document.getElementById("close-catalog-editor")?.addEventListener("click", () => {
  editor.hidden = true;
});

list?.addEventListener("click", async (event) => {
  const editButton = event.target.closest("[data-edit]");
  if (editButton) {
    openEditor(catalogs.find((catalog) => catalog.id === editButton.dataset.edit));
    return;
  }
  const remove = event.target.closest("[data-delete]");
  if (remove) {
    if (!confirmAction(t("action.delete") + " " + remove.dataset.delete + "?")) return;
    try {
      await deleteJSON("/api/catalogs/" + encodeURIComponent(remove.dataset.delete));
      showNotice(notice, t("notice.deleted"), "ok");
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    }
  }
});

document.addEventListener("builda:localechange", render);

await initShell();
await load();
