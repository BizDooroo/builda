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
  if (!confirm("설정을 저장할까요? 작업 스크립트와 실행 환경이 즉시 바뀔 수 있습니다.")) return;
  await saveConfig();
});

async function loadConfig() {
  const password = configPasswordEl.value;
  if (!password) {
    configStatus("비밀번호가 필요합니다.", "error");
    return;
  }
  loadConfigEl.disabled = true;
  configStatus("불러오는 중...", "");
  try {
    const response = await fetch("/api/config", {
      headers: { "X-Builda-Config-Password": password },
    });
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}));
      configStatus(payload.error || "설정을 불러오지 못했습니다.", "error");
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
    configStatus("비밀번호가 필요합니다.", "error");
    return;
  }
  saveConfigEl.disabled = true;
  configStatus("검증 중...", "");
  try {
    const body = new URLSearchParams({ content: configEditorEl.value, password });
    const response = await fetch("/api/config", { method: "POST", body });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || !payload.ok) {
      throw new Error(payload.error || "설정 저장에 실패했습니다.");
    }
    configStatus("저장했습니다.", "ok");
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
configStatus("비밀번호를 입력해 설정을 불러오세요.", "");
