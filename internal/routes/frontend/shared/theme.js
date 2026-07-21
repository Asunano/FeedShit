/* FeedShit 统一主题管理（亮 / 暗 / 跟随系统）
 * 单按钮循环切换：light → dark → system → light ...
 * 图标为内联 SVG（太阳 / 月亮 / 月亮+右下角 A 表示 auto），stroke=currentColor 自动适配明暗。
 * CSP 合规：仅用 addEventListener，无内联事件。
 * localStorage key 复用 'feedshit-theme'。
 */
(function () {
  "use strict";
  var THEME_KEY = "feedshit-theme";
  var ORDER = ["light", "dark", "system"];

  function getStored() { try { return localStorage.getItem(THEME_KEY); } catch (e) { return null; } }
  function setStored(v) {
    try { if (v === "system") localStorage.removeItem(THEME_KEY); else localStorage.setItem(THEME_KEY, v); } catch (e) {}
  }
  function current() { return getStored() || "system"; }

  function apply(theme) {
    if (theme === "system") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", theme);
  }
  function set(theme) { setStored(theme); apply(theme); render(); }
  function next() {
    var cur = current();
    var i = ORDER.indexOf(cur);
    if (i < 0) i = ORDER.indexOf("system");
    return ORDER[(i + 1) % ORDER.length];
  }

  // 内联 SVG 图标（stroke=currentColor，明暗自动适配）
  var ICONS = {
    light: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><line x1="12" y1="2" x2="12" y2="5"/><line x1="12" y1="19" x2="12" y2="22"/><line x1="2" y1="12" x2="5" y2="12"/><line x1="19" y1="12" x2="22" y2="12"/><line x1="4.2" y1="4.2" x2="6.3" y2="6.3"/><line x1="17.7" y1="17.7" x2="19.8" y2="19.8"/><line x1="4.2" y1="19.8" x2="6.3" y2="17.7"/><line x1="17.7" y1="6.3" x2="19.8" y2="4.2"/></svg>',
    dark: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>',
    system: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><line x1="12" y1="2" x2="12" y2="5"/><line x1="12" y1="19" x2="12" y2="22"/><line x1="2" y1="12" x2="5" y2="12"/><line x1="19" y1="12" x2="22" y2="12"/><line x1="4.2" y1="4.2" x2="6.3" y2="6.3"/><line x1="17.7" y1="17.7" x2="19.8" y2="19.8"/><line x1="4.2" y1="19.8" x2="6.3" y2="17.7"/><line x1="17.7" y1="6.3" x2="19.8" y2="4.2"/><text x="12" y="15.4" text-anchor="middle" font-size="9" font-family="sans-serif" font-weight="700" fill="currentColor" stroke="none">A</text></svg>'
  };

  function render() {
    var theme = current();
    var label = theme === "light" ? "浅色" : theme === "dark" ? "深色" : "跟随系统";
    document.querySelectorAll(".theme-toggle").forEach(function (btn) {
      var icon = btn.querySelector(".theme-toggle__icon");
      if (icon) icon.innerHTML = ICONS[theme] || ICONS.system;
      btn.setAttribute("aria-label", "切换主题（当前：" + label + "）");
      btn.setAttribute("title", "主题：" + label + "（点击切换）");
    });
  }

  // 初始化（在 <head> 同步执行，避免主题闪烁）
  apply(current());

  // 跟随系统：仅当用户选择“系统”时，系统配色变化才生效
  if (window.matchMedia) {
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    var handler = function () { if (current() === "system") apply("system"); };
    if (mq.addEventListener) mq.addEventListener("change", handler);
    else if (mq.addListener) mq.addListener(handler);
  }

  // 绑定切换按钮（CSP 安全：无内联事件）
  function wire() {
    document.querySelectorAll(".theme-toggle").forEach(function (btn) {
      btn.addEventListener("click", function () { set(next()); });
    });
    render();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", wire);
  else wire();

  // 对外 API（供后续扩展 / 向后兼容）
  window.themeManager = { set: set, apply: apply, get: current };
  window.toggleTheme = function () { set(next()); };
})();
