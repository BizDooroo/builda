import { deleteJSON, getJSON, postJSON, putJSON, query } from "./api.js";
import { escapeHTML, formatTime, renderLabels, showNotice } from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { confirmAction, initShell, mountRecords } from "./shell.js";
import { checkRow, formValues, listRow, parseList, textRow } from "./forms.js";
import { copyText, flashButtonText } from "./clipboard.js";

const notice = document.getElementById("agents-status");
const list = document.getElementById("agents");
const editor = document.getElementById("agent-editor");
const editorForm = document.getElementById("agent-form");
const editorTitle = document.getElementById("agent-editor-title");
const secretHost = document.getElementById("agent-secret");

let agents = [];
let editing = null;
let savingAgent = false;
let loading = false;

async function load() {
  if (loading) return;
  loading = true;
  try {
    const response = await getJSON("/api/agents");
    agents = response.agents || [];
    render();
  } catch (error) {
    list?.setAttribute("aria-busy", "false");
    showNotice(notice, t("notice.loadFailed", { error: error.message }), "error");
  } finally {
    loading = false;
  }
}

// agentPayload strips the runtime fields the agents API does not accept.
function agentPayload(agent, overrides = {}) {
  return {
    id: agent.id,
    name: agent.name || "",
    description: agent.description || "",
    labels: agent.labels || [],
    enabled: agent.enabled !== false,
    paused: Boolean(agent.paused),
    ...overrides,
  };
}

function stateInfo(agent) {
  if (agent.blocked) return { className: "agent-blocked", label: t("common.blocked") };
  if (!agent.enabled) return { className: "agent-disabled", label: t("common.disabled") };
  if (agent.paused) return { className: "agent-paused", label: t("common.paused") };
  if (!agent.online) return { className: "agent-offline", label: t("common.offline") };
  if (agent.current_execution_id) return { className: "agent-busy", label: t("common.busy") };
  return { className: "agent-online", label: t("common.online") };
}

function render() {
  mountRecords(list, agents, (agent) => {
    const current = agent.current_execution_id
      ? '<a href="/runs/' + escapeHTML(agent.current_execution_id) + '">' +
        escapeHTML(agent.current_execution_id + " · " + (agent.current_status || "")) + "</a>"
      : "-";
    const attention = agent.blocked
      ? '<div class="attention">' + escapeHTML(agent.blocked_reason || "") + "</div>"
      : "";
    const state = stateInfo(agent);
    return (
      '<div class="record">' +
      '<div class="record-head"><div class="record-title"><strong>' + escapeHTML(agent.name || agent.id) + "</strong>" +
      '<div class="agent-identity"><span class="meta identifier">' + escapeHTML(agent.id) + "</span>" +
      '<span class="agent-state ' + state.className + '"><span class="status-dot" aria-hidden="true"></span>' + escapeHTML(state.label) + "</span></div>" +
      (agent.description ? '<div class="meta">' + escapeHTML(agent.description) + "</div>" : "") +
      "</div><div class=\"record-actions\">" +
      '<button type="button" class="secondary compact" data-edit="' + escapeHTML(agent.id) + '">' + escapeHTML(t("action.edit")) + "</button>" +
      '<details class="action-menu"><summary data-i18n="action.more">More</summary><div class="action-menu-items">' +
      '<button type="button" class="secondary compact" data-pause="' + escapeHTML(agent.id) + '">' +
      escapeHTML(agent.paused ? t("action.resume") : t("action.pause")) + "</button>" +
      '<button type="button" class="secondary compact" data-token="' + escapeHTML(agent.id) + '">' +
      escapeHTML(agent.enrolled ? t("action.rotate") : t("action.enroll")) + "</button>" +
      (agent.enrolled
        ? '<button type="button" class="secondary compact" data-revoke="' + escapeHTML(agent.id) + '">' + escapeHTML(t("action.revoke")) + "</button>"
        : "") +
      '<a class="button secondary compact" href="/runs' + query({ agent: agent.id }) + '">' + escapeHTML(t("action.viewRuns")) + "</a>" +
      '<button type="button" class="danger compact" data-delete="' + escapeHTML(agent.id) + '">' + escapeHTML(t("action.delete")) + "</button>" +
      "</div></details>" +
      "</div></div>" +
      '<div class="record-grid">' +
      "<span>" + escapeHTML(t("field.labels")) + ": " + (renderLabels(agent.labels) || "-") + "</span>" +
      "<span>" + escapeHTML(t("agents.enrolled")) + ": " + escapeHTML(agent.enrolled ? t("common.yes") : t("agents.notEnrolled")) + "</span>" +
      "<span>" + escapeHTML(t("agents.version")) + ": " + escapeHTML(agent.version || "-") + "</span>" +
      "<span>" + escapeHTML(t("agents.lastSeen")) + ": " + escapeHTML(formatTime(agent.last_seen)) + "</span>" +
      "<span>" + escapeHTML(t("agents.lastAssigned")) + ": " + escapeHTML(formatTime(agent.last_assigned_at)) + "</span>" +
      "<span>" + escapeHTML(t("agents.current")) + ": " + current + "</span>" +
      "</div>" + attention +
      "</div>"
    );
  });
  applyTranslations(list);
}

function openEditor(agent) {
  editing = agent;
  editorTitle.textContent = agent ? t("action.edit") : t("action.newAgent");
  document.getElementById("agent-fields").innerHTML =
    '<div class="form-columns">' +
    textRow("id", "field.id", agent ? agent.id : "", { readonly: Boolean(agent), required: true }) +
    textRow("name", "field.name", agent ? agent.name : "") +
    listRow("labels", "field.labels", agent ? agent.labels : [], { hintKey: "hint.labels" }) +
    "</div>" +
    textRow("description", "field.description", agent ? agent.description : "") +
    '<div class="form-inline">' +
    checkRow("enabled", "field.enabled", agent ? agent.enabled !== false : true) +
    checkRow("paused", "field.paused", agent ? Boolean(agent.paused) : false) +
    "</div>";
  applyTranslations(editorForm);
  editor.hidden = false;
  editor.scrollIntoView({ block: "nearest" });
}

editorForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (savingAgent) return;
  const values = formValues(editorForm);
  const payload = {
    id: (values.id || "").trim(),
    name: (values.name || "").trim(),
    description: (values.description || "").trim(),
    labels: parseList(values.labels),
    enabled: Boolean(values.enabled),
    paused: Boolean(values.paused),
  };
  const submitButton = editorForm.querySelector('[type="submit"]');
  savingAgent = true;
  if (submitButton) submitButton.disabled = true;
  try {
    if (editing) {
      await putJSON("/api/agents/" + encodeURIComponent(editing.id), payload);
    } else {
      await postJSON("/api/agents", payload);
    }
    editor.hidden = true;
    showNotice(notice, t("notice.saved"), "ok");
    await load();
  } catch (error) {
    showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
  } finally {
    savingAgent = false;
    if (submitButton?.isConnected) submitButton.disabled = false;
  }
});

document.getElementById("new-agent")?.addEventListener("click", () => openEditor(null));
document.getElementById("close-agent-editor")?.addEventListener("click", () => {
  editor.hidden = true;
});

function showSecret(agentID, token) {
  secretHost.hidden = false;
  secretHost.innerHTML =
    "<strong>" + escapeHTML(agentID) + "</strong>" +
    "<div>" + escapeHTML(t("agents.tokenOnce")) + "</div>" +
    "<code>" + escapeHTML(token) + "</code>" +
    '<div><button type="button" class="secondary compact" data-copy-token>' + escapeHTML(t("action.copy")) + "</button></div>";
  secretHost.querySelector("[data-copy-token]").addEventListener("click", async (event) => {
    try {
      await copyText(token);
      flashButtonText(event.currentTarget, t("common.copied"));
    } catch (error) {
      showNotice(notice, t("notice.copyFailed", { error: error.message }), "error");
    }
  });
}

list?.addEventListener("click", async (event) => {
  const editButton = event.target.closest("[data-edit]");
  if (editButton) {
    openEditor(agents.find((agent) => agent.id === editButton.dataset.edit));
    return;
  }
  const pause = event.target.closest("[data-pause]");
  if (pause) {
    const agent = agents.find((entry) => entry.id === pause.dataset.pause);
    pause.disabled = true;
    try {
      await putJSON("/api/agents/" + encodeURIComponent(agent.id), agentPayload(agent, { paused: !agent.paused }));
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    } finally {
      if (pause.isConnected) pause.disabled = false;
    }
    return;
  }
  const token = event.target.closest("[data-token]");
  if (token) {
    const agent = agents.find((entry) => entry.id === token.dataset.token);
    if (agent?.enrolled && !confirmAction(t("agents.confirmRotate", { id: agent.id }))) return;
    token.disabled = true;
    try {
      const issued = await postJSON("/api/agents/" + encodeURIComponent(token.dataset.token) + "/token", {});
      showSecret(token.dataset.token, issued.token);
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    } finally {
      if (token.isConnected) token.disabled = false;
    }
    return;
  }
  const revoke = event.target.closest("[data-revoke]");
  if (revoke) {
    if (!confirmAction(t("action.revoke") + " " + revoke.dataset.revoke + "?")) return;
    revoke.disabled = true;
    try {
      await deleteJSON("/api/agents/" + encodeURIComponent(revoke.dataset.revoke) + "/token");
      showNotice(notice, t("notice.deleted"), "ok");
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    } finally {
      if (revoke.isConnected) revoke.disabled = false;
    }
    return;
  }
  const remove = event.target.closest("[data-delete]");
  if (remove) {
    if (!confirmAction(t("action.delete") + " " + remove.dataset.delete + "?")) return;
    remove.disabled = true;
    try {
      await deleteJSON("/api/agents/" + encodeURIComponent(remove.dataset.delete));
      showNotice(notice, t("notice.deleted"), "ok");
      await load();
    } catch (error) {
      showNotice(notice, t("notice.requestFailed", { error: error.message }), "error");
    } finally {
      if (remove.isConnected) remove.disabled = false;
    }
  }
});

document.addEventListener("builda:localechange", render);
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") load();
});
window.addEventListener("pagehide", () => {
  secretHost.replaceChildren();
  secretHost.hidden = true;
});

await initShell();
await load();
setInterval(() => {
  if (document.visibilityState === "visible") load();
}, 5000);
