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
