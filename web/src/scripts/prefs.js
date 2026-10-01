import { applyTranslations, localeKey, locales, t, useLocale } from "./i18n.js";

const THEME_KEY = "builda.theme";
const THEMES = ["dark", "light", "system"];
const themeIcons = {
  dark: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20.4 14.6A8.5 8.5 0 0 1 9.4 3.6a8.5 8.5 0 1 0 11 11Z"></path></svg>',
  light: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"></circle><path d="M12 2v2M12 20v2M4 12H2M22 12h-2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M19.1 4.9l-1.4 1.4M6.3 17.7l-1.4 1.4"></path></svg>',
  system: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="5" width="16" height="11" rx="2"></rect><path d="M8 20h8M12 16v4"></path></svg>',
};

let currentTheme = "system";
let bound = false;

export function initPreferences() {
  currentTheme = normalizeChoice(storageGet(THEME_KEY), THEMES, "system");
  applyTheme(currentTheme, false);
  applyLocale(normalizeChoice(storageGet(localeKey()), locales(), "ko"), false);
  if (!bound) {
    bindControls();
    bound = true;
  }
  updateButtons();
  applyTranslations();
}

export function setTheme(theme) {
  applyTheme(normalizeChoice(theme, THEMES, "system"), true);
}

export function setLocale(next) {
  applyLocale(normalizeChoice(next, locales(), "ko"), true);
}

function bindControls() {
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
      setLocale(localeButton.dataset.currentLocale === "ko" ? "en" : "ko");
    }
  });

  const query = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
  query?.addEventListener?.("change", () => {
    if (currentTheme === "system") applyTheme(currentTheme, false);
  });
}

function applyTheme(theme, persist) {
  currentTheme = theme;
  document.documentElement.dataset.theme = theme;
  const systemDark = window.matchMedia?.("(prefers-color-scheme: dark)").matches || false;
  document.documentElement.dataset.colorScheme = theme === "dark" || (theme === "system" && systemDark) ? "dark" : "light";
  if (persist) storageSet(THEME_KEY, theme);
  updateButtons();
}

function applyLocale(next, persist) {
  const previous = document.documentElement.lang;
  const applied = useLocale(next);
  document.documentElement.lang = applied;
  if (persist) storageSet(localeKey(), applied);
  updateButtons();
  applyTranslations();
  if (persist && previous !== applied) {
    document.dispatchEvent(new CustomEvent("builda:localechange", { detail: { locale: applied } }));
  }
}

function updateButtons() {
  const localeValue = document.documentElement.lang === "en" ? "en" : "ko";
  document.querySelectorAll("[data-theme-toggle]").forEach((button) => {
    const label = t("theme.toggleLabel", { theme: t("theme." + currentTheme) });
    button.dataset.currentTheme = currentTheme;
    button.setAttribute("aria-label", label);
    button.setAttribute("title", label);
    const icon = button.querySelector("[data-theme-icon]");
    if (icon) icon.innerHTML = themeIcons[currentTheme] || themeIcons.system;
    const labelNode = button.querySelector("[data-theme-label]");
    if (labelNode) labelNode.textContent = label;
  });
  document.querySelectorAll("[data-locale-toggle]").forEach((button) => {
    button.dataset.currentLocale = localeValue;
    button.setAttribute("aria-label", t("locale.aria"));
    button.setAttribute("title", t("locale.aria"));
    button.querySelector("[data-locale-en]")?.classList.toggle("active", localeValue === "en");
    button.querySelector("[data-locale-ko]")?.classList.toggle("active", localeValue === "ko");
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
    // A page can still reflect the preference without persistence.
  }
}
