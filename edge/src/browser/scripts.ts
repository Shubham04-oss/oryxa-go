// The page scripts the browser tools run, copied from
// agents/primary/internal/agentcorebrowser/cdp.go so a snapshot's selectors,
// what counts as actionable, and the refusal to fill secrets are the same on
// Cloudflare as on AgentCore. Change both together.

export const MAX_SNAPSHOT_TEXT_BYTES = 64 * 1024;
export const MAX_SNAPSHOT_ELEMENTS = 200;
export const MAX_ELEMENT_TEXT_BYTES = 2 * 1024;
export const MAX_SELECTOR_BYTES = 4 * 1024;
export const MAX_URL_BYTES = 8 * 1024;
export const MAX_FILL_BYTES = 100 * 1024;

export const SNAPSHOT_EXPRESSION = String.raw`(() => {
  const escape = (value) => globalThis.CSS?.escape ? CSS.escape(value) : String(value).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  const selectorFor = (element) => {
    if (element.id) return "#" + escape(element.id);
    for (const attribute of ["data-testid", "data-test", "name", "aria-label"]) {
      const value = element.getAttribute(attribute);
      if (value) return element.tagName.toLowerCase() + "[" + attribute + "=" + JSON.stringify(value) + "]";
    }
    const parts = [];
    let current = element;
    while (current && current.nodeType === Node.ELEMENT_NODE && current !== document.documentElement && parts.length < 6) {
      const tag = current.tagName.toLowerCase();
      const siblings = current.parentElement ? Array.from(current.parentElement.children).filter((node) => node.tagName === current.tagName) : [];
      const suffix = siblings.length > 1 ? ":nth-of-type(" + (siblings.indexOf(current) + 1) + ")" : "";
      parts.unshift(tag + suffix);
      current = current.parentElement;
    }
    return parts.join(" > ");
  };
  const isVisible = (element) => {
    const style = getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const candidates = Array.from(document.querySelectorAll("a[href], button, input, textarea, select, [role=button], [role=link], [contenteditable=true]"));
  const visible = candidates.filter(isVisible);
  const elements = visible.slice(0, 200).map((element) => ({
    selector: selectorFor(element),
    tag: element.tagName.toLowerCase(),
    role: element.getAttribute("role") || "",
    type: element.getAttribute("type") || "",
    name: element.getAttribute("aria-label") || element.getAttribute("name") || element.getAttribute("placeholder") || "",
    text: (element.innerText || element.textContent || "").trim().slice(0, 2048),
    href: element instanceof HTMLAnchorElement ? element.href : "",
    disabled: Boolean(element.disabled || element.getAttribute("aria-disabled") === "true")
  }));
  return {
    title: document.title || "",
    url: location.href,
    text: (document.body?.innerText || "").slice(0, 65537),
    elements,
    elements_truncated: visible.length > elements.length
  };
})()`;

export function clickExpression(selector: string): string {
  return `(() => {
  const element = document.querySelector(${JSON.stringify(selector)});
  if (!element) throw new Error("No element matches the CSS selector");
  if (!(element instanceof HTMLElement)) throw new Error("Matched node is not an HTML element");
  element.scrollIntoView({block: "center", inline: "center"});
  element.click();
  return true;
})()`;
}

export function fillExpression(selector: string, text: string): string {
  const value = JSON.stringify(text);
  return `(() => {
  const element = document.querySelector(${JSON.stringify(selector)});
  if (!element) throw new Error("No element matches the CSS selector");
  if (!(element instanceof HTMLElement)) throw new Error("Matched node is not an HTML element");
  if (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) {
    if (element.disabled || element.readOnly) throw new Error("Matched field is disabled or read-only");
    if (element instanceof HTMLInputElement && ["password", "file"].includes(element.type.toLowerCase())) {
      throw new Error("Browser tools never fill password or file-input fields");
    }
    const autocomplete = (element.getAttribute("autocomplete") || "").toLowerCase();
    if (["current-password", "new-password", "one-time-code", "cc-number", "cc-csc", "cc-exp", "cc-exp-month", "cc-exp-year"].includes(autocomplete)) {
      throw new Error("Browser tools never fill sensitive or payment fields");
    }
    const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(prototype, "value")?.set;
    if (setter) setter.call(element, ${value}); else element.value = ${value};
  } else if (element.isContentEditable) {
    element.textContent = ${value};
  } else {
    throw new Error("Matched element is not an input, textarea, or editable element");
  }
  element.focus();
  element.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: ${value}}));
  element.dispatchEvent(new Event("change", {bubbles: true}));
  return true;
})()`;
}
