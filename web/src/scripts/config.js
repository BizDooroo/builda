import { initShell, t } from "./shared.js";

const configEditorEl = document.querySelector("#config-editor");
const configStatusEl = document.querySelector("#config-status");
const configPasswordEl = document.querySelector("#config-password");
const loadConfigEl = document.querySelector("#load-config");
const saveConfigEl = document.querySelector("#save-config");
let statusKey = "";
let statusType = "";

loadConfigEl.addEventListener("click", async (event) => {
  event.preventDefault();
  await loadConfig();
});

saveConfigEl.addEventListener("click", async (event) => {
  event.preventDefault();
  if (!confirm(t("confirm.saveConfig"))) return;
  await saveConfig();
});

async function loadConfig() {
  const password = configPasswordEl.value;
  if (!password) {
    configStatusKey("config.status.needPassword", "error");
    return;
  }
  loadConfigEl.disabled = true;
  configStatusKey("config.status.loading", "");
  try {
    const response = await fetch("/api/config", {
      headers: { "X-Builda-Config-Password": password },
    });
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}));
      configStatus(payload.error || t("config.status.loadFailed"), "error");
      return;
    }
    const payload = await response.json();
    configEditorEl.value = payload.content || "";
    configStatus("", "");
  } catch (error) {
    configStatus(error.message, "error");
  } finally {
    loadConfigEl.disabled = false;
  }
}

async function saveConfig() {
  const password = configPasswordEl.value;
  if (!password) {
    configStatusKey("config.status.needPassword", "error");
    return;
  }
  saveConfigEl.disabled = true;
  configStatusKey("config.status.saving", "");
  try {
    const body = new URLSearchParams({ content: configEditorEl.value, password });
    const response = await fetch("/api/config", { method: "POST", body });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || !payload.ok) {
      throw new Error(payload.error || t("config.status.saveFailed"));
    }
    configStatusKey("config.status.saved", "ok");
  } catch (error) {
    configStatus(error.message, "error");
  } finally {
    saveConfigEl.disabled = false;
  }
}

function configStatus(message, type) {
  statusKey = "";
  statusType = type || "";
  configStatusEl.textContent = message;
  configStatusEl.className = "editor-status" + (type ? " " + type : "");
}

function configStatusKey(key, type) {
  statusKey = key;
  statusType = type || "";
  configStatusEl.textContent = t(key);
  configStatusEl.className = "editor-status" + (type ? " " + type : "");
}

await initShell();
document.addEventListener("builda:localechange", () => {
  if (statusKey) configStatusKey(statusKey, statusType);
});
configStatusKey("config.status.enterPassword", "");
