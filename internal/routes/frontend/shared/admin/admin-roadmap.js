// admin-roadmap.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.RoadmapItems = {};

function rmStatusText(s) { return ({ planning: '规划中', in_progress: '进行中', released: '已发布' })[s] || s || '规划中'; }
function fmtDate(ts) {
  if (!ts) return '';
  var d = new Date(ts * 1000);
  var m = ('0' + (d.getUTCMonth() + 1)).slice(-2);
  var day = ('0' + d.getUTCDate()).slice(-2);
  return d.getUTCFullYear() + '-' + m + '-' + day;
}
function parseDate(v) {
  if (!v) return 0;
  var p = v.split('-');
  if (p.length !== 3) return 0;
  return Math.floor(Date.UTC(parseInt(p[0], 10), parseInt(p[1], 10) - 1, parseInt(p[2], 10)) / 1000);
}

window.loadRoadmap = async function () {
  var st = document.getElementById('rmStatusFilter');
  var pub = document.getElementById('rmPublicFilter');
  var kw = document.getElementById('rmKeyword');
  var url = '/api/v1/admin/roadmap?limit=200';
  if (st && st.value) url += '&status=' + encodeURIComponent(st.value);
  if (pub && pub.value !== '') url += '&public=' + encodeURIComponent(pub.value);
  if (kw && kw.value.trim()) url += '&keyword=' + encodeURIComponent(kw.value.trim());
  var resp = await api(url);
  if (!resp) return;
  var d = await resp.json();
  var items = d.items || [];
  window.RoadmapItems = {};
  items.forEach(function (it) { window.RoadmapItems[it.id] = it; });
  var tbody = document.getElementById('rmTable');
  if (!tbody) return;
  if (!items.length) {
    tbody.innerHTML = '<tr><td colspan="11" class="empty">暂无路线图条目</td></tr>';
  } else {
    tbody.innerHTML = items.map(function (it) {
      var stOpts = ['planning', 'in_progress', 'released'].map(function (s) {
        return '<option value="' + s + '"' + (it.roadmap_status === s ? ' selected' : '') + '>' + rmStatusText(s) + '</option>';
      }).join('');
      var pinned = it.roadmap_order > 0;
      var mention = it.mention_count > 0 ? ('<span class="badge planning">+' + it.mention_count + '</span>') : '0';
      return '<tr data-id="' + it.id + '">' +
        '<td><input type="checkbox" class="rm-sel" data-change="onRmRowSelect" data-args="' + it.id + ', this.checked"></td>' +
        '<td>' + it.id + '</td>' +
        '<td>' + esc(it.project_slug) + '</td>' +
        '<td>' + esc(it.title) + '</td>' +
        '<td>' + esc(it.category || '-') + '</td>' +
        '<td><select class="rm-status-sel" data-change="onRmStatusChange" data-args="' + it.id + ', this.value">' + stOpts + '</select></td>' +
        '<td><label class="toggle"><input type="checkbox" class="rm-pub-cb" data-change="onRmPublicChange" data-args="' + it.id + ', this.checked"' + (it.public_on_roadmap ? ' checked' : '') + '><span>' + (it.public_on_roadmap ? '公开' : '隐藏') + '</span></label></td>' +
        '<td>' + mention + '</td>' +
        '<td>' + it.roadmap_order + (pinned ? ' 📌' : '') + '</td>' +
        '<td>' + esc(it.updated_at ? String(it.updated_at).slice(0, 10) : '') + '</td>' +
        '<td><button class="btn-sm" data-click="onRmTogglePin" data-args="' + it.id + '">' + (pinned ? '取消置顶' : '置顶') + '</button> <button class="btn-sm" data-click="onRmOpenCurate" data-args="' + it.id + '">策展</button> <button class="btn-sm" data-click="openPendingDetail" data-args="' + it.id + '">打开</button></td>' +
        '</tr>';
    }).join('');
  }
  var cnt = document.getElementById('rmCount');
  if (cnt) cnt.textContent = '共 ' + (d.total || items.length) + ' 条';
  updateRmBulkBar();
};

window.onRmStatusChange = async function (id, val) {
  var row = document.querySelector('tr[data-id="' + id + '"]');
  var pub = row ? row.querySelector('.rm-pub-cb').checked : false;
  var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/roadmap', { method: 'PUT', body: JSON.stringify({ status: val, public: pub }) });
  if (!d) return;
  if (d.error) { showToast(d.error, 'error'); return; }
  showToast('已更新', 'success');
};

window.onRmPublicChange = async function (id, checked) {
  var row = document.querySelector('tr[data-id="' + id + '"]');
  var st = row ? row.querySelector('.rm-status-sel').value : '';
  var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/roadmap', { method: 'PUT', body: JSON.stringify({ status: st, public: checked }) });
  if (!d) return;
  if (d.error) { showToast(d.error, 'error'); return; }
  showToast('已更新', 'success');
};

window.onRmRowSelect = function () { updateRmBulkBar(); };
window.onRmSelectAll = function (checked) {
  document.querySelectorAll('#rmTable .rm-sel').forEach(function (cb) { cb.checked = checked; });
  updateRmBulkBar();
};

function selectedRoadmapIds() {
  var ids = [];
  document.querySelectorAll('#rmTable .rm-sel:checked').forEach(function (cb) {
    var tr = cb.closest('tr');
    if (tr) ids.push(parseInt(tr.getAttribute('data-id'), 10));
  });
  return ids;
}

function updateRmBulkBar() {
  var ids = selectedRoadmapIds();
  var bar = document.getElementById('rmBulkBar');
  if (!bar) return;
  bar.style.display = ids.length ? 'flex' : 'none';
  var c = document.getElementById('rmBulkCount');
  if (c) c.textContent = '已选 ' + ids.length + ' 条';
}

window.onRmBulkApply = async function () {
  var ids = selectedRoadmapIds();
  if (!ids.length) { showToast('请先选择条目', 'error'); return; }
  var status = document.getElementById('rmBulkStatus').value;
  var pub = document.getElementById('rmBulkPublic').checked;
  var d = await apiJSON('/api/v1/admin/roadmap/bulk', { method: 'POST', body: JSON.stringify({ ids: ids, status: status, public: pub }) });
  if (!d) return;
  if (d.error) { showToast(d.error, 'error'); return; }
  showToast(d.message || '已批量更新', 'success');
  loadRoadmap();
};

window.onRmBulkClear = function () {
  document.querySelectorAll('#rmTable .rm-sel').forEach(function (cb) { cb.checked = false; });
  updateRmBulkBar();
};

window.onRmTogglePin = async function (id) {
  var it = window.RoadmapItems[id];
  var order = (it && it.roadmap_order > 0) ? 0 : 9999;
  var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/roadmap/meta', { method: 'PUT', body: JSON.stringify({ order: order }) });
  if (!d) return;
  if (d.error) { showToast(d.error, 'error'); return; }
  showToast('已更新', 'success');
  loadRoadmap();
};

window.onRmOpenCurate = function (id) {
  var it = window.RoadmapItems[id];
  if (!it) return;
  document.getElementById('rmCurateId').value = id;
  document.getElementById('rmCurateTitle').textContent = it.title;
  document.getElementById('rmCurateOrder').value = it.roadmap_order || 0;
  document.getElementById('rmCurateTarget').value = fmtDate(it.target_date);
  document.getElementById('rmCurateOwner').value = it.owner || '';
  document.getElementById('rmCurateRelease').value = it.release || '';
  document.getElementById('rmCurateModal').classList.add('active');
};

window.closeRmCurate = function () {
  document.getElementById('rmCurateModal').classList.remove('active');
};

window.saveRmCurate = async function () {
  var id = document.getElementById('rmCurateId').value;
  var body = {
    order: parseInt(document.getElementById('rmCurateOrder').value) || 0,
    target_date: parseDate(document.getElementById('rmCurateTarget').value),
    owner: document.getElementById('rmCurateOwner').value.trim(),
    release: document.getElementById('rmCurateRelease').value.trim()
  };
  var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/roadmap/meta', { method: 'PUT', body: JSON.stringify(body) });
  if (!d) return;
  if (d.error) { showToast(d.error, 'error'); return; }
  showToast('已保存', 'success');
  closeRmCurate();
  loadRoadmap();
};
