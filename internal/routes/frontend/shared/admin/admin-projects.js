// admin-projects.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.openProjectModal = function() {
    document.getElementById('projectModalTitle').textContent = '新建项目';
    document.getElementById('pfId').value = '';
    document.getElementById('pfName').value = '';
    document.getElementById('pfSlug').value = '';
    document.getElementById('pfSlug').readOnly = false;
    document.getElementById('pfDesc').value = '';
    document.getElementById('pfActive').checked = true;
    document.getElementById('pfArchived').checked = false;
    document.getElementById('pfAnnounceLevel').value = 'info';
    document.getElementById('pfAnnounceType').value = 'text';
    document.getElementById('pfAnnouncement').value = '';
    document.getElementById('projectModal').classList.add('active');
  }

window.closeProjectModal = function() {
    document.getElementById('projectModal').classList.remove('active');
  }

window.closeCloneModal = function() {
    document.getElementById('cloneModal').classList.remove('active');
  }

window.confirmClone = async function() {
    var name = document.getElementById('cloneName').value.trim();
    var slug = document.getElementById('cloneSlug').value.trim();
    var errEl = document.getElementById('cloneError');
    errEl.textContent = '';
    if (!name || !slug) {
      errEl.textContent = '名称和标识不能为空';
      return;
    }
    var d = await apiJSON('/api/v1/admin/projects/' + cloneTargetId + '/clone', {
      method: 'POST',
      body: JSON.stringify({name: name, slug: slug})
    });
    if (!d) return;
    if (d.error) { errEl.textContent = d.error; return; }
    showToast('已克隆项目', 'success');
    closeCloneModal();
    loadProjects();
  }

window.onProjectTemplateChange = function() {
    // Preview the template description
    var tpl = document.getElementById('pfTemplate').value;
    var desc = {
      empty: '通用反馈表单：标题、描述、分类、通知订阅、截图与文件上传，全部由后台控制。',
      bug_report: '收集 Bug 标题、严重程度、浏览器/OS、复现步骤、截图与通知。',
      feature_request: '收集功能标题、分类、优先级、当前问题与期望方案。',
      contact: '收集咨询主题、部门、类型与留言内容。',
      support: '工单式的标题、优先级、类型、问题描述、附件与通知。',
      product_review: '评价标题、星级评分、推荐意愿、优缺点标签与详细评价。',
      nps: 'NPS：0-10 推荐意愿量表 + 细分人群 + 开放建议。',
      satisfaction: '满意度：总体/产品/服务星级 + 续费意愿 + 改进建议。'
    };
    var el = document.getElementById('pfTemplateDesc');
    if (!el) {
      el = document.createElement('p');
      el.id = 'pfTemplateDesc';
      el.style.cssText = 'font-size:.75rem;color:#888;margin:4px 0 0 0';
      document.getElementById('pfTemplate').parentNode.appendChild(el);
    }
    el.textContent = desc[tpl] || '';
  }

window.saveProject = async function() {
    var id = document.getElementById('pfId').value;
    var project = id ? allProjects.find(function(p){ return p.id === parseInt(id); }) : null;
    // Project announcement payload
    var annContent = document.getElementById('pfAnnouncement').value.trim();
    var announcement = { enabled: false };
    if (annContent) {
      announcement = {
        enabled: true,
        level: document.getElementById('pfAnnounceLevel').value,
        content_type: document.getElementById('pfAnnounceType').value,
        content: annContent
      };
    }
    var body = {
      name: document.getElementById('pfName').value.trim(),
      slug: document.getElementById('pfSlug').value.trim(),
      description: document.getElementById('pfDesc').value.trim(),
      is_active: document.getElementById('pfActive').checked,
      is_archived: document.getElementById('pfArchived').checked,
      announcement: JSON.stringify(announcement),
      form_schema: project ? (function(){
        var ps = project.form_schema;
        if (typeof ps === 'string') { try { var p = JSON.parse(ps); if (Array.isArray(p)) ps = p; } catch(e){} }
        return Array.isArray(ps) ? JSON.stringify(ps) : '[]';
      })() : getFormTemplate(document.getElementById('pfTemplate').value)
    };
    if (!body.name || !body.slug) { showToast('名称和标识不能为空', 'error'); return; }
    var url = id ? '/api/v1/admin/projects/' + id : '/api/v1/admin/projects';
    var method = id ? 'PUT' : 'POST';
    var d = await apiJSON(url, {method: method, body: JSON.stringify(body)});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
    closeProjectModal();
    loadProjects(); loadStats();
  }

window.closeDeleteProjectModal = function() {
    var m = document.getElementById('deleteProjectModal');
    if (m) m.classList.remove('active');
  }

window.confirmDeleteProject = async function() {
    var input = document.getElementById('deleteProjectInput');
    if (!input || input.value.trim() !== deleteTargetName) {
      showToast('项目名不匹配，无法删除', 'error');
      return;
    }
    var d = await apiJSON('/api/v1/admin/projects/' + deleteTargetId, {
      method: 'DELETE',
      body: JSON.stringify({ project_name: deleteTargetName })
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message || '已删除', 'success');
    closeDeleteProjectModal();
    loadProjects(); loadStats();
  }

window.closeFormPreview = function() {
    var m = document.getElementById('formPreviewModal');
    if (m) m.classList.remove('active');
  }

window.showProjectList = function() {
    currentProjectId = null;
    document.getElementById('projectDetailView').style.display = 'none';
    document.getElementById('projectListView').style.display = '';
    window.location.hash = 'projects';
  }

window.closeFieldEditor = function() { document.getElementById('fieldEditorModal').classList.remove('active'); editingFieldIndex = -1; }

window.onFieldTypeChange = function() {
    var type = document.getElementById('feType').value;
    var sys = document.getElementById('feSys').value;
    // When a system mapping is selected, only a constrained set of options apply.
    if (sys) {
      document.getElementById('feOptionsRow').style.display = 'none';
      document.getElementById('fePlaceholderRow').style.display = (sys === 'title' || sys === 'description') ? '' : 'none';
      document.getElementById('feDefaultRow').style.display = 'none';
      document.getElementById('feMinMaxStepRow').style.display = 'none';
      document.getElementById('feMinMaxLenRow').style.display = (sys === 'title') ? '' : 'none';
      document.getElementById('fePatternRow').style.display = 'none';
      document.getElementById('feRowsRow').style.display = (sys === 'description') ? '' : 'none';
      document.getElementById('feAcceptRow').style.display = (sys === 'images' || sys === 'files') ? '' : 'none';
      document.getElementById('feIconRow').style.display = 'none';
      document.getElementById('feToggleLabelRow').style.display = 'none';
      document.getElementById('feCollapsibleRow').style.display = 'none';
      document.getElementById('feContentRow').style.display = 'none';
      document.getElementById('feName').parentNode.style.display = '';
      return;
    }
    var needsOptions = (type === 'select' || type === 'radio' || type === 'checkbox');
    document.getElementById('feOptionsRow').style.display = needsOptions ? '' : 'none';
    document.getElementById('fePlaceholderRow').style.display = (type === 'text' || type === 'textarea' || type === 'number' || type === 'email' || type === 'url' || type === 'tel') ? '' : 'none';
    document.getElementById('feDefaultRow').style.display = (type !== 'section' && type !== 'html' && type !== 'textarea' && type !== 'markdown') ? '' : 'none';
    document.getElementById('feMinMaxStepRow').style.display = (type === 'number' || type === 'slider' || type === 'scale') ? '' : 'none';
    document.getElementById('feMinMaxLenRow').style.display = (type === 'text' || type === 'textarea') ? '' : 'none';
    document.getElementById('fePatternRow').style.display = (type === 'text' || type === 'tel') ? '' : 'none';
    document.getElementById('feRowsRow').style.display = (type === 'textarea') ? '' : 'none';
    document.getElementById('feAcceptRow').style.display = (type === 'file' || type === 'image') ? '' : 'none';
    document.getElementById('feIconRow').style.display = (type === 'rating') ? '' : 'none';
    document.getElementById('feToggleLabelRow').style.display = (type === 'toggle') ? '' : 'none';
    document.getElementById('feCollapsibleRow').style.display = (type === 'section') ? '' : 'none';
    document.getElementById('feContentRow').style.display = (type === 'html') ? '' : 'none';
    var nameRow = document.getElementById('feName').parentNode;
    if (nameRow) nameRow.style.display = (type === 'section' || type === 'html') ? 'none' : '';
  }

window.onSysChange = function() {
    var sys = document.getElementById('feSys').value;
    var typeBySys = { title:'text', description:'textarea', category:'select', notify:'checkbox', images:'image', files:'file' };
    if (sys) {
      document.getElementById('feType').value = typeBySys[sys] || 'text';
      if (!document.getElementById('feName').value.trim()) document.getElementById('feName').value = sys;
      if (sys === 'title') document.getElementById('feRequired').checked = true;
    }
    onFieldTypeChange();
  }

window.addOptionRow = function(value) {
    var list = document.getElementById('feOptionsList');
    var div = document.createElement('div');
    div.className = 'opt-row';
    div.innerHTML = '<input type="text" placeholder="选项值" value="' + esc(value || '') + '"><button data-remove-parent>&times;</button>';
    list.appendChild(div);
  }

window.saveFieldEditor = function() {
    var type = document.getElementById('feType').value;
    var name = document.getElementById('feName').value.trim();
    var label = document.getElementById('feLabel').value.trim();
    var placeholder = document.getElementById('fePlaceholder').value.trim();
    var required = document.getElementById('feRequired').checked;
    var sysVal = document.getElementById('feSys').value;
    if ((type !== 'section' && type !== 'html') && (!name || !label)) { showToast('字段名称和标签不能为空', 'error'); return; }
    if (name && !/^[a-zA-Z][a-zA-Z0-9_]*$/.test(name)) { showToast('字段名称须为英文字母开头', 'error'); return; }
    var isDuplicate = currentFormSchema.some(function(f, i){ return f.name === name && i !== editingFieldIndex; });
    if (isDuplicate) { showToast('字段名称已存在', 'error'); return; }
    var field = { type: type, name: name, label: label, required: required };
    if (sysVal) field.sys = sysVal;
    if (placeholder) field.placeholder = placeholder;
    var def = document.getElementById('feDefault').value.trim();
    if (def) field.default = def;
    var minV = document.getElementById('feMin').value;
    if (minV) field.min = Number(minV);
    var maxV = document.getElementById('feMax').value;
    if (maxV) field.max = Number(maxV);
    var stepV = document.getElementById('feStep').value;
    if (stepV) field.step = Number(stepV);
    var minL = document.getElementById('feMinLength').value;
    if (minL) field.min_length = Number(minL);
    var maxL = document.getElementById('feMaxLength').value;
    if (maxL) field.max_length = Number(maxL);
    var pat = document.getElementById('fePattern').value.trim();
    if (pat) field.pattern = pat;
    var rows = document.getElementById('feRows').value;
    if (rows) field.rows = Number(rows);
    var accept = document.getElementById('feAccept').value.trim();
    if (accept) field.accept = accept;
    field.multiple = document.getElementById('feMultiple').checked;
    var icon = document.getElementById('feIcon').value;
    if (icon) field.icon = icon;
    var labelOn = document.getElementById('feLabelOn').value.trim();
    if (labelOn) field.label_on = labelOn;
    var labelOff = document.getElementById('feLabelOff').value.trim();
    if (labelOff) field.label_off = labelOff;
    field.collapsible = document.getElementById('feCollapsible').checked;
    var content = document.getElementById('feContent').value.trim();
    if (content) field.content = content;
    field.width = document.getElementById('feWidth').value;
    var helpText = document.getElementById('feHelpText').value.trim();
    if (helpText) field.help_text = helpText;
    var needsOptions = (type === 'select' || type === 'radio' || type === 'checkbox') && !sysVal;
    if (needsOptions) {
      var optInputs = document.getElementById('feOptionsList').querySelectorAll('input');
      var options = [];
      optInputs.forEach(function(inp){ var v = inp.value.trim(); if (v) options.push(v); });
      if (options.length === 0) { showToast('请至少添加一个选项', 'error'); return; }
      field.options = options;
    }
    if (editingFieldIndex >= 0) currentFormSchema[editingFieldIndex] = field;
    else currentFormSchema.push(field);
    closeFieldEditor();
    renderFormSchemaList();
  }

