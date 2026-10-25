// Applies the dark theme before the first paint, if it was chosen (see
// src/lib/theme.ts). A file, not an inline script: the CSP allows no inline
// code.
try {
  if (localStorage.getItem("kubexray.theme") === "dark") document.documentElement.dataset.theme = "dark";
} catch {
  // storage blocked: the light default
}
