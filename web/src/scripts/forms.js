import { escapeHTML } from "./format.js";
import { t } from "./i18n.js";

// Dedicated management forms are built from these helpers so every page gets
// the same markup, labels, and hint placement. Pass labelText to use a label
// that comes from configuration rather than from the translation table.

function labelMarkup(labelKey, options) {
  if (options.labelText !== undefined) return escapeHTML(options.labelText);
  return '<span data-i18n="' + escapeHTML(labelKey) + '">' + escapeHTML(t(labelKey)) + "</span>";
}

export function textRow(name, labelKey, value, options = {}) {
  const hint = options.hintKey ? '<div class="hint">' + escapeHTML(t(options.hintKey)) + "</div>" : "";
  const attrs = [
    'name="' + escapeHTML(name) + '"',
    'value="' + escapeHTML(value ?? "") + '"',
    'type="' + (options.type || "text") + '"',
  ];
  if (options.placeholder) attrs.push('placeholder="' + escapeHTML(options.placeholder) + '"');
  if (options.readonly) attrs.push("readonly");
  if (options.required) attrs.push("required");
  return (
    '<div class="form-row"><label for="' + escapeHTML(name) + '">' + labelMarkup(labelKey, options) + "</label>" +
    '<input id="' + escapeHTML(name) + '" ' + attrs.join(" ") + " />" + hint + "</div>"
  );
}

export function textareaRow(name, labelKey, value, options = {}) {
  const hint = options.hintKey ? '<div class="hint">' + escapeHTML(t(options.hintKey)) + "</div>" : "";
  const rows = options.rows || 8;
  return (
    '<div class="form-row"><label for="' + escapeHTML(name) + '">' + labelMarkup(labelKey, options) + "</label>" +
    '<textarea id="' + escapeHTML(name) + '" name="' + escapeHTML(name) + '" rows="' + rows + '" spellcheck="false">' +
    escapeHTML(value ?? "") + "</textarea>" + hint + "</div>"
  );
}

export function selectRow(name, labelKey, value, choices, options = {}) {
  const hint = options.hintKey ? '<div class="hint">' + escapeHTML(t(options.hintKey)) + "</div>" : "";
  const rendered = choices
    .map((choice) => {
      const selected = String(choice.value) === String(value ?? "") ? " selected" : "";
      const translation = choice.labelKey ? ' data-i18n="' + escapeHTML(choice.labelKey) + '"' : "";
      return '<option value="' + escapeHTML(choice.value) + '"' + selected + translation + ">" + escapeHTML(choice.label) + "</option>";
    })
    .join("");
  return (
    '<div class="form-row"><label for="' + escapeHTML(name) + '">' + labelMarkup(labelKey, options) + "</label>" +
    '<select id="' + escapeHTML(name) + '" name="' + escapeHTML(name) + '"' + (options.required ? " required" : "") + ">" + rendered + "</select>" + hint + "</div>"
  );
}

export function checkRow(name, labelKey, checked, options = {}) {
  return (
    '<div class="form-row"><label class="form-check">' +
    '<input type="checkbox" name="' + escapeHTML(name) + '"' + (checked ? " checked" : "") + " /> " +
    labelMarkup(labelKey, options) + "</label></div>"
  );
}

// listRow edits a list of short values as comma separated text, which is the
// shape labels and option label filters naturally have.
export function listRow(name, labelKey, values, options = {}) {
  return textRow(name, labelKey, (values || []).join(", "), {
    ...options,
    placeholder: options.placeholder || "linux, android",
  });
}

export function parseList(value) {
  return String(value || "")
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

export function formValues(form) {
  const values = {};
  new FormData(form).forEach((value, key) => {
    values[key] = typeof value === "string" ? value : "";
  });
  form.querySelectorAll('input[type="checkbox"]').forEach((input) => {
    values[input.name] = input.checked;
  });
  return values;
}

// repeaterItem wraps one editable sub-record with a remove button.
export function repeaterItem(index, titleKey, body) {
  return (
    '<div class="repeater-item" data-repeater-item="' + index + '">' +
    '<div class="repeater-head"><strong><span data-i18n="' + escapeHTML(titleKey) + '">' + escapeHTML(t(titleKey)) + "</span> " + (index + 1) + "</strong>" +
    '<button type="button" class="secondary compact" data-remove-item="' + index + '">' +
    escapeHTML(t("action.remove")) + "</button></div>" + body + "</div>"
  );
}

export function typeChoices() {
  return [
    { value: "string", label: t("type.string"), labelKey: "type.string" },
    { value: "choice", label: t("type.choice"), labelKey: "type.choice" },
    { value: "boolean", label: t("type.boolean"), labelKey: "type.boolean" },
  ];
}
