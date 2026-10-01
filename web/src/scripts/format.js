import { t } from "./i18n.js";

export function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

export function hasTime(value) {
  return value && !String(value).startsWith("0001-");
}

export function formatTime(value) {
  if (!hasTime(value)) return "-";
  const date = new Date(value);
  const pad = (input) => String(input).padStart(2, "0");
  return (
    String(date.getFullYear()).slice(-2) +
    "-" + pad(date.getMonth() + 1) +
    "-" + pad(date.getDate()) +
    " " + pad(date.getHours()) +
    ":" + pad(date.getMinutes()) +
    ":" + pad(date.getSeconds())
  );
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

export function formatElapsed(run) {
  if (!hasTime(run.started_at)) return "-";
  const end = hasTime(run.finished_at) ? new Date(run.finished_at) : new Date();
  return formatDurationMs(end - new Date(run.started_at));
}

export function formatDuration(run) {
  if (!hasTime(run.started_at) || !hasTime(run.finished_at)) return "-";
  return formatDurationMs(new Date(run.finished_at) - new Date(run.started_at));
}

export function statusLabel(status) {
  const normalized = String(status || "").toUpperCase();
  const label = t("status." + normalized);
  return label.startsWith("status.") ? normalized.toLowerCase() : label;
}

export function renderStatusBadge(status) {
  const normalized = String(status || "");
  return (
    '<span class="badge status-' + escapeHTML(normalized) + '">' +
    '<span class="status-dot" aria-hidden="true"></span>' +
    escapeHTML(statusLabel(normalized)) +
    "</span>"
  );
}

export function isActiveStatus(status) {
  return status === "QUEUED" || status === "ASSIGNED" || status === "RUNNING" || status === "CANCELING";
}

export function renderChips(values) {
  if (!values || typeof values !== "object") return "";
  return Object.keys(values)
    .sort()
    .map((key) => '<span class="chip param-chip">' + escapeHTML(key + "=" + (values[key] ?? "")) + "</span>")
    .join("");
}

export function renderLabels(labels) {
  if (!Array.isArray(labels) || !labels.length) return "";
  return labels.map((label) => '<span class="chip">' + escapeHTML(label) + "</span>").join("");
}

export function renderRunTimes(run) {
  return (
    '<span class="run-time-grid">' +
    "<span>" + escapeHTML(t("time.request")) + " " + formatTime(run.requested_at) + "</span>" +
    "<span>" + escapeHTML(t("time.start")) + " " + formatTime(run.started_at) + "</span>" +
    "<span>" + escapeHTML(t("time.elapsed")) + " " + formatElapsed(run) + "</span>" +
    "<span>" + escapeHTML(t("time.duration")) + " " + formatDuration(run) + "</span>" +
    "</span>"
  );
}

export function renderLogText(logText) {
  return String(logText ?? "")
    .split("\n")
    .map(renderLogLine)
    .join("");
}

function renderLogLine(line) {
  if (line === "") return '<span class="log-line"></span>';
  const match = line.match(/^(\[[^\]]+\]\s+)params\s+(.+)$/);
  if (match) {
    try {
      const chips = renderChips(JSON.parse(match[2]));
      if (chips) {
        return (
          '<span class="log-line log-param-line">' +
          '<span class="log-prefix">' + escapeHTML(match[1] + "params") + "</span>" +
          '<span class="param-list log-param-list" aria-label="' + escapeHTML(t("params.aria")) + '">' + chips + "</span>" +
          "</span>"
        );
      }
    } catch {
      // Fall through to a plain line.
    }
  }
  return '<span class="log-line">' + escapeHTML(line) + "</span>";
}

export function showNotice(target, message, type = "") {
  if (!target) return;
  target.textContent = message || "";
  target.className = "notice" + (type ? " " + type : "");
  target.hidden = !message;
}
