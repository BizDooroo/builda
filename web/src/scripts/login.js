import { postJSON } from "./api.js";
import { showNotice } from "./format.js";
import { applyTranslations, t } from "./i18n.js";
import { initPreferences } from "./prefs.js";

initPreferences();
applyTranslations();

const form = document.getElementById("login-form");
const notice = document.getElementById("login-status");
const submit = document.getElementById("login-submit");

form?.addEventListener("submit", async (event) => {
  event.preventDefault();
  showNotice(notice, "");
  submit.disabled = true;
  const data = new FormData(form);
  try {
    await postJSON(
      "/api/login",
      { username: String(data.get("username") || ""), password: String(data.get("password") || "") },
      { allowUnauthorized: true },
    );
    window.location.href = "/";
  } catch (error) {
    showNotice(notice, t("notice.signInFailed", { error: error.message }), "error");
  } finally {
    submit.disabled = false;
  }
});
