import { getText } from "./api.js";
import { renderLogText, showNotice } from "./format.js";
import { t } from "./i18n.js";
import { copyText, flashButtonText } from "./clipboard.js";

// LogView follows one execution log. It appends by byte offset and only
// touches the DOM when the text actually changes, so a completed log stays
// selectable while polling continues.
export class LogView {
  constructor(options) {
    this.pre = options.pre;
    this.copyButton = options.copyButton;
    this.followButton = options.followButton;
    this.notice = options.notice;
    this.runID = "";
    this.offset = 0;
    this.text = "";
    this.rendered = "";
    this.follow = true;
    this.bind();
    this.pre.textContent = t("log.empty");
  }

  bind() {
    this.copyButton?.addEventListener("click", async () => {
      try {
        await copyText(this.text || "");
        flashButtonText(this.copyButton, t("common.copied"));
      } catch (error) {
        showNotice(this.notice, t("notice.copyFailed", { error: error.message }), "error");
      }
    });
    this.followButton?.addEventListener("click", () => {
      this.follow = !this.follow;
      this.renderFollowButton();
      if (this.follow) this.scrollToEnd();
    });
    this.renderFollowButton();
  }

  renderFollowButton() {
    if (!this.followButton) return;
    this.followButton.textContent = this.follow ? t("action.followOn") : t("action.followOff");
    this.followButton.classList.toggle("active", this.follow);
    this.followButton.setAttribute("aria-pressed", this.follow ? "true" : "false");
  }

  select(runID) {
    if (this.runID === runID) return;
    this.runID = runID;
    this.offset = 0;
    this.text = "";
    this.rendered = "";
    this.pre.textContent = runID ? t("log.loading") : t("log.empty");
  }

  async refresh() {
    if (!this.runID) return;
    const response = await getText("/api/runs/" + encodeURIComponent(this.runID) + "/log?offset=" + this.offset);
    const next = response.headers.get("X-Builda-Log-Offset");
    if (next !== null) this.offset = Number(next) || this.offset;
    if (response.body) this.text += response.body;
    this.paint();
  }

  paint() {
    const display = this.text || t("log.unavailable");
    if (display === this.rendered) return;
    this.rendered = display;
    this.pre.innerHTML = renderLogText(display);
    if (this.follow) this.scrollToEnd();
  }

  scrollToEnd() {
    this.pre.scrollTop = this.pre.scrollHeight;
  }
}
