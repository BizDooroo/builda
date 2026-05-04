export async function initShell() {
  try {
    const response = await fetch("/api/meta");
    if (!response.ok) {
      throw new Error(await response.text());
    }
    const meta = await response.json();
    renderShellMeta(meta);
    return meta;
  } catch (error) {
    console.error("load meta failed", error);
    return {};
  }
}

export function renderShellMeta(meta) {
  document.querySelectorAll("[data-config-link]").forEach((link) => {
    link.hidden = !meta.config_editing_enabled;
  });

  document.querySelectorAll("[data-config-path]").forEach((node) => {
    node.textContent = meta.config_path || "";
  });
}

export function taskInputs(task) {
  return Array.isArray(task.Inputs) ? task.Inputs : [];
}

export function taskRunAPI(taskID, values = {}) {
  const path = "/api/tasks/" + encodeURIComponent(taskID) + "/run";
  const params = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => {
    params.set(key, value);
  });
  const query = params.toString();
  return query ? path + "?" + query : path;
}

export function taskRunCurl(taskID, values = {}) {
  return 'curl -X POST "' + window.location.origin + taskRunAPI(taskID, values) + '"';
}

export async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.width = "1px";
  textarea.style.height = "1px";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  try {
    if (!document.execCommand("copy")) {
      throw new Error("copy command was rejected");
    }
  } finally {
    textarea.remove();
  }
}

export function flashButtonText(button, text) {
  const previous = button.textContent;
  button.textContent = text;
  setTimeout(() => {
    if (button.isConnected) {
      button.textContent = previous;
    }
  }, 1200);
}

export function renderStatusBadge(status) {
  const normalized = String(status || "");
  return '<span class="badge status-' + escapeHTML(normalized) + '">' +
    '<span class="status-dot" aria-hidden="true"></span>' +
    escapeHTML(statusLabel(normalized)) +
    "</span>";
}

export function statusLabel(status) {
  const labels = {
    QUEUED: "대기",
    RUNNING: "실행 중",
    SUCCESS: "성공",
    FAILED: "실패",
    CANCELED: "취소됨",
    ABORTED: "중단됨",
  };
  return labels[String(status || "").toUpperCase()] || String(status || "-").toLowerCase();
}

export function isActiveStatus(status) {
  return status === "QUEUED" || status === "RUNNING";
}

export function isFailedStatus(status) {
  return status === "FAILED" || status === "CANCELED" || status === "ABORTED";
}

export function showNotice(target, message, type = "") {
  if (!target) return;
  target.textContent = message || "";
  target.className = "notice" + (type ? " " + type : "");
  target.hidden = !message;
}

export function renderTimes(run) {
  return '<div class="times">' +
    '<span>request ' + formatTime(run.requested_at) + "</span>" +
    '<span>start ' + formatTime(run.started_at) + "</span>" +
    '<span>elapsed ' + formatElapsed(run) + "</span>" +
    '<span>duration ' + formatDuration(run) + "</span>" +
    "</div>";
}

export function renderRunListTimes(run) {
  return '<span class="run-time-grid">' +
    '<span>request ' + formatTime(run.requested_at) + "</span>" +
    '<span>start ' + formatTime(run.started_at) + "</span>" +
    '<span>elapsed ' + formatElapsed(run) + "</span>" +
    '<span>duration ' + formatDuration(run) + "</span>" +
    "</span>";
}

export function renderRunParams(run) {
  const chips = renderRunParamChips(run.inputs);
  if (!chips) return "";
  return '<div class="param-list" aria-label="Run parameters">' + chips + "</div>";
}

export function renderRunParamChips(inputs) {
  const pairs = runParamPairs(inputs);
  if (!pairs.length) return "";
  return pairs.map(([key, value]) => {
    const label = String(key) + "=" + String(value ?? "");
    return '<span class="chip param-chip">' + escapeHTML(label) + "</span>";
  }).join("");
}

function runParamPairs(inputs) {
  if (!inputs || typeof inputs !== "object") return [];
  return Object.keys(inputs).sort().map((key) => [key, inputs[key]]);
}

export function renderLogText(logText) {
  return String(logText ?? "").split("\n").map(renderLogLine).join("");
}

function renderLogLine(line) {
  if (line === "") return '<span class="log-line"></span>';
  const paramLine = renderLogParamLine(line);
  if (paramLine) return paramLine;
  return '<span class="log-line">' + escapeHTML(line) + "</span>";
}

function renderLogParamLine(line) {
  const match = line.match(/^(\[[^\]]+\]\s+)params\s+(.+)$/);
  if (!match) return "";
  try {
    const inputs = JSON.parse(match[2]);
    const chips = renderRunParamChips(inputs);
    if (!chips) return "";
    return '<span class="log-line log-param-line">' +
      '<span class="log-prefix">' + escapeHTML(match[1] + "params") + "</span>" +
      '<span class="param-list log-param-list" aria-label="Run parameters">' + chips + "</span>" +
      "</span>";
  } catch {
    return "";
  }
}

export function formatElapsed(run) {
  if (!hasTime(run.started_at)) return "-";
  const end = hasTime(run.finished_at) ? new Date(run.finished_at) : new Date();
  return formatDurationMs(end - new Date(run.started_at));
}

export function formatDuration(run) {
  if (!hasTime(run.started_at) || !hasTime(run.finished_at)) return "-";
  return formatDurationMs(new Date(run.finished_at) - new Date(run.started_at));
}

export function hasTime(value) {
  return value && !String(value).startsWith("0001-");
}

export function formatDurationMs(value) {
  if (!Number.isFinite(value) || value < 0) return "-";
  const totalSeconds = Math.floor(value / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60);
    return hours + "h " + (minutes % 60) + "m";
  }
  if (minutes > 0) return minutes + "m " + seconds + "s";
  return seconds + "s";
}

export function formatTime(value) {
  if (!hasTime(value)) return "-";
  const date = new Date(value);
  const year = String(date.getFullYear()).slice(-2);
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  const hour = String(date.getHours()).padStart(2, "0");
  const minute = String(date.getMinutes()).padStart(2, "0");
  const second = String(date.getSeconds()).padStart(2, "0");
  return year + "-" + month + "-" + day + " " + hour + ":" + minute + ":" + second;
}

export function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
