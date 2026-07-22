// admin-kb.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.loadFaqs = async function() {
    var sel = document.getElementById('kbProject');
    kbProjectSlug = sel ? sel.value : '';
    var body = document.getElementById('kbBody');
    var wrap = document.getElementById('kbTableWrap');
    var empty = document.getElementById('kbEmpty');
    if (!kbProjectSlug) {
      kbCache = [];
      if (body) body.innerHTML = '';
      if (wrap) wrap.style.display = 'none';
      if (empty) { empty.style.display = ''; empty.textContent = '请先在上方选择一个项目。'; }
      return;
    }
    var d = await apiJSON('/api/v1/admin/projects/' + encodeURIComponent(kbProjectSlug) + '/faqs');
    if (!d) return;
    var faqs = d.faqs || [];
    kbCache = faqs;
    if (faqs.length === 0) {
      if (body) body.innerHTML = '';
      if (wrap) wrap.style.display = 'none';
      if (empty) { empty.style.display = ''; empty.textContent = '该项目暂无 FAQ，点击「新增 FAQ」创建第一条。'; }
      return;
    }
    if (empty) empty.style.display = 'none';
    if (wrap) wrap.style.display = '';
    if (!body) return;
    var html = '';
    faqs.forEach(function(f) {
      var ans = f.answer || '';
      var preview = ans.length > 60 ? ans.substring(0, 60) + '…' : ans;
      html += '<tr>'
        + '<td>' + esc(f.question) + '</td>'
        + '<td style="color:var(--tag-fg);max-width:360px;white-space:pre-wrap;word-break:break-word">' + esc(preview) + '</td>'
        + '<td>' + (f.sort_order || 0) + '</td>'
        + '<td>' + (f.is_active ? '<span style="color:var(--success-fg)">启用</span>' : '<span style="color:var(--hint)">停用</span>') + '</td>'
        + '<td style="text-align:center">' + (f.view_count || 0) + '</td>'
        + '<td style="text-align:center">' + (f.useful_votes || 0) + '</td>'
        + '<td>'
        + '<button class="btn-sm" data-click="openFaqModal" data-args="' + f.id + '">编辑</button> '
        + '<button class="btn-sm btn-danger" data-click="deleteFaq" data-args="' + f.id + '">删除</button>'
        + '</td>'
        + '</tr>';
    });
    body.innerHTML = html;
  }

  

window.openFaqModal = function(id) {
    var editId = id || 0;
    document.getElementById('faqModalTitle').textContent = editId ? '编辑 FAQ' : '新增 FAQ';
    document.getElementById('faqEditId').value = editId;
    if (editId) {
      var f = null;
      for (var i = 0; i < kbCache.length; i++) { if (kbCache[i].id === editId) { f = kbCache[i]; break; } }
      document.getElementById('faqQuestion').value = f ? (f.question || '') : '';
      document.getElementById('faqAnswer').value = f ? (f.answer || '') : '';
      document.getElementById('faqSort').value = f ? (f.sort_order || 0) : 0;
      document.getElementById('faqActive').checked = f ? !!f.is_active : true;
    } else {
      document.getElementById('faqQuestion').value = '';
      document.getElementById('faqAnswer').value = '';
      document.getElementById('faqSort').value = 0;
      document.getElementById('faqActive').checked = true;
    }
    document.getElementById('faqModal').style.display = 'flex';
  }

  

window.closeFaqModal = function() {
    document.getElementById('faqModal').style.display = 'none';
  }

  

window.previewFaq = async function() {
    var box = document.getElementById('faqPreview');
    if (!box) return;
    var md = document.getElementById('faqAnswer').value;
    if (!md || !md.trim()) {
      box.style.display = 'block';
      box.innerHTML = '<span style="color:var(--muted)">（答案为空）</span>';
      return;
    }
    try {
      var resp = await api('/api/v1/admin/faqs/preview', {
        method: 'POST',
        headers: Object.assign({ 'Content-Type': 'application/json' }, getCsrfHeaders()),
        body: JSON.stringify({ markdown: md })
      });
      if (!resp) return;
      var d = await resp.json();
      box.style.display = 'block';
      box.innerHTML = d.html || '';
    } catch (e) { /* preview is best-effort */ }
  }

  

window.saveFaq = async function() {
    var question = document.getElementById('faqQuestion').value.trim();
    if (!question) { showToast('问题不能为空', 'error'); return; }
    var answer = document.getElementById('faqAnswer').value;
    var sortOrder = parseInt(document.getElementById('faqSort').value, 10) || 0;
    var isActive = document.getElementById('faqActive').checked;
    var editId = parseInt(document.getElementById('faqEditId').value, 10) || 0;
    var payload = { question: question, answer: answer, sort_order: sortOrder, is_active: isActive };
    var url = '/api/v1/admin/projects/' + encodeURIComponent(kbProjectSlug) + '/faqs';
    var opts = {
      method: editId ? 'PUT' : 'POST',
      headers: Object.assign({ 'Content-Type': 'application/json' }, getCsrfHeaders()),
      body: JSON.stringify(payload)
    };
    if (editId) url += '/' + editId;
    var resp = await api(url, opts);
    if (!resp) return;
    if (resp.status === 409) { showToast('该问题已存在', 'error'); return; }
    if (resp.status === 404) { showToast('FAQ 不存在', 'error'); return; }
    if (!resp.ok) {
      var err = await resp.json().catch(function() { return {}; });
      showToast(err.error || (editId ? '更新失败' : '创建失败'), 'error');
      return;
    }
    showToast(editId ? '已更新' : '已创建', 'success');
    closeFaqModal();
    await loadFaqs();
  }

  

window.exportFaqs = function() {
    if (!kbProjectSlug) { showToast('请先选择项目', 'error'); return; }
    var a = document.createElement('a');
    a.href = '/api/v1/admin/projects/' + encodeURIComponent(kbProjectSlug) + '/faqs/export';
    a.download = '';
    document.body.appendChild(a); a.click(); a.remove();
  }

window.triggerFaqCSV = function() {
    var inp = document.getElementById('faqCSVFile');
    if (inp) inp.click();
  }

window.onFaqCSVChange = async function(input) {
    if (!kbProjectSlug) { showToast('请先选择项目', 'error'); return; }
    if (!input.files || !input.files.length) return;
    var fd = new FormData();
    fd.append('file', input.files[0]);
    try {
      var resp = await api('/api/v1/admin/projects/' + encodeURIComponent(kbProjectSlug) + '/faqs/import?preview=1', {
        method: 'POST',
        headers: getCsrfHeaders(),
        body: fd
      });
      if (!resp) return;
      var d = await resp.json();
      var body = document.getElementById('faqCSVPreviewBody');
      var note = document.getElementById('faqCSVPreviewNote');
      if (!d.has_question) {
        note.textContent = '⚠️ CSV 缺少「问题」列，无法导入。请使用表头：问题,答案,排序,启用';
      } else {
        note.textContent = '预览 ' + (d.sample_rows ? d.sample_rows.length : 0) + ' 行，确认后点击「确认导入」。';
      }
      var headers = d.headers || [];
      var mapped = d.mapped || {};
      var rows = d.sample_rows || [];
      var table = '<table style="width:100%;border-collapse:collapse"><thead><tr>';
      headers.forEach(function(h) {
        table += '<th style="border:1px solid var(--border-soft);padding:3px 6px;text-align:left;white-space:nowrap">' + esc(h) + '<br><span style="color:var(--hint);font-weight:normal">' + esc(mapped[h] || h) + '</span></th>';
      });
      table += '</tr></thead><tbody>';
      rows.forEach(function(r) {
        table += '<tr>';
        r.forEach(function(cell) { table += '<td style="border:1px solid var(--border-soft);padding:3px 6px">' + esc(cell) + '</td>'; });
        table += '</tr>';
      });
      table += '</tbody></table>';
      body.innerHTML = table;
      document.getElementById('faqCSVPreview').style.display = 'block';
    } catch (e) { showToast('预览失败', 'error'); }
  }

window.closeFaqCSVPreview = function() {
    document.getElementById('faqCSVPreview').style.display = 'none';
    var inp = document.getElementById('faqCSVFile');
    if (inp) inp.value = '';
  }

window.importFaqCSV = async function() {
    if (!kbProjectSlug) return;
    var inp = document.getElementById('faqCSVFile');
    if (!inp || !inp.files || !inp.files.length) { showToast('请先选择 CSV 文件', 'error'); return; }
    var fd = new FormData();
    fd.append('file', inp.files[0]);
    var resp = await api('/api/v1/admin/projects/' + encodeURIComponent(kbProjectSlug) + '/faqs/import', {
      method: 'POST',
      headers: getCsrfHeaders(),
      body: fd
    });
    if (!resp) return;
    var d = await resp.json().catch(function() { return {}; });
    if (!resp.ok) { showToast(d.error || '导入失败', 'error'); return; }
    var msg = '已导入 ' + (d.imported || 0) + ' 条';
    if (d.skipped) msg += '，跳过 ' + d.skipped + ' 条';
    if (d.errors && d.errors.length) msg += '，' + d.errors.length + ' 条出错';
    showToast(msg, 'success');
    closeFaqCSVPreview();
    await loadFaqs();
  }

