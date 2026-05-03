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
    const target = document.querySelector("[data-server-meta]");
    if (target) {
      target.textContent = "server unavailable";
    }
    console.error("load meta failed", error);
    return {};
  }
}

export function renderShellMeta(meta) {
  const target = document.querySelector("[data-server-meta]");
  if (target) {
    const pieces = [];
    if (meta.hostname) pieces.push("host " + meta.hostname);
    if (meta.log_dir) pieces.push("logs " + meta.log_dir);
    if (meta.config_path && document.body.dataset.headerMode !== "default") pieces.push("config " + meta.config_path);
    if (meta.started) pieces.push("started " + meta.started);
    target.textContent = pieces.join(" · ") || "Builda";
  }

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
  return '<span class="badge status-' + escapeHTML(status) + '">' + escapeHTML(String(status || "").toLowerCase()) + "</span>";
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
  const formatted = formatRunParams(run.inputs);
  if (!formatted) return "";
  return '<div class="meta">params ' + escapeHTML(formatted) + "</div>";
}

export function formatRunParams(inputs) {
  if (!inputs || typeof inputs !== "object" || !Object.keys(inputs).length) return "";
  const sorted = {};
  Object.keys(inputs).sort().forEach((key) => {
    sorted[key] = inputs[key];
  });
  return JSON.stringify(sorted);
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
