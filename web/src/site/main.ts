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

// The live video in the player section: the theme's take, playing only on
// screen, and only on a click for people who'd rather not see motion.
const video = document.querySelector<HTMLVideoElement>("#demo-video");
if (video) {
  const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
  let onScreen = false;
  const pick = () => {
    const take = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
    if (video.dataset.take === take) return;
    video.dataset.take = take;
    video.poster = `/media/demo-${take}.jpg`;
    video.src = `/media/demo-${take}.mp4`;
    if (onScreen && !still) void video.play().catch(() => undefined);
  };
  pick();
  new MutationObserver(pick).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  if (still) video.controls = true;
  else
    new IntersectionObserver(([entry]) => {
      onScreen = !!entry?.isIntersecting;
      if (onScreen) void video.play().catch(() => undefined);
      else video.pause();
    }).observe(video);
}
