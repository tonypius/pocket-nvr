// theme.js — U12: dark/light with persistence + no-flash boot (the boot
// snippet lives in index.html <head>; this module handles the toggle).
const KEY = "pnvr_theme";

export function currentTheme() {
  return document.documentElement.dataset.theme || "dark";
}

export function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = t === "light" ? "#f4f5f7" : "#050505";
  const ico = document.getElementById("theme-ico");
  if (ico) ico.textContent = t === "light" ? "☀" : "☾";
  try { localStorage.setItem(KEY, t); } catch (e) { /* private mode */ }
}

export function toggleTheme() {
  applyTheme(currentTheme() === "light" ? "dark" : "light");
}

export function initTheme() {
  applyTheme(currentTheme());
  document.getElementById("theme-btn")?.addEventListener("click", toggleTheme);
  document.getElementById("theme-btn2")?.addEventListener("click", toggleTheme);
}
