// Light or dark (light by default), chosen in the header and remembered in
// this browser. The page follows the choice, not the system setting.
export type Theme = "light" | "dark";

const KEY = "kubexray.theme";

export function savedTheme(): Theme {
  try {
    return localStorage.getItem(KEY) === "dark" ? "dark" : "light";
  } catch {
    return "light"; // storage blocked: the default
  }
}

export function applyTheme(theme: Theme, remember = true): void {
  document.documentElement.dataset.theme = theme;
  if (!remember) return;
  try {
    localStorage.setItem(KEY, theme);
  } catch {
    // storage blocked: this page keeps it until reloaded
  }
}
