// admin-audit.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.applyAuditFilter = function() {
    loadAuditLogs();
  }

window.exportAuditCSV = function() {
    // GET download (server sets Content-Disposition: attachment); same filters as the list view.
    window.location.href = '/api/v1/admin/audit/export?' + auditQueryString().replace(/^&/, '');
  }

