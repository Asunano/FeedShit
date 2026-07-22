// admin-settings.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.doBackup = async function() {
    var resultEl = document.getElementById('backupResult');
    resultEl.textContent = '备份中...';
    var d = await apiJSON('/api/v1/admin/backup', {method: 'POST'});
    if (!d) { resultEl.textContent = '失败'; return; }
    if (d.error) { resultEl.textContent = d.error; return; }
    resultEl.textContent = '已完成: ' + (d.path || '');
    showToast(d.message || '备份完成', 'success');
  }

window.switchSettings = function(section, btn) {
    document.querySelectorAll('.settings-nav button').forEach(function(b){ b.classList.remove('active'); });
    if (btn) btn.classList.add('active');
    ['email','account','system','tokens','emailtpl','dataops','webhooks','roadmap'].forEach(function(s){
      var el = document.getElementById('settings-'+s);
      if (el) el.style.display = s === section ? '' : 'none';
    });
    if (section === 'email') loadEmailSettings();
    if (section === 'account') loadAccountSettings();
    if (section === 'system') loadSystemSettings();
    if (section === 'tokens') loadTokens();
    if (section === 'emailtpl') loadEmailTemplate();
    if (section === 'dataops') loadDataOps();
    if (section === 'webhooks') loadWebhooks();
    if (section === 'roadmap') loadRoadmapConfig();
  }

window.loadRoadmapConfig = async function() {
  var d = await apiJSON('/api/v1/admin/config');
  if (!d || !d.config) return;
  var map = {};
  d.config.forEach(function(c){ map[c.key] = c.value; });
  var ab = document.getElementById('rmAutoBoard'); if (ab) ab.checked = map['roadmap_auto_board'] === 'true';
  var ds = document.getElementById('rmDefaultStatus'); if (ds && map['roadmap_default_status']) ds.value = map['roadmap_default_status'];
  var dp = document.getElementById('rmDefaultPublic'); if (dp) dp.checked = map['roadmap_default_public'] === 'true';
  var ap = document.getElementById('rmAutoPromote'); if (ap) ap.checked = map['roadmap_auto_promote'] === 'true';
  var aps = document.getElementById('rmAutoPromoteStatus'); if (aps && map['roadmap_auto_promote_status']) aps.value = map['roadmap_auto_promote_status'];
}

window.saveRoadmapConfig = async function() {
  var updates = [
    {key:'roadmap_auto_board', value: (document.getElementById('rmAutoBoard').checked ? 'true':'false')},
    {key:'roadmap_default_status', value: document.getElementById('rmDefaultStatus').value},
    {key:'roadmap_default_public', value: (document.getElementById('rmDefaultPublic').checked ? 'true':'false')},
    {key:'roadmap_auto_promote', value: (document.getElementById('rmAutoPromote').checked ? 'true':'false')},
    {key:'roadmap_auto_promote_status', value: document.getElementById('rmAutoPromoteStatus').value}
  ];
  var d = await apiJSON('/api/v1/admin/config', {method:'PUT', body:JSON.stringify(updates)});
  if (!d) return;
  if (d.error) { showToast(d.error, 'error'); return; }
  showToast(d.message || '已保存', 'success');
}

window.saveEmailSettings = async function() {
    var updates = [];
    ['smtp_host','smtp_port','smtp_user','smtp_pass','smtp_from','smtp_to'].forEach(function(k){
      var el = document.getElementById('ecfg_'+k);
      if (el) updates.push({key:k, value:el.value});
    });
    var nEl = document.getElementById('ecfg_notify_enable');
    if (nEl) updates.push({key:'notify_enable', value:nEl.checked?'true':'false'});
    var d = await apiJSON('/api/v1/admin/config/email', {method:'PUT', body:JSON.stringify(updates)});
    if (!d) return;
    showToast(d.message || '已保存', 'success');
  }

window.saveAccount = async function() {
    var oldPwd = document.getElementById('accOldPwd').value;
    if (!oldPwd) { showToast('请输入当前密码', 'error'); return; }
    var newPwd = document.getElementById('accNewPwd').value;
    var newPwd2 = document.getElementById('accNewPwd2').value;
    if (newPwd && newPwd !== newPwd2) { showToast('两次新密码不一致', 'error'); return; }
    if (newPwd) {
      if (newPwd.length < 8) { showToast('密码至少 8 位', 'error'); return; }
      if (!/[A-Z]/.test(newPwd)) { showToast('须包含至少一个大写字母', 'error'); return; }
      if (!/[a-z]/.test(newPwd)) { showToast('须包含至少一个小写字母', 'error'); return; }
      if (!/[0-9]/.test(newPwd)) { showToast('须包含至少一个数字', 'error'); return; }
    }
    var d = await apiJSON('/api/v1/admin/config/account', {
      method: 'PUT',
      body: JSON.stringify({ username: document.getElementById('accUser').value.trim(), new_password: newPwd, old_password: oldPwd })
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
  }

window.saveAnnouncementSettings = async function() {
    if (!document.getElementById('annContent')) { showToast('请先等待公告配置加载', 'error'); return; }
    var body = {
      enabled: document.getElementById('annEnabled').value === '1',
      level: document.getElementById('annLevel').value,
      content_type: document.getElementById('annType').value,
      content: document.getElementById('annContent').value,
      dismissible: document.getElementById('annDismiss').checked
    };
    var d = await apiJSON('/api/v1/admin/config/announcement', {method:'PUT', body:JSON.stringify(body)});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
  }

window.saveSystemSettings = async function() {
    var d = await apiJSON('/api/v1/admin/config/system', {
      method: 'PUT',
      body: JSON.stringify({
        base_url: document.getElementById('sysBase').value.trim(),
        pow_difficulty: parseInt(document.getElementById('sysPoW').value) || 4,
        rate_limit_per_hr: parseInt(document.getElementById('sysRate').value) || 3,
        webhook_url: document.getElementById('sysWebhook').value.trim(),
        webhook_type: document.getElementById('sysWebhookType').value,
        cdn_provider: document.getElementById('sysCdnProvider').value,
        trusted_proxies: document.getElementById('sysTrustedProxies').value.trim()
      })
    });
    if (!d) return;
    showToast(d.message, 'success');
  }

window.openTokenModal = function() {
    document.getElementById('tokenEditId').value = '';
    document.getElementById('tokenName').value = '';
    document.getElementById('tokenProject').value = '';
    document.getElementById('tokenRateLimit').value = '60';
    document.getElementById('tokenQuotaPerDay').value = '1000';
    document.getElementById('tokenResult').style.display = 'none';
    document.getElementById('tokenSaveBtn').style.display = '';
    document.getElementById('tokenModalTitle').textContent = '创建 API Token';
    document.getElementById('tokenModal').classList.add('active');
  }

window.closeTokenModal = function() {
    document.getElementById('tokenModal').classList.remove('active');
  }

window.saveToken = async function() {
    var name = document.getElementById('tokenName').value.trim();
    if (!name) { showToast('请输入名称', 'error'); return; }
    var projectId = document.getElementById('tokenProject').value.trim();
    var rateLimit = parseInt(document.getElementById('tokenRateLimit').value) || 0;
    var quotaPerDay = parseInt(document.getElementById('tokenQuotaPerDay').value) || 0;
    var d = await apiJSON('/api/v1/admin/api-tokens', {method:'POST', body:JSON.stringify({name:name, project_id:projectId, rate_limit:rateLimit, quota_per_day:quotaPerDay})});
    if (!d) return;
    document.getElementById('tokenValue').textContent = d.token;
    document.getElementById('tokenResult').style.display = '';
    document.getElementById('tokenSaveBtn').style.display = 'none';
    loadTokens();
  }

window.closeTokenRotateModal = function() {
    document.getElementById('tokenRotateModal').classList.remove('active');
  }

window.closeTokenStatsModal = function() {
    document.getElementById('tokenStatsModal').classList.remove('active');
  }

window.openWebhookModal = function() {
    document.getElementById('webhookModalTitle').textContent = '新建 Webhook 订阅';
    document.getElementById('webhookEditId').value = '';
    document.getElementById('whProject').value = '';
    document.getElementById('whUrl').value = '';
    document.getElementById('whSecret').value = '';
    document.getElementById('whEvents').value = '*';
    document.getElementById('whActive').checked = true;
    document.getElementById('webhookModal').classList.add('active');
  }

window.closeWebhookModal = function() {
    document.getElementById('webhookModal').classList.remove('active');
  }

window.saveWebhook = async function() {
    var id = document.getElementById('webhookEditId').value;
    var url = document.getElementById('whUrl').value.trim();
    if (!url) { showToast('URL 不能为空', 'error'); return; }
    var body = {
      project_slug: document.getElementById('whProject').value.trim(),
      url: url,
      secret: document.getElementById('whSecret').value,
      events: document.getElementById('whEvents').value.trim() || '*',
      is_active: document.getElementById('whActive').checked
    };
    var d;
    if (id) {
      d = await apiJSON('/api/v1/admin/webhooks/' + id, {method:'PUT', body:JSON.stringify(body)});
    } else {
      d = await apiJSON('/api/v1/admin/webhooks', {method:'POST', body:JSON.stringify(body)});
    }
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message || '已保存', 'success');
    closeWebhookModal();
    loadWebhooks();
  }

window.saveEmailTemplate = async function() {
    var d = await apiJSON('/api/v1/admin/config/email-template', {
      method: 'PUT',
      body: JSON.stringify({
        subject_template: document.getElementById('tplSubject').value,
        body_template: document.getElementById('tplBody').value
      })
    });
    if (!d) return;
    showToast(d.message, 'success');
  }

window.previewCSVImport = async function() {
    var fileInput = document.getElementById('csvFileInput');
    var area = document.getElementById('csvPreviewArea');
    if (!fileInput.files || fileInput.files.length === 0) {
      showToast('请选择 CSV 文件', 'error');
      return;
    }
    var formData = new FormData();
    formData.append('file', fileInput.files[0]);
    var projectId = document.getElementById('importProjectId').value;
    if (projectId) formData.append('project_id', projectId);

    var headers = getCsrfHeaders();
    var resp = await fetch('/api/v1/admin/import/csv?preview=1', {
      method: 'POST',
      headers: headers,
      body: formData
    });
    if (!resp.ok) {
      var errd = await resp.json().catch(function(){ return {}; });
      area.style.display = 'block';
      area.innerHTML = '<p style="color:var(--priority-urgent-fg);font-size:.85rem">' + esc(errd.error || '预览失败') + '</p>';
      return;
    }
    var d = await resp.json();
    var sampleRows = d.sample_rows || [];
    if (!d.has_title) {
      area.style.display = 'block';
      area.innerHTML = '<p style="color:var(--priority-urgent-fg);font-size:.85rem">CSV 缺少必要列: title (标题)，无法导入。</p>';
      return;
    }
    if (sampleRows.length === 0) {
      area.style.display = 'block';
      area.innerHTML = '<p style="font-size:.85rem;color:var(--muted)">CSV 仅包含表头，没有数据行。</p>';
      return;
    }
    var mapRows = Object.keys(d.mapped).map(function(h){
      var en = d.mapped[h];
      var known = (en === h) ? '<span style="color:var(--muted)">未识别</span>' : '<span style="color:var(--primary-500)">' + esc(en) + '</span>';
      return '<tr><td style="font-family:monospace;font-size:.78rem;padding:4px 8px;border-bottom:1px solid var(--tag-bg)">' + esc(h) + '</td><td>' + known + '</td></tr>';
    }).join('');
    var headCells = d.headers.map(function(h){
      return '<th style="font-size:.75rem;padding:4px 8px;border-bottom:1px solid var(--tag-bg);text-align:left;white-space:nowrap">' + esc(h) + '</th>';
    }).join('');
    var bodyRows = sampleRows.map(function(row){
      return '<tr>' + d.headers.map(function(_, i){
        return '<td style="font-size:.75rem;padding:4px 8px;border-bottom:1px solid var(--tag-bg);max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + esc(row[i] !== undefined ? row[i] : '') + '</td>';
      }).join('') + '</tr>';
    }).join('');
    area.style.display = 'block';
    area.innerHTML =
      '<p style="font-size:.82rem;color:var(--muted);margin-bottom:8px">预览前 ' + sampleRows.length + ' 行（实际导入将处理全部数据行）。列映射如下：</p>' +
      '<div class="table-wrap" style="margin-bottom:16px"><table style="width:auto"><thead><tr><th style="font-size:.75rem;padding:4px 8px;border-bottom:1px solid var(--tag-bg);text-align:left">CSV 表头</th><th style="font-size:.75rem;padding:4px 8px;border-bottom:1px solid var(--tag-bg);text-align:left">映射到字段</th></tr></thead><tbody>' + mapRows + '</tbody></table></div>' +
      '<div class="table-wrap" style="max-height:40vh;overflow:auto"><table style="width:100%"><thead><tr>' + headCells + '</tr></thead><tbody>' + bodyRows + '</tbody></table></div>';
  }

window.doCSVImport = async function() {
    var fileInput = document.getElementById('csvFileInput');
    if (!fileInput.files || fileInput.files.length === 0) {
      showToast('请选择 CSV 文件', 'error');
      return;
    }
    var formData = new FormData();
    formData.append('file', fileInput.files[0]);
    var projectId = document.getElementById('importProjectId').value;
    if (projectId) formData.append('project_id', projectId);

    var headers = getCsrfHeaders();
    var resp = await fetch('/api/v1/admin/import/csv', {
      method: 'POST',
      headers: headers,
      body: formData
    });
    var d = await resp.json();
    var msg = '导入完成：' + d.imported + '/' + d.total + ' 条';
    if (d.errors && d.errors.length > 0) {
      msg += '（' + d.errors.length + ' 条失败）';
    }
    document.getElementById('importResult').textContent = msg;
    showToast(msg, d.errors && d.errors.length > 0 ? 'error' : 'success');
    fileInput.value = '';
  }

window.doArchive = async function() {
    var days = parseInt(document.getElementById('archiveDaysInput').value) || 0;
    if (days <= 0) { showToast('请输入有效天数', 'error'); return; }
    var d = await apiJSON('/api/v1/admin/archive', {method:'POST', body:JSON.stringify({days_old:days})});
    if (!d) return;
    document.getElementById('archiveResult').textContent = d.message;
    showToast(d.message, 'success');
  }

window.doPruneBackups = async function() {
    var days = parseInt(document.getElementById('pruneDaysInput').value) || 0;
    if (days <= 0) { showToast('请输入有效天数', 'error'); return; }
    var d = await apiJSON('/api/v1/admin/prune-backups', {method:'POST', body:JSON.stringify({days_old:days})});
    if (!d) return;
    document.getElementById('pruneResult').textContent = d.message;
    showToast(d.message, 'success');
    loadBackupList();
  }

window.triggerBackup = async function() {
    var d = await apiJSON('/api/v1/admin/backup', {method:'POST'});
    if (!d) return;
    showToast(d.message, 'success');
    loadBackupList();
  }

