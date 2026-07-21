/* FeedShit 统一事件委托器（CSP 安全，外部 'self' 脚本，无需 nonce）
 * 加载一次（在 <head> 内），为所有页面提供 data-click / data-change 委托。
 * 页面 handler 两种注册方式：
 *   1) 暴露为全局函数 window.fn = ... （自动可被 window[name] 解析）
 *   2) 显式调用 UX.register('name', fn)
 * 移除逻辑统一用 data-remove-closest / data-remove-parent。
 */
(function () {
  "use strict";

  var registry = {};
  window.UX = {
    register: function (name, fn) { registry[name] = fn; },
    registerAll: function (map) {
      if (!map) return;
      for (var k in map) {
        if (Object.prototype.hasOwnProperty.call(map, k)) registry[k] = map[k];
      }
    },
    resolve: function (name) {
      if (registry[name]) return registry[name];
      if (typeof window[name] === "function") return window[name];
      return null;
    }
  };

  // 解析 data-args：支持 this / this.x / event / true|false / 引号字符串（含逗号）/ 数字
  function resolveArgs(raw, el, e) {
    if (!raw) return [];
    var parts = [], buf = "", q = null, i, c;
    for (i = 0; i < raw.length; i++) {
      c = raw[i];
      if (q) { buf += c; if (c === q) q = null; }
      else if (c === '"' || c === "'") { buf += c; q = c; }
      else if (c === ",") { parts.push(buf); buf = ""; }
      else buf += c;
    }
    if (buf !== "") parts.push(buf);
    return parts.map(function (t) {
      t = t.trim();
      if (t === "this") return el;
      if (t.indexOf("this.") === 0) return el[t.slice(5)];
      if (t === "event") return e;
      if (t === "true") return true;
      if (t === "false") return false;
      if ((t[0] === '"' || t[0] === "'") && t.slice(-1) === t[0]) return t.slice(1, -1);
      if (t !== "" && !isNaN(Number(t))) return Number(t);
      return t;
    });
  }

  document.addEventListener("click", function (e) {
    var el = e.target.closest("[data-remove-closest]");
    if (el && el.hasAttribute("data-remove-closest")) {
      var anc = el.closest(el.getAttribute("data-remove-closest"));
      if (anc) anc.remove();
      return;
    }
    el = e.target.closest("[data-remove-parent]");
    if (el && el.hasAttribute("data-remove-parent")) {
      if (el.parentNode) el.parentNode.remove();
      return;
    }
    el = e.target.closest("[data-click]");
    if (!el) return;
    var fn = window.UX.resolve(el.getAttribute("data-click"));
    if (typeof fn === "function") {
      if (el.tagName === "A" || el.type === "submit") e.preventDefault();
      fn.apply(null, resolveArgs(el.getAttribute("data-args"), el, e).concat([e]));
    }
  });

  document.addEventListener("change", function (e) {
    var el = e.target.closest("[data-change]");
    if (!el) return;
    var fn = window.UX.resolve(el.getAttribute("data-change"));
    if (typeof fn === "function") {
      fn.apply(null, resolveArgs(el.getAttribute("data-args"), el, e).concat([e]));
    }
  });
})();
