// admin-dashboard.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.loadFeedbacks = async function() {
    var project = document.getElementById('projectFilter').value;
    var status = document.getElementById('statusFilter').value;
    var priority = document.getElementById('priorityFilter').value;
    var assignee = assigneeMine ? currentUsername : document.getElementById('assigneeFilter').value;
    var category = document.getElementById('categoryFilter').value;
    var keyword = document.getElementById('keywordSearch').value.trim();

    // Reset pagination when the filter combination changes
    var sig = [project, status, priority, assignee, category, keyword].join('|');
    if (sig !== lastFilterSig) { feedbackOffset = 0; lastFilterSig = sig; }

    // Save filter state to memory
    filterMemory = {project:project, status:status, priority:priority, assignee:assignee, category:category, keyword:keyword};

    var sortBy = document.getElementById('sortBy') ? document.getElementById('sortBy').value : '';
    var url = '/api/v1/admin/feedbacks?limit=' + feedbackLimit + '&offset=' + feedbackOffset;
    if (project) url += '&project=' + encodeURIComponent(project);
    if (status) url += '&status=' + encodeURIComponent(status);
    if (priority) url += '&priority=' + encodeURIComponent(priority);
    if (assignee) url += '&assignee=' + encodeURIComponent(assignee);
    if (category) url += '&category=' + encodeURIComponent(category);
    if (keyword) url += '&keyword=' + encodeURIComponent(keyword);
    if (sortBy) url += '&sort=' + encodeURIComponent(sortBy);
    var resp = await api(url);
    if (!resp) return;
    var d = await resp.json();
    feedbacks = d.feedbacks || [];
    projectNames = d.project_names || {};
    var projects = d.projects || [];
    var assignees = d.assignees || [];
    var total = d.total || 0;

    var sel = document.getElementById('projectFilter');
    var cur = sel.value;
    sel.innerHTML = '<option value="">全部项目</option>';
    projects.forEach(function(p){
      var opt = document.createElement('option');
      opt.value = p; opt.textContent = p;
      if (p === cur) opt.selected = true;
      sel.appendChild(opt);
    });

    // Populate assignee filter dropdown
    var aSel = document.getElementById('assigneeFilter');
    var aCur = aSel.value;
    aSel.innerHTML = '<option value="">全部指派</option>';
    assignees.forEach(function(a){
      var opt = document.createElement('option');
      opt.value = a; opt.textContent = a;
      if (a === aCur) opt.selected = true;
      aSel.appendChild(opt);
    });

    document.getElementById('feedbackCount').textContent = '共 ' + total + ' 条';
    renderTable();
    renderPagination(total);
  }

window.onSortChange = function() {
    feedbackOffset = 0;
    loadFeedbacks();
  }

window.toggleMine = function() {
    assigneeMine = !assigneeMine;
    var btn = document.getElementById('mineBtn');
    if (btn) btn.classList.toggle('active', assigneeMine);
    feedbackOffset = 0;
    loadFeedbacks();
  }

window.onTrendRange = function(r) {
    if (r === 'custom') {
      var cr = document.getElementById('customRange');
      if (cr) cr.style.display = 'flex';
    } else {
      chartDays = parseInt(r, 10) || 7;
      chartFrom = ''; chartTo = '';
      var cr2 = document.getElementById('customRange');
      if (cr2) cr2.style.display = 'none';
      loadChart();
    }
    var btns = document.querySelectorAll('#trendRange button');
    for (var i = 0; i < btns.length; i++) {
      btns[i].classList.toggle('active', btns[i].getAttribute('data-args') === "'" + r + "'");
    }
  }

window.applyCustomRange = function() {
    var from = document.getElementById('trendFrom');
    var to = document.getElementById('trendTo');
    if (!from || !to || !from.value || !to.value) { showToast('请选择起止日期', 'error'); return; }
    chartFrom = from.value; chartTo = to.value;
    loadChart();
  }

window.onProjectChange = function() {
    document.getElementById('categoryFilter').value = '';
    populateCategoryFilter();
    loadFeedbacks();
  }

window.toggleSelectAll = function(checked) {
    if (checked) { feedbacks.forEach(function(f){ selectedIds.add(f.id); }); }
    else { selectedIds.clear(); }
    renderTable();
  }

window.clearSelection = function() {
    selectedIds.clear();
    document.getElementById('selectAllCb').checked = false;
    renderTable();
  }

window.bulkUpdateStatus = async function() {
    var status = document.getElementById('bulkStatusSelect').value;
    if (!status) { showToast('请选择目标状态', 'error'); return; }
    if (!confirm('确认将 '+selectedIds.size+' 条反馈标记为 '+statusLabels[status]+'？')) return;
    var d = await apiJSON('/api/v1/admin/feedbacks/bulk-status', {
      method: 'POST',
      body: JSON.stringify({ids: Array.from(selectedIds), status: status})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
    selectedIds.clear();
    loadFeedbacks(); loadStats();
  }

window.bulkDelete = async function() {
    if (!confirm('确认删除 '+selectedIds.size+' 条反馈？此操作不可恢复。')) return;
    var d = await apiJSON('/api/v1/admin/feedbacks/bulk-delete', {
      method: 'POST',
      body: JSON.stringify({ids: Array.from(selectedIds)})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
    selectedIds.clear();
    loadFeedbacks(); loadStats();
  }

window.showList = function() {
    document.getElementById('detailView').classList.remove('active');
    document.getElementById('listView').style.display = '';
    window.location.hash = currentTab;
  }

window.refreshDashboard = function() {
    loadStats(); loadFeedbacks(); loadChart();
  }

window.exportFeedback = function(fmt) {
    var project = document.getElementById('projectFilter').value;
    var url = '/api/v1/admin/feedbacks/export?fmt=' + encodeURIComponent(fmt || 'csv');
    if (project) url += '&project=' + encodeURIComponent(project);
    window.open(url, '_blank');
  }

window.bulkUpdatePriority = async function() {
    var ids = getSelectedIds();
    if (ids.length === 0) { showToast('请先选择反馈', 'error'); return; }
    var priority = document.getElementById('bulkPrioritySelect').value;
    if (!priority) { showToast('请选择优先级', 'error'); return; }
    var d = await apiJSON('/api/v1/admin/feedbacks/bulk-priority', {method:'POST', body:JSON.stringify({ids:ids, priority:priority})});
    if (!d) return;
    showToast(d.message, 'success');
    clearSelection();
    loadFeedbacks();
  }

window.bulkUpdateAssignee = async function() {
    var ids = getSelectedIds();
    if (ids.length === 0) { showToast('请先选择反馈', 'error'); return; }
    var assignee = document.getElementById('bulkAssigneeInput').value.trim();
    if (!assignee) { showToast('请输入指派人', 'error'); return; }
    var d = await apiJSON('/api/v1/admin/feedbacks/bulk-assignee', {method:'POST', body:JSON.stringify({ids:ids, assignee:assignee})});
    if (!d) return;
    showToast(d.message, 'success');
    clearSelection();
    loadFeedbacks();
  }

window.bulkUpdateTags = async function() {
    var ids = getSelectedIds();
    if (ids.length === 0) { showToast('请先选择反馈', 'error'); return; }
    var tags = document.getElementById('bulkTagsInput').value.trim();
    if (!tags) { showToast('请输入标签', 'error'); return; }
    var d = await apiJSON('/api/v1/admin/feedbacks/bulk-tags', {method:'POST', body:JSON.stringify({ids:ids, tags:tags})});
    if (!d) return;
    showToast(d.message, 'success');
    clearSelection();
    loadFeedbacks();
  }

