import { getText } from "./api.js";
import { renderLogLine, showNotice } from "./format.js";
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
    this.wrapButton = options.wrapButton;
    this.updateNode = options.updateNode;
    this.notice = options.notice;
    this.runID = "";
    this.offset = 0;
    this.text = "";
    this.rendered = "";
    this.renderedLines = [];
    this.generation = 0;
    this.requests = new Map();
    this.follow = true;
    this.wrap = true;
    this.bind();
    this.pre.textContent = t("log.empty");
    this.pre.dataset.wrap = "on";
    this.pre.dataset.empty = "true";
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
      if (this.follow) {
        this.updateNode && (this.updateNode.textContent = "");
        this.scrollToEnd();
      }
    });
    this.wrapButton?.addEventListener("click", () => {
      this.wrap = !this.wrap;
      this.pre.dataset.wrap = this.wrap ? "on" : "off";
      this.renderControls();
    });
    this.pre?.addEventListener("scroll", () => {
      const atEnd = this.pre.scrollHeight - this.pre.scrollTop - this.pre.clientHeight < 24;
      if (this.follow && !atEnd) {
        this.follow = false;
        this.renderFollowButton();
      }
    });
    this.renderControls();
  }

  renderFollowButton() {
    if (!this.followButton) return;
    this.followButton.textContent = this.follow ? t("action.followOn") : t("action.followOff");
    this.followButton.classList.toggle("active", this.follow);
    this.followButton.setAttribute("aria-pressed", this.follow ? "true" : "false");
    this.followButton.setAttribute("aria-label", this.follow ? t("action.followOn") : t("action.followOff"));
  }

  renderControls() {
    this.renderFollowButton();
    if (this.wrapButton) {
      this.wrapButton.textContent = t(this.wrap ? "log.wrapOn" : "log.wrapOff");
      this.wrapButton.setAttribute("aria-pressed", this.wrap ? "true" : "false");
    }
  }

  localeChanged() {
    this.renderControls();
    if (!this.text) {
      this.rendered = "";
      this.renderedLines = [];
      this.pre.replaceChildren();
      this.paint();
    }
  }

  select(runID) {
    if (this.runID === runID) return;
    this.runID = runID;
    this.generation += 1;
    this.offset = 0;
    this.text = "";
    this.rendered = "";
    this.renderedLines = [];
    if (this.updateNode) this.updateNode.textContent = "";
    this.pre.dataset.empty = "true";
    this.pre.textContent = runID ? t("log.loading") : t("log.empty");
  }

  async refresh() {
    if (!this.runID) return;
    const runID = this.runID;
    const generation = this.generation;
    const key = generation + ":" + this.offset;
    if (this.requests.has(key)) return this.requests.get(key);
    const request = getText("/api/runs/" + encodeURIComponent(runID) + "/log?offset=" + this.offset).then((response) => {
      if (generation !== this.generation || runID !== this.runID) return;
      const next = response.headers.get("X-Builda-Log-Offset");
      if (next !== null) this.offset = Number(next) || this.offset;
      const previousLength = this.text.length;
      if (response.body) this.text += response.body;
      this.paint();
      if (!this.follow && this.text.length > previousLength && this.updateNode) {
        const added = this.text.slice(previousLength);
        const lines = Math.max(1, added.split("\n").length - (added.endsWith("\n") ? 1 : 0));
        this.updateNode.textContent = t("log.newOutput", { count: lines });
      }
    });
    this.requests.set(key, request);
    try {
      return await request;
    } finally {
      this.requests.delete(key);
    }
  }

  paint() {
    const display = this.text || t("log.unavailable");
    this.pre.dataset.empty = this.text ? "false" : "true";
    if (display === this.rendered) return;
    const lines = display.split("\n");
    if (!this.renderedLines.length) this.pre.replaceChildren();
    const common = this.renderedLines.reduce((count, line, index) => {
      return count === index && lines[index] === line ? count + 1 : count;
    }, 0);
    while (this.pre.children.length > common) this.pre.lastElementChild.remove();
    for (let index = common; index < lines.length; index += 1) {
      const template = document.createElement("template");
      template.innerHTML = renderLogLine(lines[index]);
      const node = template.content.firstElementChild;
      node.dataset.logLine = String(index);
      this.pre.append(node);
    }
    this.renderedLines = lines;
    this.rendered = display;
    if (this.follow) this.scrollToEnd();
  }

  scrollToEnd() {
    this.pre.scrollTop = this.pre.scrollHeight;
  }
}
