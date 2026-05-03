import { initShell } from "./shared.js";

const configEditorEl = document.querySelector("#config-editor");
const configStatusEl = document.querySelector("#config-status");
const configPasswordEl = document.querySelector("#config-password");
const loadConfigEl = document.querySelector("#load-config");
const saveConfigEl = document.querySelector("#save-config");

loadConfigEl.addEventListener("click", async (event) => {
  event.preventDefault();
  await loadConfig();
});

saveConfigEl.addEventListener("click", async (event) => {
  event.preventDefault();
  await saveConfig();
});

async function loadConfig() {
  const password = configPasswordEl.value;
  if (!password) {
    configStatus("Password is required.", "error");
    return;
  }
  loadConfigEl.disabled = true;
  configStatus("Loading...", "");
  try {
    const response = await fetch("/api/config", {
      headers: { "X-Builda-Config-Password": password },
    });
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}));
      configStatus(payload.error || "Failed to load config.", "error");
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
    configStatus("Password is required.", "error");
    return;
  }
  saveConfigEl.disabled = true;
  configStatus("Validating...", "");
  try {
    const body = new URLSearchParams({ content: configEditorEl.value, password });
    const response = await fetch("/api/config", { method: "POST", body });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || !payload.ok) {
      throw new Error(payload.error || "Config save failed.");
    }
    configStatus("Saved.", "ok");
  } catch (error) {
    configStatus(error.message, "error");
  } finally {
    saveConfigEl.disabled = false;
  }
}

function configStatus(message, type) {
  configStatusEl.textContent = message;
  configStatusEl.className = "editor-status" + (type ? " " + type : "");
}

await initShell();
configStatus("Enter password to load config.", "");
