// Controller API client. Every state-changing browser request carries the
// CSRF token of the current session, and an expired session sends the browser
// back to the login page instead of failing silently.
let csrfToken = "";

export function setCSRF(token) {
  csrfToken = String(token || "");
}

export function csrf() {
  return csrfToken;
}

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function send(method, path, body, options = {}) {
  const headers = {};
  let payload;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  if (method !== "GET" && method !== "HEAD" && csrfToken) {
    headers["X-Builda-CSRF"] = csrfToken;
  }
  const response = await fetch(path, { method, headers, body: payload, credentials: "same-origin" });
  if (response.status === 401 && !options.allowUnauthorized) {
    window.location.href = "/login";
    throw new ApiError(401, "authentication required");
  }
  if (!response.ok) {
    throw new ApiError(response.status, await errorMessage(response));
  }
  if (options.text) {
    return { body: await response.text(), headers: response.headers };
  }
  if (response.status === 204) return null;
  const text = await response.text();
  return text ? JSON.parse(text) : null;
}

async function errorMessage(response) {
  const text = await response.text();
  try {
    const parsed = JSON.parse(text);
    if (parsed && parsed.error) return String(parsed.error);
  } catch {
    // Fall through to the raw body.
  }
  return text.trim() || response.statusText || "request failed";
}

export function getJSON(path, options) {
  return send("GET", path, undefined, options);
}

export function postJSON(path, body, options) {
  return send("POST", path, body, options);
}

export function putJSON(path, body, options) {
  return send("PUT", path, body, options);
}

export function deleteJSON(path, options) {
  return send("DELETE", path, undefined, options);
}

export function getText(path) {
  return send("GET", path, undefined, { text: true });
}

// query builds a query string from defined, non-empty values.
export function query(values) {
  const params = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => {
    if (value === undefined || value === null || value === "") return;
    params.set(key, String(value));
  });
  const encoded = params.toString();
  return encoded ? "?" + encoded : "";
}
