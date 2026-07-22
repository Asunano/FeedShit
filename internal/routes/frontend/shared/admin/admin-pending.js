// admin-pending.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.loadPending = async function() {
    var pf = document.getElementById('pendingProjectFilter');
    var project = pf ? pf.value : '';
    var kw = document.getElementById('pendingKeyword');
    var keyword = kw ? kw.value.trim() : '';
    var url = '/api/v1/admin/feedbacks?status=pending&limit=200';
    if (project) url += '&project=' + encodeURIComponent(project);
    if (keyword) url += '&keyword=' + encodeURIComponent(keyword);
    var resp = await api(url);
    if (!resp) return;
    var d = await resp.json();
    var items = d.feedbacks || [];
    var tbody = document.getElementById('pendingTable');
    if (!tbody) return;
    if (!items.length) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty">暂无待审批反馈 🎉</td></tr>';
    } else {
      tbody.innerHTML = items.map(function(fb){
        var pname = (d.project_names && d.project_names[fb.project_id]) || fb.project_id;
        return '<tr data-id="' + fb.id + '">' +
          '<td class="cb-col"><input type="checkbox" class="pending-cb" value="' + fb.id + '"></td>' +
          '<td>' + fb.id + '</td>' +
          '<td>' + esc(pname) + '</td>' +
          '<td>' + esc(fb.title) + '</td>' +
          '<td>' + esc(fb.priority || '') + '</td>' +
          '<td>' + esc(fb.created_at ? String(fb.created_at).slice(0,10) : '') + '</td>' +
          '<td><button class="btn-sm" data-click="openPendingDetail" data-args="' + fb.id + '">打开</button></td>' +
          '</tr>';
      }).join('');
    }
    var cnt = document.getElementById('pendingCount');
    if (cnt) cnt.textContent = '共 ' + (d.total || items.length) + ' 条';
    var sel = document.getElementById('pendingProjectFilter');
    if (sel) {
      var cur = sel.value;
      sel.innerHTML = '<option value="">全部项目</option>';
      (d.projects || []).forEach(function(p){
        var opt = document.createElement('option');
        opt.value = p; opt.textContent = p;
        if (p === cur) opt.selected = true;
        sel.appendChild(opt);
      });
    }
    loadPendingCount();
  }





window.togglePendingSelectAll = function(checked) {
    document.querySelectorAll('#pendingTable .pending-cb').forEach(function(cb){ cb.checked = !!checked; });
  }




