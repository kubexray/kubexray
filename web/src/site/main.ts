// kubexray.com: the product site. Static; the only code is the theme toggle
// and copy buttons.
import "../fonts";
import "./site.css";
import { applyTheme, type Theme } from "../lib/theme";

const toggle = document.querySelector<HTMLButtonElement>("[data-theme-toggle]");
// What the page shows (public/theme.js applied the saved choice).
const shown = (): Theme => (document.documentElement.dataset.theme === "dark" ? "dark" : "light");
const label = () => {
  const next = shown() === "light" ? "dark" : "light";
  toggle?.setAttribute("aria-label", `Switch to ${next} mode`);
  toggle?.setAttribute("title", `Switch to ${next} mode`);
};
label();
toggle?.addEventListener("click", () => {
  applyTheme(shown() === "light" ? "dark" : "light");
  label();
});

for (const button of document.querySelectorAll<HTMLButtonElement>("[data-copy]")) {
  button.addEventListener("click", () => {
    const text = button.parentElement?.querySelector("code")?.textContent ?? "";
    navigator.clipboard.writeText(text).then(
      () => {
        button.textContent = "Copied";
        setTimeout(() => (button.textContent = "Copy"), 1500);
      },
      () => prompt("Copy this command", text),
    );
  });
}

// The Map | Changes switch over the player screenshots (a tab list).
const tabs = [...document.querySelectorAll<HTMLButtonElement>('.still-tabs [role="tab"]')];
const select = (tab: HTMLButtonElement) => {
  for (const t of tabs) {
    const on = t === tab;
    t.setAttribute("aria-selected", String(on));
    t.tabIndex = on ? 0 : -1;
    const panel = document.getElementById(t.getAttribute("aria-controls") ?? "");
    if (panel) panel.hidden = !on;
  }
};
for (const [i, tab] of tabs.entries()) {
  tab.addEventListener("click", () => select(tab));
  tab.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    const next = tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
    if (!next) return;
    select(next);
    next.focus();
  });
}
