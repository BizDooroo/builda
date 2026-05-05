const THEME_KEY = "builda.theme";
const LOCALE_KEY = "builda.locale";
const THEMES = ["dark", "light", "system"];
const LOCALES = ["en", "ko"];
const themeIcons = {
  dark: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20.4 14.6A8.5 8.5 0 0 1 9.4 3.6a8.5 8.5 0 1 0 11 11Z"></path></svg>',
  light: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"></circle><path d="M12 2v2M12 20v2M4 12H2M22 12h-2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M19.1 4.9l-1.4 1.4M6.3 17.7l-1.4 1.4"></path></svg>',
  system: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="5" width="16" height="11" rx="2"></rect><path d="M8 20h8M12 16v4"></path></svg>',
};

const translations = {
  en: {
    "action.cancel": "Cancel",
    "action.close": "Close",
    "action.copyCurl": "Copy curl",
    "action.copyLog": "Copy log",
    "action.copyURL": "Copy URL",
    "action.deleteRun": "Delete run",
    "action.followOff": "Follow off",
    "action.followOn": "Follow on",
    "action.load": "Load",
    "action.openLog": "Open log",
    "action.run": "Run",
    "action.save": "Save",
    "action.taskRuns": "Task runs",
    "action.viewAll": "View all",
    "action.viewAllRuns": "All runs",
    "brand.home": "Builda home",
    "common.copied": "Copied",
    "common.failed": "Failed",
    "common.loading": "Loading",
    "common.notSelected": "No run selected.",
    "config.eyebrow": "Administrative surface",
    "config.lead": "Load, validate, and save the active YAML configuration when config editing is enabled.",
    "config.passwordAria": "Config password",
    "config.passwordPlaceholder": "Password",
    "config.status.enterPassword": "Enter the password to load the configuration.",
    "config.status.loading": "Loading...",
    "config.status.needPassword": "Password is required.",
    "config.status.saved": "Saved.",
    "config.status.saving": "Validating...",
    "config.status.loadFailed": "Could not load the configuration.",
    "config.status.saveFailed": "Could not save the configuration.",
    "config.title": "Config editor",
    "confirm.cancelRun": "Cancel this run? A script that already started may be interrupted.",
    "confirm.deleteRun": "Delete this run history? Its log file will also be removed.",
    "confirm.runTask": "Run task '{task}'?",
    "confirm.saveConfig": "Save the configuration? Task scripts and runtime environment may change immediately.",
    "count.latest": "{count} latest",
    "count.runs": "{count} runs",
    "empty.activeRuns": "No queued or running runs.",
    "empty.failedRuns": "No failed runs.",
    "empty.noRuns": "No runs yet.",
    "empty.noTasks": "No configured tasks.",
    "empty.successRuns": "No successful runs.",
    "empty.taskActiveRuns": "This task has no queued or running runs.",
    "empty.taskFailedRuns": "This task has no failed runs.",
    "empty.taskRuns": "This task has no runs.",
    "empty.taskSuccessRuns": "This task has no successful runs.",
    "field.description": "Description",
    "field.env": "env",
    "field.exit": "Exit",
    "field.inputOptional": "optional",
    "field.inputRequired": "required",
    "field.options": "options",
    "field.taskID": "Task ID",
    "field.timeout": "Timeout",
    "filter.active": "Queued/Running",
    "filter.all": "All",
    "filter.failed": "Failed",
    "filter.success": "Success",
    "home.eyebrow": "Local task runner",
    "home.lead": "Builda runs only configured tasks, one at a time, and links every run to its log.",
    "home.latestRun": "Last run {id}",
    "home.metrics.latest": "Latest result",
    "home.metrics.queued": "Queued",
    "home.metrics.running": "Running",
    "home.metrics.tasks": "Tasks",
    "home.noRunsChip": "No run history",
    "home.panelTasksMeta": "Run only commands registered in the config file",
    "home.recentRuns": "Recent runs",
    "home.title": "Run tasks and monitor the queue.",
    "input.choice": "choice",
    "input.string": "string",
    "locale.aria": "Locale",
    "locale.en": "en",
    "locale.ko": "ko",
    "log.empty": "Select a run to view its log.",
    "log.heading": "Log",
    "log.loading": "Loading log...",
    "log.unavailable": "Log file is not available.",
    "modal.runMeta": "Task ID {task} · Input values are stored in run history.",
    "modal.runTitle": "Run {task}",
    "nav.back": "Back",
    "nav.primary": "Primary",
    "nav.runs": "Run history",
    "nav.settings": "Config",
    "nav.tasks": "Tasks",
    "notice.apiCopied": "API URL copied.",
    "notice.apiCopyFailed": "Could not copy API URL: {error}",
    "notice.configLoadFailed": "Could not load state: {error}",
    "notice.curlCopied": "curl command copied.",
    "notice.curlCopyFailed": "Could not copy curl: {error}",
    "notice.deleteRunFailed": "Could not delete run history: {error}",
    "notice.deleteRunSuccess": "Run history deleted.",
    "notice.loadRunFailed": "Could not load run: {error}",
    "notice.loadRunsFailed": "Could not load run history: {error}",
    "notice.logCopied": "Log copied.",
    "notice.logCopyFailed": "Could not copy log: {error}",
    "notice.runCancelFailed": "Could not cancel run: {error}",
    "notice.runCancelRequested": "Cancel requested.",
    "notice.runQueued": "Run queued. Opening log view.",
    "notice.runStartFailed": "Could not start run: {error}",
    "params.aria": "Run parameters",
    "run.loading": "Loading run",
    "runs.eyebrow": "Run history",
    "runs.lead": "Select a run from the timeline and inspect parameters, script, duration, and logs together.",
    "runs.selectAria": "Select run",
    "runs.statusFilterAria": "Status filters",
    "runs.taskFilter": "task {task}",
    "runs.timeline": "Timeline",
    "runs.title": "Inspect run history and logs.",
    "script.view": "View script",
    "select.noChoice": "No selection",
    "select.choose": "Choose",
    "status.ABORTED": "aborted",
    "status.CANCELED": "canceled",
    "status.FAILED": "failed",
    "status.QUEUED": "queued",
    "status.RUNNING": "running",
    "status.SUCCESS": "success",
    "task.detailsClose": "Hide details",
    "task.detailsOpen": "Show details",
    "theme.aria": "Color scheme",
    "theme.dark": "dark",
    "theme.light": "light",
    "theme.system": "system",
    "theme.toggleLabel": "Color scheme: {theme}",
    "time.cancelled": "cancelled",
    "time.duration": "duration",
    "time.elapsed": "elapsed",
    "time.finished": "finished",
    "time.request": "request",
    "time.start": "start",
  },
  ko: {
    "action.cancel": "취소",
    "action.close": "닫기",
    "action.copyCurl": "curl 복사",
    "action.copyLog": "로그 복사",
    "action.copyURL": "URL 복사",
    "action.deleteRun": "실행 기록 삭제",
    "action.followOff": "따라가기 끔",
    "action.followOn": "따라가기 켬",
    "action.load": "불러오기",
    "action.openLog": "로그 확인",
    "action.run": "실행",
    "action.save": "저장",
    "action.taskRuns": "이 작업 기록",
    "action.viewAll": "전체 보기",
    "action.viewAllRuns": "전체 실행 기록",
    "brand.home": "Builda 홈",
    "common.copied": "복사됨",
    "common.failed": "실패",
    "common.loading": "로딩 중",
    "common.notSelected": "실행을 선택하세요.",
    "config.eyebrow": "관리 화면",
    "config.lead": "설정 편집이 활성화된 경우 활성 YAML 설정을 불러오고 검증한 뒤 저장합니다.",
    "config.passwordAria": "설정 비밀번호",
    "config.passwordPlaceholder": "비밀번호",
    "config.status.enterPassword": "비밀번호를 입력해 설정을 불러오세요.",
    "config.status.loading": "불러오는 중...",
    "config.status.needPassword": "비밀번호가 필요합니다.",
    "config.status.saved": "저장했습니다.",
    "config.status.saving": "검증 중...",
    "config.status.loadFailed": "설정을 불러오지 못했습니다.",
    "config.status.saveFailed": "설정 저장에 실패했습니다.",
    "config.title": "설정 편집기",
    "confirm.cancelRun": "이 실행을 취소할까요? 이미 시작된 스크립트가 중단될 수 있습니다.",
    "confirm.deleteRun": "이 실행 기록을 삭제할까요? 연결된 로그 파일도 함께 삭제됩니다.",
    "confirm.runTask": "작업 '{task}'을(를) 실행할까요?",
    "confirm.saveConfig": "설정을 저장할까요? 작업 스크립트와 실행 환경이 즉시 바뀔 수 있습니다.",
    "count.latest": "최근 {count}개",
    "count.runs": "{count}개 실행",
    "empty.activeRuns": "대기 또는 실행 중인 기록이 없습니다.",
    "empty.failedRuns": "실패한 실행이 없습니다.",
    "empty.noRuns": "아직 실행 기록이 없습니다.",
    "empty.noTasks": "설정된 작업이 없습니다.",
    "empty.successRuns": "성공한 실행이 없습니다.",
    "empty.taskActiveRuns": "이 작업의 대기 또는 실행 중인 기록이 없습니다.",
    "empty.taskFailedRuns": "이 작업의 실패한 실행이 없습니다.",
    "empty.taskRuns": "이 작업의 실행 기록이 없습니다.",
    "empty.taskSuccessRuns": "이 작업의 성공한 실행이 없습니다.",
    "field.description": "설명",
    "field.env": "환경 변수",
    "field.exit": "종료 코드",
    "field.inputOptional": "선택",
    "field.inputRequired": "필수",
    "field.options": "옵션",
    "field.taskID": "작업 ID",
    "field.timeout": "제한 시간",
    "filter.active": "대기/실행",
    "filter.all": "전체",
    "filter.failed": "실패",
    "filter.success": "성공",
    "home.eyebrow": "로컬 작업 실행기",
    "home.lead": "Builda는 설정된 작업만 한 번에 하나씩 실행하고, 모든 실행을 로그와 연결합니다.",
    "home.latestRun": "마지막 실행 {id}",
    "home.metrics.latest": "최근 결과",
    "home.metrics.queued": "대기",
    "home.metrics.running": "실행 중",
    "home.metrics.tasks": "작업",
    "home.noRunsChip": "실행 기록 없음",
    "home.panelTasksMeta": "설정 파일에 등록된 명령만 실행",
    "home.recentRuns": "최근 실행",
    "home.title": "작업을 실행하고 큐 상태를 확인하세요.",
    "input.choice": "선택",
    "input.string": "문자열",
    "locale.aria": "언어",
    "locale.en": "en",
    "locale.ko": "ko",
    "log.empty": "실행을 선택하면 로그가 표시됩니다.",
    "log.heading": "로그",
    "log.loading": "로그를 불러오는 중...",
    "log.unavailable": "로그 파일을 사용할 수 없습니다.",
    "modal.runMeta": "작업 ID {task} · 입력값은 실행 기록에 저장됩니다.",
    "modal.runTitle": "{task} 실행",
    "nav.back": "뒤로",
    "nav.primary": "주요",
    "nav.runs": "실행 기록",
    "nav.settings": "설정",
    "nav.tasks": "작업",
    "notice.apiCopied": "API URL을 복사했습니다.",
    "notice.apiCopyFailed": "API URL 복사에 실패했습니다: {error}",
    "notice.configLoadFailed": "상태를 불러오지 못했습니다: {error}",
    "notice.curlCopied": "curl 명령을 복사했습니다.",
    "notice.curlCopyFailed": "curl 복사에 실패했습니다: {error}",
    "notice.deleteRunFailed": "실행 기록 삭제에 실패했습니다: {error}",
    "notice.deleteRunSuccess": "실행 기록을 삭제했습니다.",
    "notice.loadRunFailed": "실행 정보를 불러오지 못했습니다: {error}",
    "notice.loadRunsFailed": "실행 기록을 불러오지 못했습니다: {error}",
    "notice.logCopied": "로그를 복사했습니다.",
    "notice.logCopyFailed": "로그 복사에 실패했습니다: {error}",
    "notice.runCancelFailed": "취소에 실패했습니다: {error}",
    "notice.runCancelRequested": "취소 요청을 보냈습니다.",
    "notice.runQueued": "실행이 큐에 등록되었습니다. 로그 화면으로 이동합니다.",
    "notice.runStartFailed": "실행에 실패했습니다: {error}",
    "params.aria": "실행 파라미터",
    "run.loading": "실행을 불러오는 중",
    "runs.eyebrow": "실행 기록",
    "runs.lead": "타임라인에서 실행을 고르고, 파라미터·스크립트·소요 시간·로그를 한 화면에서 확인합니다.",
    "runs.selectAria": "실행 선택",
    "runs.statusFilterAria": "상태 필터",
    "runs.taskFilter": "작업 {task}",
    "runs.timeline": "타임라인",
    "runs.title": "실행 기록과 로그를 확인하세요.",
    "script.view": "스크립트 보기",
    "select.noChoice": "선택 안 함",
    "select.choose": "선택하세요",
    "status.ABORTED": "중단됨",
    "status.CANCELED": "취소됨",
    "status.FAILED": "실패",
    "status.QUEUED": "대기",
    "status.RUNNING": "실행 중",
    "status.SUCCESS": "성공",
    "task.detailsClose": "상세 닫기",
    "task.detailsOpen": "상세 보기",
    "theme.aria": "색상 모드",
    "theme.dark": "dark",
    "theme.light": "light",
    "theme.system": "system",
    "theme.toggleLabel": "색상 모드: {theme}",
    "time.cancelled": "취소",
    "time.duration": "소요",
    "time.elapsed": "경과",
    "time.finished": "완료",
    "time.request": "요청",
    "time.start": "시작",
  },
};

let currentTheme = "system";
let currentLocale = "ko";
let preferencesReady = false;

export async function initShell() {
  initPreferences();
  try {
    const response = await fetch("/api/meta");
    if (!response.ok) {
      throw new Error(await response.text());
    }
    const meta = await response.json();
    renderShellMeta(meta);
    applyTranslations();
    return meta;
  } catch (error) {
    console.error("load meta failed", error);
    return {};
  }
}

export function initPreferences() {
  currentTheme = normalizeChoice(storageGet(THEME_KEY), THEMES, "system");
  currentLocale = normalizeChoice(storageGet(LOCALE_KEY), LOCALES, "ko");
  applyTheme(currentTheme, false);
  applyLocale(currentLocale, false);
  if (!preferencesReady) {
    bindPreferenceControls();
    preferencesReady = true;
  }
  updatePreferenceButtons();
  applyTranslations();
}

export function setTheme(theme) {
  applyTheme(normalizeChoice(theme, THEMES, "system"), true);
}

export function setLocale(locale) {
  applyLocale(normalizeChoice(locale, LOCALES, "ko"), true);
}

export function locale() {
  return currentLocale;
}

export function t(key, values = {}) {
  const dictionary = translations[currentLocale] || translations.ko;
  const fallback = translations.ko[key] || translations.en[key] || key;
  const template = dictionary[key] || fallback;
  return String(template).replace(/\{([a-zA-Z0-9_]+)\}/g, (_, name) => {
    return values[name] == null ? "" : String(values[name]);
  });
}

export function applyTranslations(root = document) {
  root.querySelectorAll("[data-i18n]").forEach((node) => {
    node.textContent = t(node.dataset.i18n);
  });
  root.querySelectorAll("[data-i18n-placeholder]").forEach((node) => {
    node.setAttribute("placeholder", t(node.dataset.i18nPlaceholder));
  });
  root.querySelectorAll("[data-i18n-aria-label]").forEach((node) => {
    node.setAttribute("aria-label", t(node.dataset.i18nAriaLabel));
  });
  root.querySelectorAll("[data-i18n-title]").forEach((node) => {
    node.setAttribute("title", t(node.dataset.i18nTitle));
  });
}

function bindPreferenceControls() {
  document.addEventListener("click", (event) => {
    const themeButton = event.target.closest("[data-theme-toggle]");
    if (themeButton) {
      event.preventDefault();
      setTheme(nextTheme());
      return;
    }

    const localeButton = event.target.closest("[data-locale-toggle]");
    if (localeButton) {
      event.preventDefault();
      setLocale(currentLocale === "ko" ? "en" : "ko");
    }
  });

  const colorSchemeQuery = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
  colorSchemeQuery?.addEventListener?.("change", () => {
    if (currentTheme === "system") {
      document.documentElement.dataset.theme = "system";
    }
  });
}

function applyTheme(theme, persist) {
  currentTheme = theme;
  document.documentElement.dataset.theme = theme;
  if (persist) storageSet(THEME_KEY, theme);
  updatePreferenceButtons();
}

function applyLocale(nextLocale, persist) {
  const changed = currentLocale !== nextLocale;
  currentLocale = nextLocale;
  document.documentElement.lang = nextLocale;
  if (persist) storageSet(LOCALE_KEY, nextLocale);
  updatePreferenceButtons();
  applyTranslations();
  if (persist && changed) {
    document.dispatchEvent(new CustomEvent("builda:localechange", { detail: { locale: nextLocale } }));
  }
}

function updatePreferenceButtons() {
  document.querySelectorAll("[data-theme-toggle]").forEach((button) => {
    const label = t("theme.toggleLabel", { theme: t("theme." + currentTheme) });
    button.dataset.currentTheme = currentTheme;
    button.setAttribute("aria-label", label);
    button.setAttribute("title", label);
    button.querySelector("[data-theme-icon]").innerHTML = themeIcons[currentTheme] || themeIcons.system;
    const labelNode = button.querySelector("[data-theme-label]");
    if (labelNode) labelNode.textContent = label;
  });
  document.querySelectorAll("[data-locale-toggle]").forEach((button) => {
    button.dataset.currentLocale = currentLocale;
    button.setAttribute("aria-label", t("locale.aria"));
    button.setAttribute("title", t("locale.aria"));
    button.querySelector("[data-locale-en]")?.classList.toggle("active", currentLocale === "en");
    button.querySelector("[data-locale-ko]")?.classList.toggle("active", currentLocale === "ko");
  });
}

function nextTheme() {
  const index = THEMES.indexOf(currentTheme);
  return THEMES[(index + 1) % THEMES.length] || "system";
}

function normalizeChoice(value, allowed, fallback) {
  return allowed.includes(value) ? value : fallback;
}

function storageGet(key) {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

function storageSet(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Ignore storage failures; the active page can still reflect the preference.
  }
}

export function renderShellMeta(meta) {
  document.querySelectorAll("[data-config-link]").forEach((link) => {
    link.hidden = !meta.config_editing_enabled;
  });

  document.querySelectorAll("[data-config-path]").forEach((node) => {
    node.textContent = meta.config_path || "";
  });

  const version = String(meta.version || "").trim() || "dev";
  let commit = String(meta.commit || "").trim() || "unknown";
  if (commit !== "unknown" && meta.build_modified) commit += " dirty";
  const buildID = "Builda " + version + " @ " + commit;
  document.querySelectorAll("[data-build-id]").forEach((node) => {
    node.textContent = buildID;
    node.title = meta.version_info || buildID;
    node.setAttribute("aria-label", buildID);
    node.hidden = false;
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
  const normalized = String(status || "").toUpperCase();
  const label = t("status." + normalized);
  return label.startsWith("status.") ? String(status || "-").toLowerCase() : label;
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
    '<span>' + escapeHTML(t("time.request")) + " " + formatTime(run.requested_at) + "</span>" +
    '<span>' + escapeHTML(t("time.start")) + " " + formatTime(run.started_at) + "</span>" +
    '<span>' + escapeHTML(t("time.elapsed")) + " " + formatElapsed(run) + "</span>" +
    '<span>' + escapeHTML(t("time.duration")) + " " + formatDuration(run) + "</span>" +
    "</div>";
}

export function renderRunListTimes(run) {
  return '<span class="run-time-grid">' +
    '<span>' + escapeHTML(t("time.request")) + " " + formatTime(run.requested_at) + "</span>" +
    '<span>' + escapeHTML(t("time.start")) + " " + formatTime(run.started_at) + "</span>" +
    '<span>' + escapeHTML(t("time.elapsed")) + " " + formatElapsed(run) + "</span>" +
    '<span>' + escapeHTML(t("time.duration")) + " " + formatDuration(run) + "</span>" +
    "</span>";
}

export function renderRunParams(run) {
  const chips = renderRunParamChips(run.inputs);
  if (!chips) return "";
  return '<div class="param-list" aria-label="' + escapeHTML(t("params.aria")) + '">' + chips + "</div>";
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

export function runLogDisplayText(logText) {
  const text = String(logText ?? "");
  return isMissingRunLog(text) ? t("log.unavailable") : text;
}

export function isMissingRunLog(logText) {
  return String(logText ?? "").trim() === "Log file is not available.";
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
      '<span class="param-list log-param-list" aria-label="' + escapeHTML(t("params.aria")) + '">' + chips + "</span>" +
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
