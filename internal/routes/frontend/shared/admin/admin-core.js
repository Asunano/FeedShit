// admin-core.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

var currentTab = 'dashboard';

var feedbacks = [];

var allProjects = [];

var currentProjectId = null;

var currentProjectCategories = [];

var currentFormSchema = [];

var editingFieldIndex = -1;

var csrfToken = '';

var searchTimer = null;

var selectedIds = new Set();

var currentUserRole = '';

var currentUsername = '';

var filterMemory = {}

var projectNames = {}

var feedbackOffset = 0;

var feedbackLimit = 100;

var lastFilterSig = '';

var assigneeMine = false;

var chartDays = 7;

var chartFrom = '';

var chartTo = '';

var currentDetailId = null;

var adminImgFiles = {}

var adminOtherFiles = {}

var adminNoteImgFiles = {}

var adminLbItems = [];

var adminLbIndex = 0;

var adminLbFbId = 0;

var adminLbNoteId = 0;

var statusLabels = {pending:'待处理',processing:'处理中',resolved:'已解决',closed:'已关闭'}

var priorityLabels = {'':'无',urgent:'紧急',high:'高',medium:'中',low:'低'}

var kbProjectSlug = '';

var kbCache = [];

var cloneTargetId = 0;

var FORM_TEMPLATES = {
    empty: [
      {sys:'title', type:'text', name:'title', label:'反馈标题', required:true, placeholder:'请输入反馈标题'},
      {sys:'description', type:'textarea', name:'description', label:'详细描述', placeholder:'请描述您遇到的问题或建议', rows:5},
      {sys:'category', type:'select', name:'category', label:'分类'},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收反馈处理通知'},
      {sys:'images', type:'image', name:'images', label:'截图上传', multiple:true},
      {sys:'files', type:'file', name:'files', label:'日志 / 附件', multiple:true}
    ],
    bug_report: [
      {sys:'title', type:'text', name:'bug_title', label:'问题标题', required:true, placeholder:'简要描述问题'},
      {sys:'description', type:'textarea', name:'bug_desc', label:'详细描述', placeholder:'请详细描述遇到的问题、复现步骤等信息', rows:5},
      {type:'select', name:'severity', label:'严重程度', required:true, options:['严重','较高','一般','较低'], width:'half', help_text:'该 Bug 对用户的影响程度'},
      {type:'select', name:'browser', label:'浏览器', options:['Chrome','Firefox','Safari','Edge','其他'], width:'half'},
      {type:'text', name:'os', label:'操作系统', placeholder:'如 Windows 11 / macOS 14', width:'half'},
      {type:'url', name:'url', label:'问题页面 URL', placeholder:'https://', width:'half'},
      {type:'textarea', name:'steps', label:'复现步骤', required:true, placeholder:'1. 打开...\n2. 点击...\n3. 出现...', rows:5},
      {type:'textarea', name:'expected', label:'预期行为', placeholder:'应该发生什么？', rows:3},
      {type:'textarea', name:'actual', label:'实际行为', placeholder:'实际发生了什么？', rows:3},
      {sys:'images', type:'image', name:'screenshots', label:'截图上传', multiple:true},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收反馈处理通知'}
    ],
    feature_request: [
      {sys:'title', type:'text', name:'fr_title', label:'功能标题', required:true, placeholder:'一句话概括你的建议'},
      {sys:'description', type:'textarea', name:'fr_desc', label:'背景与动机', placeholder:'为什么需要这个功能？', rows:4},
      {type:'select', name:'category', label:'分类', required:true, options:['新功能','改进','性能','UI/UX','其他'], width:'half'},
      {type:'select', name:'priority', label:'优先级', options:['急需','重要','可以考虑','低优先级'], width:'half'},
      {type:'textarea', name:'problem', label:'当前问题', placeholder:'目前在使用中遇到了什么不便？', rows:3},
      {type:'textarea', name:'solution', label:'期望方案', required:true, placeholder:'你希望如何解决？', rows:4},
      {type:'textarea', name:'alternatives', label:'替代方案', placeholder:'是否考虑过其他解决方式？', rows:3},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收反馈处理通知'}
    ],
    contact: [
      {sys:'title', type:'text', name:'contact_title', label:'咨询主题', required:true},
      {type:'text', name:'company', label:'公司/组织', placeholder:'可选', width:'half'},
      {type:'select', name:'department', label:'部门', options:['销售','市场','技术','客服','管理','其他'], width:'half'},
      {type:'select', name:'subject', label:'咨询类型', required:true, options:['商务合作','技术支持','投诉建议','其他']},
      {sys:'description', type:'textarea', name:'contact_msg', label:'留言内容', required:true, placeholder:'请详细描述...', rows:5},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收反馈处理通知'}
    ],
    support: [
      {sys:'title', type:'text', name:'ticket_title', label:'工单标题', required:true},
      {type:'select', name:'tier', label:'优先级', required:true, options:['P0: 紧急','P1: 高','P2: 中','P3: 低'], width:'half'},
      {type:'select', name:'type', label:'工单类型', required:true, options:['故障报修','使用咨询','账号问题','数据修复','其他'], width:'half'},
      {type:'url', name:'related_url', label:'相关链接', placeholder:'https://', help_text:'问题相关的页面或文档链接'},
      {sys:'description', type:'textarea', name:'ticket_detail', label:'问题描述', required:true, placeholder:'请详细描述您遇到的问题...', rows:6},
      {sys:'files', type:'file', name:'attachments', label:'附件', multiple:true, help_text:'支持截图、日志文件等'},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收反馈处理通知'}
    ],
    product_review: [
      {sys:'title', type:'text', name:'review_title', label:'评价标题', required:true, placeholder:'一句话总结你的体验'},
      {type:'rating', name:'rating', label:'总体评分', required:true, max:5, icon:'star'},
      {type:'select', name:'recommend', label:'推荐给朋友', options:['肯定会','可能会','不确定','可能不会','肯定不会'], width:'half'},
      {type:'textarea', name:'pros', label:'优点', placeholder:'你最喜欢哪些方面？', rows:3},
      {type:'textarea', name:'cons', label:'不足', placeholder:'哪些方面需要改进？', rows:3},
      {type:'tags', name:'tags', label:'标签', placeholder:'输入标签后回车'},
      {sys:'description', type:'textarea', name:'review_detail', label:'详细评价', rows:4},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收反馈处理通知'}
    ],
    nps: [
      {sys:'title', type:'text', name:'nps_title', label:'调查主题', required:true, placeholder:'一句话概括本次调查'},
      {type:'scale', name:'nps_score', label:'您向朋友或同事推荐我们的可能性有多大？', required:true, max:10, options:['极不可能','极可能'], help_text:'0 = 极不可能，10 = 极可能'},
      {type:'select', name:'nps_segment', label:'您属于', options:['客户','试用用户','合作伙伴','其他'], width:'half'},
      {type:'textarea', name:'nps_comment', label:'原因 / 建议', placeholder:'请告诉我们您的打分理由', rows:4},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收处理通知'}
    ],
    satisfaction: [
      {sys:'title', type:'text', name:'sat_title', label:'评价主题', required:true},
      {type:'rating', name:'sat_overall', label:'总体满意度', required:true, max:5, icon:'star'},
      {type:'rating', name:'sat_product', label:'产品满意度', max:5, icon:'star', width:'half'},
      {type:'rating', name:'sat_service', label:'服务满意度', max:5, icon:'star', width:'half'},
      {type:'select', name:'sat_renew', label:'是否愿意续费 / 继续使用', options:['肯定会','可能会','不确定','可能不会','肯定不会'], width:'half'},
      {type:'textarea', name:'sat_suggest', label:'改进建议', rows:4},
      {sys:'notify', type:'checkbox', name:'notify', label:'接收处理通知'}
    ]
  }

var deleteTargetId = null;

var deleteTargetName = '';

var deleteInputBound = false;

var chartColors = ['#333','#e53e3e','#2563eb','#38a169','#d69e2e','#805ad5','#dd6b20','#319795','#d53f8c','#718096'];

var keywordSearchEl = document.getElementById('keywordSearch');

function getCsrfHeaders() {
    var h = {'X-CSRF-Token': csrfToken};
    return h;
  }

  

function syncCsrfFromCookie() {
    try {
      var m = document.cookie.match(/(?:^|;\s*)csrf_token=([^;]+)/);
      if (m && m[1]) csrfToken = decodeURIComponent(m[1]);
    } catch (e) {}
  }

  

async function fetchCSRFToken() {
    try {
      var resp = await api('/api/v1/admin/csrf-token', {method:'POST'});
      if (resp) {
        var d = await resp.json();
        csrfToken = d.csrf_token || '';
      }
      syncCsrfFromCookie();
    } catch(e) {}
  }

  

function onPageSizeChange(v) {
    feedbackLimit = parseInt(v, 10);
    feedbackOffset = 0;
    loadFeedbacks();
  }

  

async function api(url, opts) {
    var resp = await fetch(url, opts);
    syncCsrfFromCookie(); // keep in sync with server-side CSRF rotation
    if (resp.status === 401) { window.location.href = '/admin/login'; return null; }
    return resp;
  }

  

async function apiJSON(url, opts) {
    opts = opts || {};
    opts.headers = opts.headers || {};
    opts.headers['Content-Type'] = 'application/json';
    Object.assign(opts.headers, getCsrfHeaders());
    var resp = await api(url, opts);
    if (!resp) return null;
    return resp.json();
  }

  

async function apiForm(url, formData) {
    var opts = { method: 'POST', body: formData };
    opts.headers = getCsrfHeaders();
    var resp = await api(url, opts);
    if (!resp) return null;
    return resp.json();
  }

  

function renderKbProjects() {
    var sel = document.getElementById('kbProject');
    if (!sel) return;
    var html = '<option value="">选择项目</option>';
    (allProjects || []).forEach(function(p) {
      html += '<option value="' + esc(p.slug) + '">' + esc(p.name) + '</option>';
    });
    sel.innerHTML = html;
  }

  

async function loadStats() {
    var resp = await api('/api/v1/admin/stats');
    if (!resp) return;
    var d = await resp.json();
    document.getElementById('statTotal').textContent = d.total_feedbacks || 0;
    document.getElementById('statProjects').textContent = d.total_projects || 0;
    document.getElementById('statToday').textContent = d.today_feedbacks || 0;
    var avg = document.getElementById('statAvgRes');
    if (avg) avg.textContent = formatResolution(d.avg_resolution_hours);
  }

function formatResolution(h) {
    if (h === null || h === undefined || h === '' || isNaN(Number(h))) return '-';
    h = Number(h);
    if (h <= 0) return '0h';
    if (h < 24) return (Math.round(h * 10) / 10) + 'h';
    return (Math.round(h / 24 * 10) / 10) + '天';
  }

  

function restoreFilters() {
    if (filterMemory.project) document.getElementById('projectFilter').value = filterMemory.project;
    if (filterMemory.status) document.getElementById('statusFilter').value = filterMemory.status;
    if (filterMemory.priority) document.getElementById('priorityFilter').value = filterMemory.priority;
    if (filterMemory.assignee) document.getElementById('assigneeFilter').value = filterMemory.assignee;
    if (filterMemory.category) document.getElementById('categoryFilter').value = filterMemory.category;
    if (filterMemory.keyword) document.getElementById('keywordSearch').value = filterMemory.keyword;
  }

  

function renderPagination(total) {
    var bar = document.getElementById('paginationBar');
    if (!bar) return;
    var start = total === 0 ? 0 : feedbackOffset + 1;
    var end = Math.min(feedbackOffset + feedbackLimit, total);
    bar.innerHTML =
      '<span class="count">显示 ' + start + '-' + end + ' / 共 ' + total + ' 条</span>' +
      '<button class="btn-sm" data-click="prevPage" data-args="" ' + (feedbackOffset <= 0 ? 'disabled' : '') + '>上一页</button>' +
      '<button class="btn-sm" data-click="nextPage" data-args="" ' + (end >= total ? 'disabled' : '') + '>下一页</button>';
  }

  

function statusBadge(status) {
    var cls = 'status-badge status-' + (status || 'pending');
    var label = statusLabels[status] || status || '待处理';
    return '<span class="'+cls+'">'+esc(label)+'</span>';
  }

  

function priorityBadge(p) {
    if (!p) return '<span style="color:var(--border-muted)">-</span>';
    var cls = 'priority-badge priority-' + p;
    var label = priorityLabels[p] || p;
    return '<span class="'+cls+'">'+esc(label)+'</span>';
  }

  

function renderTable() {
    var tbody = document.getElementById('feedbackTable');
    if (feedbacks.length === 0) {
      tbody.innerHTML = '<tr><td colspan="10" class="empty">暂无反馈记录</td></tr>';
      updateBulkBar();
      return;
    }
    tbody.innerHTML = feedbacks.map(function(f){
      var dt = f.created_at ? f.created_at.replace('T',' ').substring(0,16) : '-';
      var checked = selectedIds.has(f.id) ? ' checked' : '';
      var dupBadge = f.is_duplicate ? '<span class="dup-badge" title="重复 #'+f.duplicate_of+'">重复</span>' : '';
      return '<tr data-id="'+f.id+'">' +
        '<td class="cb-col"><input type="checkbox" data-id="'+f.id+'"'+checked+' data-change="toggleSelect" data-args="'+f.id+',this.checked"></td>' +
        '<td><a href="#" data-click="showDetail" data-args="'+f.id+'" style="color:var(--fg)">#'+f.id+'</a></td>' +
        '<td><span class="tag">'+esc(projectNames[f.project_id] || f.project_id)+'</span></td>' +
        '<td><a href="#" data-click="showDetail" data-args="'+f.id+'">'+esc(f.title)+'</a>'+dupBadge+'</td>' +
        '<td>'+priorityBadge(f.priority)+'</td>' +
        '<td>'+statusBadge(f.status)+'</td>' +
        '<td style="text-align:center;color:var(--tag-fg);font-size:.85rem">'+(f.votes||0)+'</td>' +
        '<td style="font-size:.8rem;color:var(--tag-fg)">'+(f.assignee ? esc(f.assignee) : '-')+'</td>' +
        '<td style="font-family:monospace;font-size:.8rem;color:var(--muted)">'+esc(f.client_ip)+'</td>' +
        '<td style="color:var(--muted);font-size:.8rem">'+dt+'</td>' +
        '</tr>';
    }).join('');
    updateBulkBar();
  }

  

function updateBulkBar() {
    var bar = document.getElementById('bulkBar');
    var n = selectedIds.size;
    if (n > 0) {
      bar.classList.add('active');
      document.getElementById('bulkCount').textContent = n + ' 条已选';
    } else {
      bar.classList.remove('active');
    }
  }
  

function renderCustomData(customData, formSchemaJSON) {
    if (!customData || typeof customData !== 'object') return '';
    var keys = Object.keys(customData);
    if (!keys.length) return '';

    // 构建 name/key -> {label, type, options} 映射
    var fieldMap = {};
    try {
      var schema = JSON.parse(formSchemaJSON || '[]');
      if (Array.isArray(schema)) {
        schema.forEach(function(f){
          if (!f) return;
          var fk = (f.name && f.name !== '') ? f.name : (f.key || '');
          if (!fk) return;
          fieldMap[fk] = { label: f.label || fk, type: f.type || '', options: f.options || [] };
        });
      }
    } catch (e) {}

    var rows = '';
    keys.forEach(function(k){
      var def = fieldMap[k];
      var label = def ? def.label : k;
      var type = def ? def.type : '';
      var raw = customData[k];
      var valHtml;
      if (type === 'select' || type === 'radio') {
        // options 为纯字符串，raw 即展示文本
        valHtml = esc(String(raw));
      } else if (type === 'checkbox') {
        var ck = (raw === true || raw === 'true' || raw === 1 || raw === '1');
        valHtml = ck ? '是' : '否';
      } else if (type === 'toggle') {
        var on = (raw === true || raw === 'true' || raw === 1 || raw === '1');
        valHtml = on ? '开启' : '关闭';
      } else if (type === 'rating') {
        var n = Number(raw) || 0;
        var stars = '';
        for (var s = 1; s <= 5; s++) stars += (s <= n) ? '★' : '☆';
        valHtml = esc(stars) + ' ' + n + '/5';
      } else if (type === 'tags') {
        valHtml = Array.isArray(raw) ? esc(raw.join(', ')) : esc(String(raw));
      } else {
        if (raw === true) valHtml = '是';
        else if (raw === false) valHtml = '否';
        else if (Array.isArray(raw)) valHtml = esc(raw.join(', '));
        else valHtml = esc(String(raw));
      }
      rows += '<div class="cd-row"><span class="cd-label">' + esc(label) + '</span><span class="cd-value">' + valHtml + '</span></div>';
    });
    if (!rows) return '';
    return '<div class="custom-data">' + rows + '</div>';
  }

  

async function showDetailInner(id) {
    var resp = await api('/api/v1/admin/feedbacks/' + id);
    if (!resp) return;
    var f = await resp.json();
    document.getElementById('listView').style.display = 'none';
    var dv = document.getElementById('detailView');
    dv.classList.add('active');
    var dt = f.created_at ? f.created_at.replace('T',' ').substring(0,19) : '-';
    var html = '<h2>' + esc(f.title) + '</h2>' +
      '<div class="detail-meta">' +
        '<span>项目：<span class="tag">'+esc(f.project_id)+'</span></span>' +
        '<span>IP：'+esc(f.client_ip)+'</span>' +
        '<span>时间：'+dt+'</span>' +
        '<span><a href="#" data-click="deleteFeedback" data-args="'+f.id+'" style="color:var(--priority-urgent-fg)">删除</a></span>' +
      '</div>';

    // Contact info
    if (f.contact_name || f.contact_email) {
      html += '<div class="contact-info">';
      if (f.contact_name) html += '<span>联系人：'+esc(f.contact_name)+'</span>';
      if (f.contact_email) html += '<span>邮箱：<a href="mailto:'+esc(f.contact_email)+'">'+esc(f.contact_email)+'</a></span>';
      html += '</div>';
    }

    if (f.description) html += '<div class="detail-desc">' + esc(f.description) + '</div>';

    // Custom data（适配 form_schema：中文标签 + 人类可读值）
    var customData = {};
    try { customData = JSON.parse(f.custom_data || '{}'); } catch(e){}
    html += renderCustomData(customData, f.form_schema);

    // Status selector
    var curStatus = f.status || 'pending';
    html += '<div class="status-selector">' +
      '<label>状态：</label>' +
      '<select id="statusSelect">' +
      '<option value="pending"' + (curStatus==='pending'?' selected':'') + '>待处理</option>' +
      '<option value="processing"' + (curStatus==='processing'?' selected':'') + '>处理中</option>' +
      '<option value="resolved"' + (curStatus==='resolved'?' selected':'') + '>已解决</option>' +
      '<option value="closed"' + (curStatus==='closed'?' selected':'') + '>已关闭</option>' +
      '</select>' +
      '<button data-click="updateFeedbackStatus" data-args="'+f.id+'">更新</button>' +
      '</div>';

    // Tags
    if (f.tags) {
      html += '<div style="margin-top:8px;font-size:.8rem;color:var(--muted)">标签：' + esc(f.tags) + '</div>';
    }

    // Assignee
    html += '<div class="assignee-row">' +
      '<label>指派给：</label>' +
      '<input type="text" id="assigneeInput" value="'+esc(f.assignee||'')+'" placeholder="处理人">' +
      '<button data-click="updateAssignee" data-args="'+f.id+'">保存</button>' +
      '</div>';

    // Priority & Duplicate
    var curPriority = f.priority || '';
    html += '<div style="display:flex;align-items:center;gap:12px;margin-top:8px">' +
      '<label style="font-size:.8rem;color:var(--tag-fg)">优先级：</label>' +
      '<select class="priority-selector" data-change="updatePriority" data-args="'+f.id+',this.value">' +
      '<option value=""' + (curPriority===''?' selected':'') + '>无</option>' +
      '<option value="low"' + (curPriority==='low'?' selected':'') + '>低</option>' +
      '<option value="medium"' + (curPriority==='medium'?' selected':'') + '>中</option>' +
      '<option value="high"' + (curPriority==='high'?' selected':'') + '>高</option>' +
      '<option value="urgent"' + (curPriority==='urgent'?' selected':'') + '>紧急</option>' +
      '</select>';
    if (f.is_duplicate) {
      html += '<span class="dup-badge">重复 #'+f.duplicate_of+'</span>' +
        '<button class="btn-sm" data-click="unmarkDuplicate" data-args="'+f.id+'" style="font-size:.75rem">取消标记</button>';
    } else {
      html += '<button class="btn-sm" data-click="markDuplicate" data-args="'+f.id+'" style="font-size:.75rem">标记重复</button>';
    }
    html += '</div>';

    // Roadmap (M3)
    var rmStatusOpts = [['planning','规划中'],['in_progress','进行中'],['released','已发布']];
    var rmOptHtml = rmStatusOpts.map(function(o){ return '<option value="'+o[0]+'"'+(f.roadmap_status===o[0]?' selected':'')+'>'+o[1]+'</option>'; }).join('');
    html += '<div class="roadmap-row" style="display:flex;align-items:center;gap:12px;margin-top:8px;flex-wrap:wrap">' +
      '<label style="font-size:.8rem;color:var(--tag-fg)">公开到路线图：</label>' +
      '<label class="toggle"><input type="checkbox" id="roadmapPublic"'+(f.public_on_roadmap?' checked':'')+'><span>'+(f.public_on_roadmap?'是':'否')+'</span></label>' +
      '<label style="font-size:.8rem;color:var(--tag-fg)">看板状态：</label>' +
      '<select id="roadmapStatus">'+rmOptHtml+'</select>' +
      '<button data-click="saveRoadmap" data-args="'+f.id+'">保存路线图</button>' +
      '</div>';

    // 媒体：截图 / 文件 折叠展开，默认不渲染大图，防布局撑爆
    var files = [];
    try { files = JSON.parse(f.file_paths || '[]'); } catch(e){}
    currentDetailId = f.id;
    adminImgFiles[f.id] = [];
    adminOtherFiles[f.id] = [];
    files.forEach(function(fp, idx){
      var fname = fp.split('/').pop();
      if (/\.(png|jpe?g|gif|webp|bmp)$/i.test(fname)) adminImgFiles[f.id].push({fp: fp, idx: idx});
      else adminOtherFiles[f.id].push(fp);
    });
    if (files.length > 0) {
      html += '<div class="detail-media" style="margin-top:16px">';
      if (adminImgFiles[f.id].length) html += '<button type="button" class="media-toggle" data-click="toggleMedia" data-args="shots,'+f.id+'">📷 截图 ('+adminImgFiles[f.id].length+')</button>';
      if (adminOtherFiles[f.id].length) html += '<button type="button" class="media-toggle" data-click="toggleMedia" data-args="files,'+f.id+'">📎 文件 ('+adminOtherFiles[f.id].length+')</button>';
      html += '<div id="mediaShots" class="media-panel" style="display:none"></div>';
      html += '<div id="mediaFiles" class="media-panel" style="display:none"></div>';
      html += '</div>';
    }

    // Notes section
    html += '<div class="notes-section">' +
      '<h3>备注与回复 ('+((f.notes||[]).length)+')</h3>' +
      '<div id="notesList">加载中...</div>' +
      '<div class="note-form">' +
      '<textarea id="noteContent" placeholder="添加内部备注或公开回复..."></textarea>' +
      '<input type="file" id="noteFiles" multiple accept="image/*,.pdf,.doc,.docx,.txt,.log,.csv,.json,.zip" style="margin-top:8px">' +
      '<div style="margin:4px 0 8px;font-size:.75rem;color:var(--muted)">支持图片/PDF/Word/文本/日志/压缩包，单文件最大 20MB（选填）</div>' +
      '<div class="note-form-actions">' +
      '<label><input type="checkbox" id="notePublic"> 公开回复（提交者可见）</label>' +
      '<button data-click="addNote" data-args="'+f.id+'">提交</button>' +
      '</div></div></div>';

    document.getElementById('detailContent').innerHTML = html;

    // Load notes
    loadNotes(f.id);
  }

  

async function loadLogContent(url, elemId) {
    try {
      var resp = await fetch(url);
      var text = await resp.text();
      var el = document.getElementById(elemId);
      if (el) {
        if (text.length > 51200) text = text.substring(0, 51200) + '\n... (仅显示前 50KB)';
        el.textContent = text;
      }
    } catch(e) {
      var el = document.getElementById(elemId);
      if (el) el.textContent = '加载失败';
    }
  }

  

async function loadNotes(feedbackId) {
    var resp = await api('/api/v1/admin/feedbacks/' + feedbackId + '/notes');
    if (!resp) return;
    var d = await resp.json();
    var notes = d.notes || [];
    var container = document.getElementById('notesList');
    if (!container) return;
    if (notes.length === 0) {
      container.innerHTML = '<p style="color:var(--hint);font-size:.8rem">暂无备注</p>';
      return;
    }
    container.innerHTML = notes.map(function(n){
      var dt = n.created_at ? n.created_at.replace('T',' ').substring(0,16) : '-';
      var pubClass = n.is_public ? ' public' : '';
      var badge = n.is_public ? '<span class="note-badge pub">公开</span>' : '<span class="note-badge priv">内部</span>';
      var fileHtml = '';
      try {
        var fps = JSON.parse(n.file_paths || '[]');
        if (fps.length) {
          adminNoteImgFiles[n.id] = [];
          var gridHtml = '';
          var chipHtml = '';
          fps.forEach(function(fp, idx){
            var fname = fp.split('/').pop();
            if (/\.(png|jpe?g|gif|webp|bmp)$/i.test(fname)) {
              var ri = adminNoteImgFiles[n.id].length;
              adminNoteImgFiles[n.id].push({fp: fp, idx: idx});
              gridHtml += '<div class="thumb-item" data-click="openAdminLb" data-args="'+ri+','+n.id+'">' +
                '<img src="/api/v1/admin/feedbacks/'+feedbackId+'/thumb?note='+n.id+'&i='+idx+'" alt="'+esc(fname)+'" loading="lazy">' +
                '<span class="thumb-name">'+esc(fname)+'</span></div>';
            } else {
              chipHtml += '<a class="file-chip" href="/admin/files/'+esc(fp)+'" target="_blank" rel="noopener" download style="display:inline-block;margin:6px 6px 0 0;padding:4px 10px;background:var(--panel);border:1px solid var(--border);border-radius:8px;color:var(--accent);text-decoration:none;font-size:.8rem">'+esc(fname)+'</a>';
              if (/\.pdf$/i.test(fp)) {
                chipHtml += '<button type="button" class="file-chip pdf-preview" data-click="openPdf" data-pdf="' + esc(fp) + '">预览</button>';
              }
            }
          });
          if (gridHtml) fileHtml += '<div class="thumb-grid">'+gridHtml+'</div>';
          if (chipHtml) fileHtml += '<div class="note-files">'+chipHtml+'</div>';
        }
      } catch(e) {}
      return '<div class="note-item'+pubClass+'">' +
        '<div class="note-header"><span class="note-author">'+esc(n.author)+badge+'</span>' +
        '<span><span class="note-time">'+dt+'</span> ' +
        '<button class="note-delete" data-click="deleteNote" data-args="'+n.id+','+feedbackId+'">删除</button></span></div>' +
        '<div class="note-content">'+esc(n.content)+'</div>' + fileHtml + '</div>';
    }).join('');
  }

  

async function loadChart() {
    var url = '/api/v1/admin/chart-data';
    if (chartFrom && chartTo) url += '?from=' + encodeURIComponent(chartFrom) + '&to=' + encodeURIComponent(chartTo);
    else url += '?days=' + (chartDays || 7);
    var resp = await api(url);
    if (!resp) return;
    var d = await resp.json();
    var trend = d.daily_trend || [];
    var container = document.getElementById('chartInner');
    if (!container) return;
    if (trend.length === 0) {
      container.innerHTML = '<p class="chart-empty">暂无数据</p>';
    } else {
      var maxCount = Math.max.apply(null, trend.map(function(t){ return t.count; }));
      if (maxCount === 0) maxCount = 1;
      var html = '<div class="bar-chart">';
      trend.forEach(function(t){
        var h = Math.max(2, Math.round((t.count / maxCount) * 100));
        var dateShort = (t.date || '').substring(5);
        html += '<div class="bar-col" title="'+t.date+': '+t.count+'">' +
          '<span class="bar-count">'+(t.count > 0 ? t.count : '')+'</span>' +
          '<div class="bar" style="height:'+h+'px"></div>' +
          '<span class="bar-label">'+dateShort+'</span></div>';
      });
      html += '</div>';
      container.innerHTML = html;
    }
    renderDonut('statusDonut', d.status_distribution);
  }

  

function renderDonut(containerId, dist) {
    var el = document.getElementById(containerId);
    if (!el) return;
    if (!dist || !dist.length) { el.innerHTML = '<p class="chart-empty">暂无数据</p>'; return; }
    var total = 0;
    dist.forEach(function(x){ total += (x.count || 0); });
    if (total === 0) { el.innerHTML = '<p class="chart-empty">暂无数据</p>'; return; }
    var colors = {pending:'#f59e0b', processing:'#3b82f6', resolved:'#22c55e', closed:'#9ca3af'};
    var r = 54, cx = 70, cy = 70, C = 2 * Math.PI * r, sw = 18, offset = 0;
    var arcs = '', legend = '';
    dist.forEach(function(x){
      var frac = (x.count || 0) / total;
      var len = frac * C;
      var col = colors[x.status] || '#9ca3af';
      var label = statusLabels[x.status] || x.status;
      arcs += '<circle cx="'+cx+'" cy="'+cy+'" r="'+r+'" fill="none" stroke="'+col+'" stroke-width="'+sw+'" '+
        'stroke-dasharray="'+len.toFixed(2)+' '+(C-len).toFixed(2)+'" stroke-dashoffset="'+(-offset).toFixed(2)+'" transform="rotate(-90 '+cx+' '+cy+')"></circle>';
      offset += len;
      legend += '<div class="donut-legend-item"><span class="donut-dot" style="background:'+col+'"></span>'+esc(label)+' <b>'+(x.count||0)+'</b> ('+Math.round(frac*100)+'%)</div>';
    });
    var svg = '<svg width="140" height="140" viewBox="0 0 140 140" class="donut-svg">'+
      '<circle cx="'+cx+'" cy="'+cy+'" r="'+r+'" fill="none" stroke="var(--border)" stroke-width="'+sw+'"></circle>'+
      arcs+
      '<text x="'+cx+'" y="'+cy+'" text-anchor="middle" dominant-baseline="central" style="fill:var(--fg);font-size:20px;font-weight:700">'+total+'</text>'+
      '</svg>';
    el.innerHTML = '<div class="donut-container"><div class="donut-chart">'+svg+'</div><div class="donut-legend">'+legend+'</div></div>';
  }

  

function getFormTemplate(name) {
    if (!FORM_TEMPLATES[name]) return '[]';
    return JSON.stringify(FORM_TEMPLATES[name]);
  }

  

function _pa(s){ return String(s==null?'':s).replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/'/g,'&#39;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
  

function _pe(s){ var d=document.createElement('div'); d.textContent=s==null?'':s; return d.innerHTML; }
  

function _pr(f){ return f.required ? ' required' : ''; }
  

function _pat(n,v){ return (v!==undefined&&v!==null&&v!=='') ? ' '+n+'="'+_pa(String(v))+'"' : ''; }
  

function _pReq(f){ return f.required ? ' <span style="color:var(--priority-urgent-fg)">*</span>' : ''; }
  

function _pMd(s){
    var out=_pe(s||'');
    out=out.replace(/\*\*(.+?)\*\*/g,'<strong>$1</strong>');
    out=out.replace(/\*(.+?)\*/g,'<em>$1</em>');
    out=out.replace(/`(.+?)`/g,'<code>$1</code>');
    out=out.replace(/\n/g,'<br>');
    return out;
  }
  

function renderPreviewField(f){
    var fName='cf_'+f.name;
    var widthClass = f.width==='half' ? ' field-half' : f.width==='third' ? ' field-third' : '';
    var t=f.type;
    if (f.sys==='title') {
      return '<div class="field'+widthClass+'"><label for="'+fName+'">'+_pe(f.label||'标题')+_pReq(f)+'</label><input type="text" id="'+fName+'" name="'+fName+'"'+_pr(f)+' placeholder="'+_pa(f.placeholder||'')+'"'+_pat('maxlength',f.max_length)+_pat('minlength',f.min_length)+' disabled></div>';
    }
    if (f.sys==='description') {
      return '<div class="field'+widthClass+'"><label for="'+fName+'">'+_pe(f.label||'详细描述')+_pReq(f)+'</label><textarea id="'+fName+'" name="'+fName+'"'+_pr(f)+' placeholder="'+_pa(f.placeholder||'')+'"'+_pat('rows',f.rows||5)+_pat('maxlength',f.max_length)+' disabled style="min-height:80px"></textarea></div>';
    }
    if (f.sys==='category') {
      var ch='<div class="field'+widthClass+'"><label for="'+fName+'">'+_pe(f.label||'分类')+_pReq(f)+'</label><select id="'+fName+'" name="'+fName+'"'+_pr(f)+' disabled><option value="">请选择分类（选填）</option>';
      (currentProjectCategories||[]).forEach(function(cat){ ch+='<option value="'+_pa(cat.key)+'">'+_pe(cat.name)+'</option>'; });
      return ch+'</select></div>';
    }
    if (f.sys==='notify') {
      return '<div class="field"><label class="checkbox-label" style="display:flex;align-items:flex-start;gap:8px"><input type="checkbox" disabled style="margin-top:2px;width:auto"><span>📬 接收反馈处理通知<br><small style="color:var(--muted);font-weight:normal">勾选后可接收反馈状态更新与回复邮件通知。</small></span></label></div>';
    }
    if (f.sys==='images') {
      return '<div class="'+widthClass+'"><div class="file-area"><div class="label">'+_pe(f.label||'图片')+'</div><div class="file-input-wrap"><input type="file" multiple accept="image/*" disabled><span class="hint">点击或拖拽图片到此处</span></div></div></div>';
    }
    if (f.sys==='files') {
      return '<div class="'+widthClass+'"><div class="file-area"><div class="label">'+_pe(f.label||'附件')+'</div><div class="file-input-wrap"><input type="file" multiple disabled><span class="hint">点击或拖拽文件到此处</span></div></div></div>';
    }
    var html='';
    if (t==='text') html='<input type="text" disabled placeholder="'+_pa(f.placeholder||'')+'"'+_pat('minlength',f.min_length)+_pat('maxlength',f.max_length)+_pat('pattern',f.pattern)+'>';
    else if (t==='textarea') html='<textarea disabled placeholder="'+_pa(f.placeholder||'')+'"'+_pat('rows',f.rows||4)+_pat('maxlength',f.max_length)+' style="min-height:80px"></textarea>';
    else if (t==='number') html='<input type="number" disabled placeholder="'+_pa(f.placeholder||'')+'"'+_pat('min',f.min)+_pat('max',f.max)+_pat('step',f.step)+'>';
    else if (t==='email') html='<input type="email" disabled placeholder="'+_pa(f.placeholder||'')+'">';
    else if (t==='url') html='<input type="url" disabled placeholder="'+_pa(f.placeholder||'')+'">';
    else if (t==='tel') html='<input type="tel" disabled placeholder="'+_pa(f.placeholder||'')+'">';
    else if (t==='date') html='<input type="date" disabled>';
    else if (t==='time') html='<input type="time" disabled>';
    else if (t==='datetime') html='<input type="datetime-local" disabled>';
    else if (t==='month') html='<input type="month" disabled>';
    else if (t==='color') html='<input type="color" disabled value="'+_pa(f.default||'#000000')+'">';
    else if (t==='select') {
      var h='<select disabled><option value="">请选择...</option>';
      (f.options||[]).forEach(function(o){ h+='<option value="'+_pa(o)+'">'+_pe(o)+'</option>'; });
      html=h+'</select>';
    }
    else if (t==='checkbox') {
      var h='<div class="checkbox-group">';
      (f.options||[]).forEach(function(o){ h+='<label><input type="checkbox" disabled> '+_pe(o)+'</label>'; });
      html=h+'</div>';
    }
    else if (t==='radio') {
      var h='<div class="radio-group">';
      (f.options||[]).forEach(function(o){ h+='<label><input type="radio" disabled> '+_pe(o)+'</label>'; });
      html=h+'</div>';
    }
    else if (t==='rating') {
      var max=f.max||5; var sym=f.icon==='heart'?'❤':f.icon==='thumb'?'👍':'★';
      var h='<div class="rating-group">';
      for(var i=1;i<=max;i++){ h+='<span class="rating-star">'+sym+'</span>'; }
      html=h+'</div>';
    }
    else if (t==='scale') {
      var max=f.max||(f.options?f.options.length:5); if(max<1)max=5;
      var h='<div class="scale-group">';
      for(var i=1;i<=max;i++){ var lbl=(f.options&&f.options[i-1])?f.options[i-1]:String(i); h+='<label class="scale-option"><input type="radio" disabled> <span class="scale-label">'+_pe(lbl)+'</span></label>'; }
      html=h+'</div>';
    }
    else if (t==='toggle') {
      var on=f.default==='on'?' checked':'';
      html='<label class="toggle-switch"><input type="checkbox" disabled'+on+'><span class="toggle-slider"></span></label><span class="toggle-label">'+(f.default==='on'?(f.label_on||'已启用'):(f.label_off||'已禁用'))+'</span>';
    }
    else if (t==='slider') {
      html='<input type="range" disabled'+_pat('min',f.min||0)+_pat('max',f.max||100)+_pat('step',f.step||1)+'>';
    }
    else if (t==='tags') {
      html='<div class="tags-input"><input type="text" disabled placeholder="'+_pa(f.placeholder||'输入标签后回车')+'"><div class="tag-list"></div></div>';
    }
    else if (t==='markdown') {
      html='<textarea disabled placeholder="'+_pa(f.placeholder||'支持 Markdown 语法')+'" style="min-height:120px;font-family:monospace"></textarea>';
    }
    else if (t==='file') {
      html='<input type="file" disabled'+(f.multiple?' multiple':'')+_pat('accept',f.accept)+'>';
    }
    else if (t==='image') {
      html='<input type="file" accept="image/*" disabled'+(f.multiple?' multiple':'')+'>';
    }
    else if (t==='paragraph') {
      return '<div class="paragraph">'+_pMd(f.content||f.label||'')+'</div>';
    }
    else if (t==='divider') {
      return '<hr class="divider">';
    }
    else if (t==='section') {
      return '<div class="section-divider'+(f.collapsible?' collapsible':'')+'"><h3 class="section-title">'+_pe(f.label||'')+'</h3>'+(f.description?'<p class="section-desc">'+_pe(f.description)+'</p>':'')+'</div>';
    }
    else if (t==='html') {
      return '<div class="custom-html">'+(f.content||'')+'</div>';
    }
    else if (t==='hidden') {
      return '';
    }
    else {
      html='<p style="color:var(--danger)">未知字段类型: '+_pe(f.type)+'</p>';
    }
    var skipLabel = (t==='hidden'||t==='section'||t==='html'||t==='divider');
    var out='<div class="field'+widthClass+'">';
    if(!skipLabel) out+='<label>'+_pe(f.label||f.name||'')+_pReq(f)+'</label>';
    out+=html;
    if(f.help_text) out+='<p class="help-text">'+_pe(f.help_text)+'</p>';
    out+='</div>';
    return out;
  }
  

async function loadProjectDetail(id) {
    var resp = await api('/api/v1/admin/projects');
    if (!resp) return;
    var d = await resp.json();
    var project = (d.projects || []).find(function(p){ return p.id === id; });
    if (!project) { showToast('项目不存在', 'error'); showProjectList(); return; }
    try {
      var _ps = project.form_schema;
      if (typeof _ps === 'string') { _ps = JSON.parse(_ps); }
      if (typeof _ps === 'string') { _ps = JSON.parse(_ps); } // 处理双重编码
      currentFormSchema = Array.isArray(_ps) ? _ps : [];
    } catch(e) { currentFormSchema = []; }
    // When a project has no configured schema yet, seed the builder with the
    // default template so admins can see and edit exactly what the public page
    // will render (instead of an empty editor that hides the default fields).
    if (currentFormSchema.length === 0) {
      try { currentFormSchema = JSON.parse(getFormTemplate('empty')); } catch(e) { currentFormSchema = []; }
    }
    document.getElementById('projectListView').style.display = 'none';
    var dv = document.getElementById('projectDetailView');
    dv.style.display = '';
    var fbUrl = window.location.origin + '/fb/' + encodeURIComponent(project.slug);
    var statusText = project.is_active ? '启用' : '停用';
    var statusColor = project.is_active ? '#1a7a1a' : '#c00';
    var html = '<div class="project-detail-header"><h2>' + esc(project.name) + '</h2>' +
      '<span class="tag">' + esc(project.slug) + '</span>' +
      '<span style="color:'+statusColor+';font-size:.8rem">' + statusText + '</span>' +
      '<span style="flex:1"></span>' +
      '<button class="btn-sm" data-click="editProject" data-args="'+project.id+'">编辑基本信息</button></div>';
    if (project.description) html += '<p style="color:var(--tag-fg);font-size:.85rem;margin-bottom:16px">' + esc(project.description) + '</p>';
    html += '<div class="project-info-grid">' +
      '<div class="project-info-card"><div class="label">反馈链接</div><div class="value"><code>' + esc(fbUrl) + '</code> <a href="#" data-click="copyUrl" data-args="\''+fbUrl+'\'" style="font-size:.75rem">复制</a></div></div>' +
      '<div class="project-info-card"><div class="label">反馈数量</div><div class="value" style="font-size:1.2rem;font-weight:700">' + (project.feedback_count || 0) + '</div></div></div>';
    // Form schema builder
    html += '<div class="settings-card" style="margin-bottom:20px"><h2>自定义表单字段</h2>' +
      '<p style="font-size:.8rem;color:var(--muted);margin-bottom:12px">配置反馈页面除标题和描述外的额外收集字段。</p>' +
      '<div class="form-builder" id="formBuilder"><div id="formSchemaListContainer">';
    if (currentFormSchema.length === 0) {
      html += '<p style="color:var(--hint);font-size:.8rem;margin-bottom:8px">暂无自定义字段。</p>';
    } else {
      var typeLabels = {text:'单行文本',textarea:'多行文本',number:'数字',email:'邮箱',url:'网址',tel:'电话',date:'日期',time:'时间',datetime:'日期时间',month:'月份',color:'颜色',select:'下拉选择',checkbox:'复选框',radio:'单选',rating:'评分',toggle:'开关',slider:'滑块',scale:'量表',tags:'标签',markdown:'Markdown',file:'文件',image:'图片',hidden:'隐藏',section:'分区',html:'HTML',paragraph:'说明文字',divider:'分割线'};
      var sysLabels = {title:'标题',description:'描述',category:'分类',notify:'通知',images:'图片',files:'文件'};
      currentFormSchema.forEach(function(f, i){
        var typeLabel = typeLabels[f.type] || f.type;
        var reqMark = f.required ? ' <span style="color:var(--priority-urgent-fg)">*</span>' : '';
        var sysBadge = f.sys ? ' <span style="color:#fff;background:#3182ce;border-radius:3px;padding:0 4px;font-size:.7rem;margin-left:4px">系统:' + (sysLabels[f.sys]||f.sys) + '</span>' : '';
        var optInfo = (f.options && f.options.length > 0) ? ' &mdash; ' + f.options.length + ' 个选项' : '';
        html += '<div class="fb-field"><div class="fb-field-info"><div class="fb-field-title">' + esc(f.label) + reqMark + sysBadge + '</div>' +
          '<div class="fb-field-meta">' + typeLabel + ' &middot; <code>' + esc(f.name) + '</code>' + optInfo + '</div></div>' +
          '<div class="fb-field-actions">' +
          (i > 0 ? '<button data-click="moveField" data-args="'+i+',-1">&uarr;</button>' : '') +
          (i < currentFormSchema.length - 1 ? '<button data-click="moveField" data-args="'+i+',1">&darr;</button>' : '') +
          '<button data-click="editField" data-args="'+i+'">编辑</button>' +
          '<button class="del" data-click="removeField" data-args="'+i+'">删除</button></div></div>';
      });
    }
    html += '</div>'; // close formSchemaListContainer
    html += '<button class="fb-add-btn" data-click="addField" data-args="">+ 添加字段</button></div>';
    html += '<div class="settings-actions"><button class="btn-sm" data-click="previewForm" data-args="" style="margin-right:8px">预览表单</button><button class="btn-save" data-click="saveFormSchema" data-args="">保存表单配置</button></div></div>';
    html += '<div class="settings-card"><h2>最近反馈</h2><div id="projectFeedbacks"><p class="empty">加载中...</p></div></div>';
    document.getElementById('projectDetailContent').innerHTML = html;
    // Prefetch categories so the form preview can render the category selector.
    api('/api/v1/admin/projects/' + encodeURIComponent(project.slug) + '/categories')
      .then(function(r){ return r ? r.json() : null; })
      .then(function(cd){ currentProjectCategories = ((cd && cd.categories) || []).filter(function(c){ return c.is_active; }); })
      .catch(function(){ currentProjectCategories = []; });
    loadProjectFeedbacks(project.slug);
  }

  

async function loadProjectFeedbacks(slug) {
    var resp = await api('/api/v1/admin/feedbacks?limit=20&project=' + encodeURIComponent(slug));
    if (!resp) return;
    var d = await resp.json();
    var fbs = d.feedbacks || [];
    var container = document.getElementById('projectFeedbacks');
    if (!container) return;
    if (fbs.length === 0) { container.innerHTML = '<p class="empty">暂无反馈</p>'; return; }
    var html = '<div class="table-wrap"><table><thead><tr><th>ID</th><th>标题</th><th>状态</th><th>IP</th><th>时间</th></tr></thead><tbody>';
    fbs.forEach(function(f){
      var dt = f.created_at ? f.created_at.replace('T',' ').substring(0,16) : '-';
      html += '<tr class="clickable" data-click="showDetail" data-args="'+f.id+'"><td>#'+f.id+'</td><td>'+esc(f.title)+'</td><td>'+statusBadge(f.status)+'</td><td style="font-family:monospace;font-size:.8rem;color:var(--muted)">'+esc(f.client_ip)+'</td><td style="color:var(--muted);font-size:.8rem">'+dt+'</td></tr>';
    });
    html += '</tbody></table></div>';
    container.innerHTML = html;
  }

  

function renderFormSchemaList() {
    var container = document.getElementById('formSchemaListContainer');
    if (!container) return;
    var html = '';
    if (!currentFormSchema || currentFormSchema.length === 0) {
      html = '<p style="color:var(--hint);font-size:.8rem;margin-bottom:8px">暂无自定义字段。</p>';
    } else {
      var typeLabels = {text:'单行文本',textarea:'多行文本',number:'数字',email:'邮箱',url:'网址',tel:'电话',date:'日期',time:'时间',datetime:'日期时间',month:'月份',color:'颜色',select:'下拉选择',checkbox:'复选框',radio:'单选',rating:'评分',toggle:'开关',slider:'滑块',scale:'量表',tags:'标签',markdown:'Markdown',file:'文件',image:'图片',hidden:'隐藏',section:'分区',html:'HTML',paragraph:'说明文字',divider:'分割线'};
      var sysLabels = {title:'标题',description:'描述',category:'分类',notify:'通知',images:'图片',files:'文件'};
      currentFormSchema.forEach(function(f, i){
        var typeLabel = typeLabels[f.type] || f.type;
        var reqMark = f.required ? ' <span style="color:var(--priority-urgent-fg)">*</span>' : '';
        var sysBadge = f.sys ? ' <span style="color:#fff;background:#3182ce;border-radius:3px;padding:0 4px;font-size:.7rem;margin-left:4px">系统:' + (sysLabels[f.sys]||f.sys) + '</span>' : '';
        var optInfo = (f.options && f.options.length > 0) ? ' &mdash; ' + f.options.length + ' 个选项' : '';
        html += '<div class="fb-field"><div class="fb-field-info"><div class="fb-field-title">' + esc(f.label) + reqMark + sysBadge + '</div>' +
          '<div class="fb-field-meta">' + typeLabel + ' &middot; <code>' + esc(f.name) + '</code>' + optInfo + '</div></div>' +
          '<div class="fb-field-actions">' +
          (i > 0 ? '<button data-click="moveField" data-args="'+i+',-1">&uarr;</button>' : '') +
          (i < currentFormSchema.length - 1 ? '<button data-click="moveField" data-args="'+i+',1">&darr;</button>' : '') +
          '<button data-click="editField" data-args="'+i+'">编辑</button>' +
          '<button class="del" data-click="removeField" data-args="'+i+'">删除</button></div></div>';
      });
    }
    container.innerHTML = html;
  }
  

function auditQueryString() {
    var p = [];
    var action = document.getElementById('auditFilterAction');
    var user = document.getElementById('auditFilterUser');
    var from = document.getElementById('auditFilterFrom');
    var to = document.getElementById('auditFilterTo');
    if (action && action.value) p.push('action=' + encodeURIComponent(action.value));
    if (user && user.value.trim()) p.push('user=' + encodeURIComponent(user.value.trim()));
    if (from && from.value) p.push('from=' + encodeURIComponent(from.value));
    if (to && to.value) p.push('to=' + encodeURIComponent(to.value));
    return p.length ? ('&' + p.join('&')) : '';
  }

  

async function loadAuditLogs() {
    var resp = await api('/api/v1/admin/audit-logs?limit=100' + auditQueryString());
    if (!resp) return;
    var d = await resp.json();
    var logs = d.logs || [];
    document.getElementById('auditCount').textContent = '共 ' + (d.total || 0) + ' 条';
    var tbody = document.getElementById('auditTable');
    if (logs.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty">暂无审计记录</td></tr>';
      return;
    }
    tbody.innerHTML = logs.map(function(l){
      var dt = l.created_at ? l.created_at.replace('T',' ').substring(0,19) : '-';
      return '<tr><td>'+dt+'</td><td>'+esc(l.action)+'</td><td>'+esc(l.detail)+'</td><td>'+esc(l.user)+'</td><td style="font-family:monospace;font-size:.8rem;color:var(--muted)">'+esc(l.ip)+'</td></tr>';
    }).join('');
  }

  

async function loadEmailSettings() {
    var resp = await api('/api/v1/admin/config/email');
    if (!resp) return;
    var d = await resp.json();
    var configs = d.config || [];
    var labels = { smtp_host:'SMTP 服务器', smtp_port:'SMTP 端口', smtp_user:'SMTP 用户名', smtp_pass:'SMTP 密码', smtp_from:'发件人地址', smtp_to:'收件人地址（多个用逗号分隔）', notify_enable:'启用邮件通知' };
    var html = '';
    configs.forEach(function(c){
      var label = labels[c.key] || c.key;
      if (c.key === 'notify_enable') {
        var checked = c.value === 'true' ? ' checked' : '';
        html += '<div class="settings-field"><label>'+label+'</label><div class="toggle"><input type="checkbox" id="ecfg_'+c.key+'"'+checked+'><span>'+(c.value==='true'?'已启用':'已禁用')+'</span></div></div>';
      } else if (c.key === 'smtp_pass') {
        html += '<div class="settings-field"><label>'+label+'</label><input type="password" id="ecfg_'+c.key+'" value="'+esc(c.value)+'"></div>';
      } else {
        html += '<div class="settings-field"><label>'+label+'</label><input type="text" id="ecfg_'+c.key+'" value="'+esc(c.value)+'"></div>';
      }
    });
    document.getElementById('emailForm').innerHTML = html;
  }

  

async function loadAccountSettings() {
    var resp = await api('/api/v1/admin/config/account');
    if (!resp) return;
    var d = await resp.json();
    document.getElementById('accountForm').innerHTML =
      '<div class="settings-field"><label>当前用户名</label><input type="text" id="accUser" value="'+esc(d.username)+'"></div>' +
      '<div class="settings-field"><label>当前密码</label><input type="password" id="accOldPwd" placeholder="修改用户名或密码需验证当前密码"></div>' +
      '<div class="settings-field"><label>新密码（留空不修改）</label><input type="password" id="accNewPwd" placeholder="至少 8 位，含大小写和数字"></div>' +
      '<div class="settings-field"><label>确认新密码</label><input type="password" id="accNewPwd2" placeholder="再次输入新密码"></div>';
  }

  

async function loadSystemSettings() {
    var resp = await api('/api/v1/admin/config/system');
    if (!resp) return;
    var d = await resp.json();
    document.getElementById('systemForm').innerHTML =
      '<div class="settings-field"><label>系统基础 URL</label><input type="text" id="sysBase" value="'+esc(d.base_url)+'"><div class="hint">用于邮件通知中的链接</div></div>' +
      '<div class="settings-field"><label>PoW 难度（前导零位数）</label><input type="number" id="sysPoW" value="'+d.pow_difficulty+'" min="1" max="10"><div class="hint">越高越安全，但客户端计算时间更长</div></div>' +
      '<div class="settings-field"><label>每小时提交上限（每 IP）</label><input type="number" id="sysRate" value="'+d.rate_limit_per_hr+'" min="1"></div>' +
      '<div class="settings-field"><label>Webhook 通知 URL</label><input type="text" id="sysWebhook" value="'+esc(d.webhook_url||'')+'"><div class="hint">新反馈将 POST JSON 到此 URL（留空禁用）</div></div>' +
      '<div class="settings-field"><label>Webhook 类型</label><select id="sysWebhookType">' +
        '<option value="auto"'+(d.webhook_type==='auto'?' selected':'')+'>自动检测</option>' +
        '<option value="feishu"'+(d.webhook_type==='feishu'?' selected':'')+'>飞书</option>' +
        '<option value="dingtalk"'+(d.webhook_type==='dingtalk'?' selected':'')+'>钉钉</option>' +
        '<option value="slack"'+(d.webhook_type==='slack'?' selected':'')+'>Slack</option>' +
        '<option value="wecom"'+(d.webhook_type==='wecom'?' selected':'')+'>企业微信</option>' +
      '</select><div class="hint">默认根据 URL 自动检测平台类型</div></div>' +
      '<div class="settings-field"><label>CDN 提供商</label><select id="sysCdnProvider">' +
        '<option value="auto"'+(d.cdn_provider==='auto'?' selected':'')+'>自动检测（全部 Header）</option>' +
        '<option value="cloudflare"'+(d.cdn_provider==='cloudflare'?' selected':'')+'>Cloudflare</option>' +
        '<option value="generic"'+(d.cdn_provider==='generic'?' selected':'')+'>通用代理（Nginx/CDN）</option>' +
        '<option value="none"'+(d.cdn_provider==='none'?' selected':'')+'>不使用 CDN（直连模式）</option>' +
      '</select><div class="hint">影响如何从 HTTP Header 中获取真实客户端 IP</div></div>' +
      '<div class="settings-field"><label>可信代理 IP</label><input type="text" id="sysTrustedProxies" value="'+esc(d.trusted_proxies||'')+'" placeholder="例: 10.0.0.1, 172.16.0.0/12 或 *"><div class="hint">逗号分隔，仅当请求来自这些代理时才读取 CDN Header。填 * 表示信任所有来源</div></div>';
    loadAnnouncementSettings();
  }

  

async function loadAnnouncementSettings() {
    var resp = await api('/api/v1/admin/config/announcement');
    if (!resp) return;
    var d = await resp.json();
    document.getElementById('announcementForm').innerHTML =
      '<div class="settings-field"><div style="display:flex;gap:8px;margin-bottom:6px">' +
        '<select id="annEnabled" style="flex:0 0 110px"><option value="1"'+(d.enabled?' selected':'')+'>启用</option><option value="0"'+(!d.enabled?' selected':'')+'>停用</option></select>' +
        '<select id="annLevel" style="flex:0 0 130px"><option value="info"'+(d.level==='info'?' selected':'')+'>ℹ️ 提示</option><option value="warning"'+(d.level==='warning'?' selected':'')+'>⚠️ 警告</option><option value="success"'+(d.level==='success'?' selected':'')+'>✅ 成功</option><option value="danger"'+(d.level==='danger'?' selected':'')+'>🚫 重要</option></select>' +
        '<select id="annType" style="flex:1"><option value="text"'+(d.content_type!=='html'?' selected':'')+'>纯文本</option><option value="html"'+(d.content_type==='html'?' selected':'')+'>HTML 代码</option></select>' +
      '</div>' +
      '<textarea id="annContent" rows="3" placeholder="公告内容">'+esc(d.content||'')+'</textarea>' +
      '<div class="field-row" style="margin-top:8px"><label style="display:flex;align-items:center;gap:6px"><input type="checkbox" id="annDismiss" '+(d.dismissible?'checked':'')+'> 允许用户关闭（关闭后当天同一浏览器不再显示）</label></div>' +
      '</div>';
  }

  

async function loadCurrentUser() {
    var resp = await api('/api/v1/admin/me');
    if (!resp) return;
    var d = await resp.json();
    currentUsername = d.username || '';
    currentUserRole = d.role || '';
    document.getElementById('currentUserLabel').textContent = currentUsername + (currentUserRole ? ' (' + (currentUserRole === 'admin' ? '管理员' : currentUserRole === 'manager' ? '经理' : currentUserRole === 'editor' ? '编辑' : '只读') + ')' : '');
    // Show team tab only for admin role
    var teamBtn = document.getElementById('navTeam');
    if (teamBtn) teamBtn.style.display = currentUserRole === 'admin' ? '' : 'none';
    // Show knowledge base tab for editor and above (FAQ management requires editor role)
    var kbBtn = document.getElementById('navKb');
    if (kbBtn) kbBtn.style.display = (currentUserRole && currentUserRole !== 'viewer') ? '' : 'none';
    // Show advanced settings tabs (API tokens / email template / data ops /
    // webhooks) to all operational roles; read-only viewers stay restricted.
    var canSeeAdvanced = currentUserRole && currentUserRole !== 'viewer';
    ['navSettingsTokens','navSettingsEmailTpl','navSettingsDataOps','navSettingsWebhooks'].forEach(function(id){
      var el = document.getElementById(id);
      if (el) el.style.display = canSeeAdvanced ? '' : 'none';
    });
  }

  

async function loadInvitations() {
    var container = document.getElementById('invitationList');
    if (!container) return;
    var resp = await api('/api/v1/admin/invitations');
    if (!resp) return;
    var d = await resp.json();
    var invites = d.invitations || [];
    if (invites.length === 0) { container.innerHTML = ''; return; }
    var html = '<h3 style="font-size:.9rem;margin-bottom:8px">邀请记录</h3><table style="width:100%;font-size:.8rem"><thead><tr><th>链接</th><th>角色</th><th>已用/上限</th><th>创建者</th><th>创建时间</th><th>状态</th></tr></thead>';
    invites.forEach(function(inv){
      var status = inv.expired ? '<span style="color:var(--hint)">已失效</span>' : '<span style="color:var(--online)">有效</span>';
      html += '<tr><td style="font-family:monospace;font-size:.7rem;max-width:200px;overflow:hidden;text-overflow:ellipsis">' + esc(inv.url) + '</td>' +
        '<td>' + inv.role + '</td>' +
        '<td>' + inv.used_count + '/' + inv.max_uses + '</td>' +
        '<td>' + esc(inv.created_by) + '</td>' +
        '<td>' + (inv.created_at ? new Date(inv.created_at*1000).toLocaleDateString() : '-') + '</td>' +
        '<td>' + status + '</td></tr>';
    });
    html += '</table>';
    container.innerHTML = html;
  }

  

function fmtUnix(ts) {
    if (!ts) return '-';
    var d = new Date(ts * 1000);
    var p = function(n){ return (n < 10 ? '0' : '') + n; };
    return d.getFullYear() + '-' + p(d.getMonth()+1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  }

  

async function loadAdmins() {
    var resp = await api('/api/v1/admin/admins');
    if (!resp) return;
    var d = await resp.json();
    var admins = d.admins || [];
    // 排除超管（id=1，安装引导时创建）
    admins = admins.filter(function(a){ return a.id !== 1; });
    var tbody = document.getElementById('adminTable');
    if (admins.length === 0) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty">暂无其他团队成员</td></tr>';
      return;
    }
    var roleLabels = {admin:'管理员',manager:'经理',editor:'编辑',viewer:'只读'};
    tbody.innerHTML = admins.map(function(a){
      var dt = a.created_at ? a.created_at.replace('T',' ').substring(0,16) : '-';
      var loginDt = (a.last_login_at && a.last_login_at > 0) ? fmtUnix(a.last_login_at) : '从未登录';
      var statusText = a.is_active ? '<span style="color:var(--success-fg)">启用</span>' : '<span style="color:var(--priority-urgent-fg)">停用</span>';
      var isSelf = a.username === currentUsername;
      var email = a.email || '-';
      var roleBadge = '<span class="tag">'+(roleLabels[a.role] || a.role)+(isSelf ? ' <span style="color:var(--muted);font-size:.7rem">(我)</span>' : '')+'</span>';
      var actions = '<button class="btn-sm" data-click="editAdmin" data-args="'+a.id+',\''+esc(a.username)+'\',\''+esc(email)+'\',\''+a.role+'\','+a.is_active+'">编辑</button>' +
        ' <button class="btn-sm" data-click="editMemberGrants" data-args="'+a.id+'">授权</button>';
      if (!isSelf) {
        actions += ' <button class="btn-sm btn-danger" data-click="deleteAdmin" data-args="'+a.id+',\''+esc(a.username)+'\'">删除</button>';
      }
      return '<tr>' +
        '<td>'+esc(a.username)+(isSelf ? ' <span style="color:var(--muted);font-size:.75rem">(我)</span>' : '')+'</td>' +
        '<td style="color:var(--muted);font-size:.8rem">'+esc(email)+'</td>' +
        '<td>'+roleBadge+'</td>' +
        '<td>'+statusText+'</td>' +
        '<td style="color:var(--muted);font-size:.8rem">'+dt+'</td>' +
        '<td style="color:var(--muted);font-size:.8rem">'+loginDt+'</td>' +
        '<td>'+actions+'</td>' +
        '</tr>';
    }).join('');
  }

  

async function renderCreateGrants() {
    var area = document.getElementById('createGrantsArea');
    if (!area) return;
    if (!allProjects || allProjects.length === 0) {
      var pResp = await api('/api/v1/admin/projects');
      if (pResp) { var pd = await pResp.json(); allProjects = pd.projects || []; }
    }
    if (!allProjects || allProjects.length === 0) {
      area.innerHTML = '<p style="font-size:.8rem;color:var(--hint)">暂无可用项目</p>';
      return;
    }
    var html = '';
    allProjects.forEach(function(p){
      html += '<div class="grant-project-row" style="margin-bottom:8px;padding:6px;border:1px solid var(--border-soft);border-radius:4px">';
      html += '<label style="display:flex;align-items:center;gap:6px;font-size:.82rem;font-weight:500">';
      html += '<input type="checkbox" class="cgrant-proj-cb" data-slug="'+esc(p.slug)+'" style="width:auto"> ';
      html += esc(p.name)+' <span style="color:var(--hint);font-size:.72rem">('+esc(p.slug)+')</span></label>';
      html += '<div class="cgrant-detail" style="display:none;margin-top:6px;padding-left:20px">';
      html += '<div style="display:flex;align-items:center;gap:8px;margin-bottom:4px"><span style="font-size:.78rem;color:var(--tag-fg)">角色:</span>';
      html += '<select class="cgrant-role" data-slug="'+esc(p.slug)+'" style="font-size:.78rem;padding:2px 4px">';
      html += '<option value="viewer">只读</option><option value="editor" selected>编辑</option><option value="manager">经理</option></select></div>';
      html += '<div class="cgrant-cats" data-slug="'+esc(p.slug)+'" style="font-size:.78rem;margin-top:4px"></div>';
      html += '</div></div>';
    });
    area.innerHTML = html;
    area.querySelectorAll('.cgrant-proj-cb').forEach(function(cb){
      cb.addEventListener('change', function(){
        var detail = this.closest('.grant-project-row').querySelector('.cgrant-detail');
        detail.style.display = this.checked ? '' : 'none';
        if (this.checked) loadCreateCategories(this.getAttribute('data-slug'));
      });
    });
  }

  

async function loadCreateCategories(slug) {
    var cResp = await api('/api/v1/admin/projects/'+encodeURIComponent(slug)+'/categories');
    var cats = [];
    if (cResp) { var cd = await cResp.json(); cats = (cd.categories||[]).filter(function(c){ return c.is_active; }); }
    var area = document.querySelector('.cgrant-cats[data-slug="'+CSS.escape(slug)+'"]');
    if (!area) return;
    if (cats.length === 0) { area.innerHTML = '<span style="color:var(--hint)">该项目暂无分类</span>'; return; }
    var h = '<div style="color:var(--hint);font-size:.72rem;margin-bottom:2px">分类(不选=全部):</div>';
    cats.forEach(function(cat){
      h += '<label style="display:inline-flex;align-items:center;gap:3px;margin-right:6px;font-size:.75rem;cursor:pointer">';
      h += '<input type="checkbox" class="cgrant-cat" data-slug="'+esc(slug)+'" data-cat="'+esc(cat.key)+'" style="width:auto"> '+esc(cat.name)+'</label>';
    });
    area.innerHTML = h;
  }

  

function collectCreateGrants() {
    var grants = [];
    var area = document.getElementById('createGrantsArea');
    if (!area) return grants;
    area.querySelectorAll('.cgrant-proj-cb').forEach(function(cb){
      if (!cb.checked) return;
      var slug = cb.getAttribute('data-slug');
      var roleSel = area.querySelector('.cgrant-role[data-slug="'+CSS.escape(slug)+'"]');
      var role = roleSel ? roleSel.value : 'editor';
      var cats = [];
      area.querySelectorAll('.cgrant-cat[data-slug="'+CSS.escape(slug)+'"]').forEach(function(catCb){
        if (catCb.checked) cats.push(catCb.getAttribute('data-cat'));
      });
      if (cats.length === 0) grants.push({project_slug:slug, category_key:'*', role:role});
      else cats.forEach(function(c){ grants.push({project_slug:slug, category_key:c, role:role}); });
    });
    return grants;
  }

  

function fallbackMarkDuplicate(id) {
    var targetId = prompt('请输入主反馈的 ID：');
    if (!targetId) return;
    targetId = parseInt(targetId);
    if (!targetId || targetId === id) { showToast('无效的目标 ID', 'error'); return; }
    apiJSON('/api/v1/admin/feedbacks/' + id + '/duplicate', {
      method: 'POST',
      body: JSON.stringify({duplicate_of: targetId})
    }).then(function(d) {
      if (!d) return;
      if (d.error) { showToast(d.error, 'error'); return; }
      showToast(d.message || '已标记为重复', 'success');
      showDetail(id);
      loadFeedbacks();
    });
  }

  

function esc(s) { if (!s) return ''; var d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
  

function showToast(msg, type) {
    var t = document.getElementById('toast');
    t.textContent = msg; t.className = 'toast ' + type;
    setTimeout(function(){ t.className = 'toast'; }, 3000);
  }

  

function handleHash() {
    var hash = window.location.hash.replace('#','');
    if (hash.startsWith('feedback/')) {
      var id = parseInt(hash.split('/')[1]);
      if (id && currentTab === 'dashboard') { switchTab('dashboard'); setTimeout(function(){ showDetailInner(id); }, 100); }
      return;
    }
    if (hash.startsWith('project/')) {
      var pid = parseInt(hash.split('/')[1]);
      if (pid) {
        currentTab = 'projects';
        document.querySelectorAll('.nav button').forEach(function(b){ b.classList.toggle('active', b.dataset.tab === 'projects'); });
        ['dashboard','pending','roadmap','projects','team','audit','settings','kb'].forEach(function(t){ var el = document.getElementById('tab-'+t); if(el) el.style.display = t === 'projects' ? '' : 'none'; });
        loadProjects().then(function(){ viewProjectDetail(pid); });
      }
      return;
    }
    var validTabs = ['dashboard','pending','roadmap','projects','team','audit','settings','kb'];
    if (validTabs.indexOf(hash) >= 0) { switchTab(hash); }
    else { currentTab = 'dashboard'; window.location.hash = 'dashboard'; }
  }

  

function getSelectedIds() {
    return Array.from(selectedIds);
  }

  

async function loadTokens() {
    var resp = await api('/api/v1/admin/api-tokens');
    if (!resp) return;
    var d = await resp.json();
    var tokens = d.tokens || d || [];
    if (!Array.isArray(tokens)) tokens = [];
    var container = document.getElementById('tokenList');
    if (!tokens || tokens.length === 0) {
      container.innerHTML = '<p style="font-size:.85rem;color:var(--hint)">暂无 API Token</p>';
      return;
    }
    var html = '<table style="width:100%"><thead><tr><th>名称</th><th>Token</th><th>项目</th><th>限速/时</th><th>配额/日</th><th>状态</th><th>最后使用</th><th>操作</th></tr></thead><tbody>';
    tokens.forEach(function(t){
      var masked = t.token ? t.token.substring(0, 8) + '...' + t.token.substring(t.token.length - 4) : '-';
      var lastUsed = t.last_used_at ? t.last_used_at : '从未';
      html += '<tr>' +
        '<td>' + esc(t.name) + '</td>' +
        '<td><code style="font-size:.8rem">' + esc(masked) + '</code></td>' +
        '<td>' + esc(t.project_id || '全部') + '</td>' +
        '<td style="font-size:.8rem;color:var(--tag-fg)">' + (t.rate_limit ? t.rate_limit : '∞') + '</td>' +
        '<td style="font-size:.8rem;color:var(--tag-fg)">' + (t.quota_per_day ? t.quota_per_day : '∞') + '</td>' +
        '<td>' + (t.is_active ? '<span style="color:green">启用</span>' : '<span style="color:var(--hint)">禁用</span>') + '</td>' +
        '<td style="font-size:.8rem;color:var(--muted)">' + esc(lastUsed) + '</td>' +
        '<td>' +
          '<button class="btn-sm" data-click="toggleToken" data-args="' + t.id + ',' + !t.is_active + '" style="font-size:.75rem">' + (t.is_active ? '禁用' : '启用') + '</button> ' +
          '<button class="btn-sm" data-click="rotateToken" data-args="' + t.id + '" style="font-size:.75rem">重新生成</button> ' +
          '<button class="btn-sm" data-click="openTokenStats" data-args="' + t.id + '" style="font-size:.75rem">统计</button> ' +
          '<button class="btn-sm btn-danger" data-click="deleteToken" data-args="' + t.id + '" style="font-size:.75rem">删除</button>' +
        '</td></tr>';
    });
    html += '</tbody></table>';
    container.innerHTML = html;
  }

  

async function loadWebhooks() {
    var resp = await api('/api/v1/admin/webhooks');
    if (!resp) return;
    var d = await resp.json();
    var subs = d.subscriptions || [];
    var container = document.getElementById('webhookList');
    if (!container) return;
    if (subs.length === 0) { container.innerHTML = '<p style="font-size:.85rem;color:var(--hint)">暂无 Webhook 订阅</p>'; return; }
    var html = '<table style="width:100%"><thead><tr><th>ID</th><th>项目</th><th>URL</th><th>事件</th><th>状态</th><th>操作</th></tr></thead><tbody>';
    subs.forEach(function(s){
      html += '<tr>' +
        '<td>'+s.id+'</td>' +
        '<td>'+esc(s.project_slug || '全部')+'</td>' +
        '<td style="font-size:.8rem;max-width:240px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"><code>'+esc(s.url)+'</code></td>' +
        '<td style="font-size:.8rem;color:var(--tag-fg)">'+esc(s.events || '*')+'</td>' +
        '<td>'+(s.is_active ? '<span style="color:green">启用</span>' : '<span style="color:var(--hint)">禁用</span>')+'</td>' +
        '<td><button class="btn-sm" data-click="editWebhook" data-args="'+s.id+'" style="font-size:.75rem">编辑</button> ' +
        '<button class="btn-sm" data-click="testWebhook" data-args="'+s.id+'" style="font-size:.75rem">测试</button> ' +
        '<button class="btn-sm" data-click="showWebhookDeliveries" data-args="'+s.id+'" style="font-size:.75rem">历史</button> ' +
        '<button class="btn-sm btn-danger" data-click="deleteWebhook" data-args="'+s.id+'" style="font-size:.75rem">删除</button></td>' +
        '</tr>';
    });
    html += '</tbody></table>';
    container.innerHTML = html;
  }
  

async function loadEmailTemplate() {
    var resp = await api('/api/v1/admin/config/email-template');
    if (!resp) return;
    var d = await resp.json();
    document.getElementById('tplSubject').value = d.subject_template || '';
    document.getElementById('tplBody').value = d.body_template || '';
  }

  

function loadDataOps() {
    // Load project list for import dropdown
    var sel = document.getElementById('importProjectId');
    if (sel && sel.options.length <= 1) {
      allProjects.forEach(function(p){
        var opt = document.createElement('option');
        opt.value = p.slug;
        opt.textContent = p.name;
        sel.appendChild(opt);
      });
    }
  }

  

async function loadBackupList() {
    var container = document.getElementById('backupListContainer');
    if (!container) return;
    var resp = await api('/api/v1/admin/backups');
    if (!resp) return;
    var d = await resp.json();
    var bk = d.backups || [];
    if (bk.length === 0) {
      container.innerHTML = '<p style="font-size:.8rem;color:var(--muted)">暂无备份文件。</p>';
      return;
    }
    var html = '<table style="width:100%;font-size:.8rem"><tr><th>文件名</th><th>大小</th><th>时间</th><th>操作</th></tr>';
    bk.forEach(function(b){
      var enc = encodeURIComponent(b.name);
      html += '<tr><td style="font-family:monospace;font-size:.75rem">' + esc(b.name) + '</td>' +
        '<td>' + (b.size_str || b.size) + '</td>' +
        '<td>' + b.modified + '</td>' +
        '<td><a href="/api/v1/admin/system/backup/download?file=' + enc + '" class="btn-sm" download>下载</a></td></tr>';
    });
    html += '</table>';
    container.innerHTML = html;
  }

  

function updateAdminLb() {
    if (!adminLbItems.length) return;
    if (adminLbIndex < 0) adminLbIndex = 0;
    if (adminLbIndex >= adminLbItems.length) adminLbIndex = adminLbItems.length - 1;
    var item = adminLbItems[adminLbIndex];
    var img = document.getElementById('adminLbImg');
    var cap = document.getElementById('adminLbCap');
    var dl = document.getElementById('adminLbDownload');
    var prev = document.getElementById('adminLbPrev');
    var next = document.getElementById('adminLbNext');
    if (img) img.src = '/api/v1/admin/feedbacks/' + adminLbFbId + '/thumb?note=' + adminLbNoteId + '&i=' + item.idx;
    if (cap) cap.textContent = item.fp.split('/').pop();
    if (dl) dl.setAttribute('href', '/admin/files/' + item.fp);
    if (prev) prev.style.display = (adminLbItems.length > 1) ? '' : 'none';
    if (next) next.style.display = (adminLbItems.length > 1) ? '' : 'none';
  }

  

window.switchTab = function(tab) {
    currentTab = tab;
    window.location.hash = tab;
    document.querySelectorAll('.nav button').forEach(function(b){
      b.classList.toggle('active', b.dataset.tab === tab);
    });
    ['dashboard','pending','roadmap','projects','team','audit','settings','kb'].forEach(function(t){
      var el = document.getElementById('tab-'+t);
      if (el) el.style.display = t === tab ? '' : 'none';
    });
    if (tab === 'projects') { showProjectList(); loadProjects(); loadProjectStats(); }
    if (tab === 'settings') { loadEmailSettings(); loadBackupList(); }
    if (tab === 'dashboard') { showList(); loadStats(); loadFeedbacks(); loadChart(); }
    if (tab === 'audit') loadAuditLogs();
    if (tab === 'team') { loadAdmins(); loadInvitations(); }
    if (tab === 'kb') { renderKbProjects(); loadFaqs(); }
    if (tab === 'pending') loadPending();
    if (tab === 'roadmap') loadRoadmap();
  }

window.deleteFaq = async function(id) {
    if (!confirm('确定删除该 FAQ？此操作不可恢复。')) return;
    var resp = await api('/api/v1/admin/projects/' + encodeURIComponent(kbProjectSlug) + '/faqs/' + id, {
      method: 'DELETE',
      headers: getCsrfHeaders()
    });
    if (!resp) return;
    if (resp.status === 404) { showToast('FAQ 不存在', 'error'); return; }
    if (!resp.ok) { showToast('删除失败', 'error'); return; }
    showToast('已删除', 'success');
    await loadFaqs();
  }

  

window.openPdf = function() {
    var e = arguments[arguments.length - 1];
    var t = e && e.target ? e.target.closest('[data-click]') : null;
    var fp = t ? t.getAttribute('data-pdf') : '';
    if (!fp) return;
    var src = '/admin/files/' + fp;
    var frame = document.getElementById('pdfFrame');
    var dl = document.getElementById('pdfDownload');
    if (frame) frame.src = src;
    if (dl) dl.setAttribute('href', src);
    var m = document.getElementById('pdfModal');
    if (m) m.classList.add('active');
  }

window.closePdf = function() {
    var m = document.getElementById('pdfModal');
    if (m) m.classList.remove('active');
    var frame = document.getElementById('pdfFrame');
    if (frame) frame.src = '';
  }

window.debounceSearch = function() {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(function(){ loadFeedbacks(); }, 300);
  }

window.populateCategoryFilter = async function() {
    var cSel = document.getElementById('categoryFilter');
    var project = document.getElementById('projectFilter').value;
    cSel.innerHTML = '<option value="">全部分类</option>';
    if (!project) return;
    try {
      var resp = await api('/api/v1/admin/projects/' + encodeURIComponent(project) + '/categories');
      if (!resp) return;
      var d = await resp.json();
      (d.categories || []).forEach(function(cat){
        if (cat.is_active === false) return;
        var opt = document.createElement('option');
        opt.value = cat.key; opt.textContent = cat.name || cat.key;
        cSel.appendChild(opt);
      });
    } catch (e) { /* ignore network errors */ }
  }

window.prevPage = function() {
    if (feedbackOffset >= feedbackLimit) { feedbackOffset -= feedbackLimit; loadFeedbacks(); }
  }

window.nextPage = function() {
    feedbackOffset += feedbackLimit; loadFeedbacks();
  }

window.toggleSelect = function(id, checked) {
    if (checked) selectedIds.add(id); else selectedIds.delete(id);
    updateBulkBar();
  }

window.showDetail = function(id) {
    window.location.hash = 'feedback/' + id;
    showDetailInner(id);
  }

window.toggleMedia = function(kind, fbId) {
    var shotsPanel = document.getElementById('mediaShots');
    var filesPanel = document.getElementById('mediaFiles');
    if (kind === 'shots') {
      if (shotsPanel.style.display !== 'block') {
        if (!shotsPanel.dataset.rendered) {
          var grid = '<div class="thumb-grid">';
          (adminImgFiles[fbId] || []).forEach(function(it, i){
            var nm = it.fp.split('/').pop();
            grid += '<div class="thumb-item" data-click="openAdminLb" data-args="' + i + ',0">' +
              '<img src="/api/v1/admin/feedbacks/' + fbId + '/thumb?note=0&i=' + it.idx + '" alt="' + esc(nm) + '" loading="lazy">' +
              '<span class="thumb-name">' + esc(nm) + '</span></div>';
          });
          grid += '</div>';
          shotsPanel.innerHTML = grid;
          shotsPanel.dataset.rendered = '1';
        }
        shotsPanel.style.display = 'block';
      } else {
        shotsPanel.style.display = 'none';
      }
    } else if (kind === 'files') {
      if (filesPanel.style.display !== 'block') {
        if (!filesPanel.dataset.rendered) {
          var list = '<div style="margin-top:8px;display:flex;flex-wrap:wrap;gap:8px">';
          (adminOtherFiles[fbId] || []).forEach(function(fp){
            var nm = fp.split('/').pop();
            list += '<a class="file-chip" href="/admin/files/' + esc(fp) + '" target="_blank" rel="noopener" download style="display:inline-block;padding:4px 10px;background:var(--panel);border:1px solid var(--border);border-radius:8px;color:var(--accent);text-decoration:none;font-size:.8rem">' + esc(nm) + '</a>';
            if (/\.pdf$/i.test(fp)) {
              list += '<button type="button" class="file-chip pdf-preview" data-click="openPdf" data-pdf="' + esc(fp) + '">预览</button>';
            }
          });
          list += '</div>';
          filesPanel.innerHTML = list;
          filesPanel.dataset.rendered = '1';
        }
        filesPanel.style.display = 'block';
      } else {
        filesPanel.style.display = 'none';
      }
    }
  }

window.updateFeedbackStatus = async function(id) {
    var sel = document.getElementById('statusSelect');
    if (!sel) return;
    var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/status', {
      method: 'PUT',
      body: JSON.stringify({status: sel.value})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast('状态已更新', 'success');
  }

window.addNote = async function(feedbackId) {
    var content = document.getElementById('noteContent').value.trim();
    var fileInput = document.getElementById('noteFiles');
    var hasFile = fileInput && fileInput.files && fileInput.files.length > 0;
    if (!content && !hasFile) { showToast('内容或附件至少填写一项', 'error'); return; }
    var isPublic = document.getElementById('notePublic').checked;

    var fd = new FormData();
    fd.append('content', content);
    fd.append('is_public', isPublic ? 'true' : 'false');
    if (hasFile) {
      for (var i = 0; i < fileInput.files.length; i++) {
        if (fileInput.files[i].size > 20*1024*1024) { showToast('文件 ' + fileInput.files[i].name + ' 超过 20MB', 'error'); return; }
        fd.append('file', fileInput.files[i]);
      }
    }
    var d = await apiForm('/api/v1/admin/feedbacks/' + feedbackId + '/notes', fd);
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast('备注已添加', 'success');
    document.getElementById('noteContent').value = '';
    document.getElementById('notePublic').checked = false;
    if (fileInput) fileInput.value = '';
    loadNotes(feedbackId);
  }

window.deleteNote = async function(noteId, feedbackId) {
    if (!confirm('确认删除此备注？')) return;
    var d = await apiJSON('/api/v1/admin/feedbacks/' + feedbackId + '/notes/' + noteId, {method: 'DELETE'});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast('备注已删除', 'success');
    loadNotes(feedbackId);
  }

window.updateAssignee = async function(id) {
    var val = document.getElementById('assigneeInput').value.trim();
    var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/assignee', {
      method: 'PUT',
      body: JSON.stringify({assignee: val})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast('指派已更新', 'success');
  }

window.deleteFeedback = async function(id) {
    if (!confirm('确认删除此反馈记录？')) return;
    var d = await apiJSON('/api/v1/admin/feedbacks/' + id, {method: 'DELETE'});
    if (!d) return;
    showToast(d.message || '已删除', 'success');
    showList(); loadFeedbacks(); loadStats();
  }

window.saveRoadmap = async function(id) {
    var pub = document.getElementById('roadmapPublic');
    var statusSel = document.getElementById('roadmapStatus');
    if (!statusSel) return;
    var body = {public: !!(pub && pub.checked), status: statusSel.value};
    var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/roadmap', {method:'PUT', body:JSON.stringify(body)});
    if (!d) return;
    if (d.error) { showToast(d.error,'error'); return; }
    showToast('路线图已更新','success');
    showDetail(id);
  }

window.loadProjects = async function() {
    var resp = await api('/api/v1/admin/projects');
    if (!resp) return;
    var d = await resp.json();
    allProjects = d.projects || [];
    renderKbProjects();
    var tbody = document.getElementById('projectTable');
    if (allProjects.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">暂无项目</td></tr>';
      return;
    }
    tbody.innerHTML = allProjects.map(function(p){
      var checked = p.is_active ? ' checked' : '';
      var toggleHtml = '<label class="toggle-switch" ><input type="checkbox"' + checked + ' data-change="toggleProjectActive" data-args="' + p.id + ',this.checked"><span class="toggle-slider"></span></label>';
      var fbUrl = window.location.origin + '/fb/' + encodeURIComponent(p.slug);
      var fbCount = (typeof p.feedback_count === 'number') ? p.feedback_count : 0;
      var archivedBadge = p.is_archived ? ' <span style="background:var(--priority-low-bg);color:var(--priority-low-fg);padding:1px 6px;border-radius:3px;font-size:.7rem;margin-left:4px">已归档</span>' : '';
      return '<tr>' +
        '<td><strong>'+esc(p.name)+'</strong>'+archivedBadge + (p.description ? '<br><span style="font-size:.75rem;color:var(--muted)">'+esc(p.description)+'</span>' : '') + '</td>' +
        '<td><span class="tag">'+esc(p.slug)+'</span></td>' +
        '<td>'+toggleHtml+'</td>' +
        '<td>'+fbCount+'</td>' +
        '<td style="font-size:.75rem"><code>'+esc(fbUrl)+'</code> <a href="#" data-click="copyUrl" data-args="\''+fbUrl+'\'">复制</a></td>' +
        '<td><a href="#" data-click="viewProjectDetail" data-args="'+p.id+'">详情</a> ' +
        '<a href="#" data-click="editProject" data-args="'+p.id+'">编辑</a> ' +
        '<a href="#" data-click="cloneProject" data-args="'+p.id+'">克隆</a> ' +
        '<a href="#" data-click="toggleArchive" data-args="'+p.id+','+!p.is_archived+'">'+(p.is_archived?'取消归档':'归档')+'</a> ' +
        '<a href="#" data-click="deleteProject" data-args="'+p.id+'" style="color:var(--priority-urgent-fg)">删除</a></td>' +
        '</tr>';
    }).join('');
  }

window.toggleProjectActive = async function(id, active) {
    var project = allProjects.find(function(p){ return p.id === id; });
    if (!project) return;
    var d = await apiJSON('/api/v1/admin/projects/' + id, {
      method: 'PUT',
      body: JSON.stringify({name:project.name,slug:project.slug,description:project.description||'',is_active:active,is_archived:project.is_archived||false,form_schema:project.form_schema||'[]'})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); loadProjects(); return; }
    showToast(active ? '已启用' : '已停用', 'success');
    loadStats();
    project.is_active = active;
  }

window.copyUrl = function(url) {
    navigator.clipboard.writeText(url).then(function(){ showToast('链接已复制', 'success'); });
  }

window.cloneProject = function(id) {
    var project = allProjects.find(function(p){ return p.id === id; });
    if (!project) return;
    cloneTargetId = id;
    document.getElementById('cloneName').value = project.name + ' (副本)';
    document.getElementById('cloneSlug').value = project.slug + '-copy';
    document.getElementById('cloneError').textContent = '';
    document.getElementById('cloneModal').classList.add('active');
    setTimeout(function(){ document.getElementById('cloneName').focus(); }, 50);
  }

window.editProject = function(id) {
    var project = allProjects.find(function(p){ return p.id === id; });
    if (!project) return;
    document.getElementById('projectModalTitle').textContent = '编辑项目';
    document.getElementById('pfId').value = id;
    document.getElementById('pfName').value = project.name;
    document.getElementById('pfSlug').value = project.slug;
    document.getElementById('pfSlug').readOnly = true;
    document.getElementById('pfDesc').value = project.description || '';
    document.getElementById('pfActive').checked = project.is_active;
    document.getElementById('pfArchived').checked = project.is_archived || false;
    // Project announcement
    var pa = {level:'info', content_type:'text', content:''};
    if (project.announcement) {
      try { var parsed = JSON.parse(project.announcement); if (parsed && typeof parsed === 'object') pa = parsed; } catch(e){}
    }
    document.getElementById('pfAnnounceLevel').value = pa.level || 'info';
    document.getElementById('pfAnnounceType').value = pa.content_type || 'text';
    document.getElementById('pfAnnouncement').value = pa.content || '';
    document.getElementById('projectModal').classList.add('active');
  }

window.deleteProject = function(id) {
    var project = (typeof allProjects !== 'undefined') ? allProjects.find(function(p){ return p.id === id; }) : null;
    if (!project) { showToast('项目不存在', 'error'); return; }
    deleteTargetId = id;
    deleteTargetName = project.name;
    var nameEl = document.getElementById('deleteProjectName');
    if (nameEl) nameEl.textContent = project.name;
    var input = document.getElementById('deleteProjectInput');
    if (input) input.value = '';
    var btn = document.getElementById('confirmDeleteBtn');
    if (btn) {
      btn.disabled = true;
      if (!deleteInputBound) {
        input.addEventListener('input', function() {
          btn.disabled = input.value.trim() !== deleteTargetName;
        });
        deleteInputBound = true;
      }
    }
    var m = document.getElementById('deleteProjectModal');
    if (m) m.classList.add('active');
  }

window.previewForm = function() {
    if (typeof currentFormSchema === 'undefined' || !currentFormSchema) { showToast('当前没有可预览的表单', 'error'); return; }
    var schema = currentFormSchema;
    var hasNotify = schema.some(function(f){ return f.sys==='notify'; });
    var html = '';
    schema.forEach(function(f){ html += renderPreviewField(f); });
    if (!hasNotify) {
      html += '<div class="field"><label class="checkbox-label" style="display:flex;align-items:flex-start;gap:8px"><input type="checkbox" disabled style="margin-top:2px;width:auto"><span>📬 接收反馈处理通知<br><small style="color:var(--muted);font-weight:normal">勾选后可接收反馈状态更新邮件通知。</small></span></label></div>';
    }
    var container = document.getElementById('formPreviewBody');
    if (!container) return;
    container.innerHTML = html;
    var m = document.getElementById('formPreviewModal');
    if (m) m.classList.add('active');
  }

window.loadProjectStats = async function() {
    var resp = await api('/api/v1/admin/project-stats');
    if (!resp) return;
    var d = await resp.json();
    var stats = d.stats || [];
    var chart = document.getElementById('projectStatsChart');
    if (stats.length === 0) { chart.innerHTML = '<p class="empty">暂无数据</p>'; return; }
    var total = stats.reduce(function(s,x){ return s + x.count; }, 0);
    if (total === 0) { chart.innerHTML = '<p class="empty">暂无数据</p>'; return; }
    var size=160,cx=80,cy=80,r=60,strokeW=24;
    var circumference = 2 * Math.PI * r;
    var offset = 0;
    var svgPaths = '';
    var legendHtml = '';
    stats.forEach(function(s, i){
      var pct = s.count / total;
      var dashLen = pct * circumference;
      var color = chartColors[i % chartColors.length];
      var label = s.project_name || s.project_id;
      svgPaths += '<circle cx="'+cx+'" cy="'+cy+'" r="'+r+'" fill="none" stroke="'+color+'" stroke-width="'+strokeW+'" stroke-dasharray="'+dashLen+' '+(circumference - dashLen)+'" stroke-dashoffset="'+(-offset)+'" transform="rotate(-90 '+cx+' '+cy+')" />';
      offset += dashLen;
      legendHtml += '<div class="donut-legend-item"><div class="donut-legend-dot" style="background:'+color+'"></div><span class="donut-legend-label">'+esc(label)+'</span><span class="donut-legend-count">'+s.count+' ('+Math.round(pct*100)+'%)</span></div>';
    });
    var svg = '<svg class="donut-svg" viewBox="0 0 '+size+' '+size+'">' + svgPaths +
      '<text x="'+cx+'" y="'+cy+'" text-anchor="middle" dominant-baseline="central" font-size="18" font-weight="700" fill="#333">'+total+'</text>' +
      '<text x="'+cx+'" y="'+(cy+16)+'" text-anchor="middle" font-size="10" fill="#888">总计</text></svg>';
    chart.innerHTML = '<div class="donut-container">' + svg + '<div class="donut-legend">' + legendHtml + '</div></div>';
  }

window.viewProjectDetail = function(id) {
    currentProjectId = id;
    window.location.hash = 'project/' + id;
    loadProjectDetail(id);
  }

window.moveField = function(index, direction) {
    var newIndex = index + direction;
    if (newIndex < 0 || newIndex >= currentFormSchema.length) return;
    var tmp = currentFormSchema[index];
    currentFormSchema[index] = currentFormSchema[newIndex];
    currentFormSchema[newIndex] = tmp;
    renderFormSchemaList();
  }

window.removeField = function(index) {
    var f = currentFormSchema[index];
    if (f && f.sys === 'title') {
      showToast('“标题”为必填系统字段，不能删除（后端提交依赖它）', 'error');
      return;
    }
    currentFormSchema.splice(index, 1);
    renderFormSchemaList();
  }

window.addField = function() {
    editingFieldIndex = -1;
    document.getElementById('fieldEditorTitle').textContent = '添加字段';
    document.getElementById('feType').value = 'text';
    document.getElementById('feName').value = '';
    document.getElementById('feLabel').value = '';
    document.getElementById('fePlaceholder').value = '';
    document.getElementById('feRequired').checked = false;
    document.getElementById('feDefault').value = '';
    document.getElementById('feMin').value = '';
    document.getElementById('feMax').value = '';
    document.getElementById('feStep').value = '';
    document.getElementById('feMinLength').value = '';
    document.getElementById('feMaxLength').value = '';
    document.getElementById('fePattern').value = '';
    document.getElementById('feRows').value = '';
    document.getElementById('feAccept').value = '';
    document.getElementById('feMultiple').checked = false;
    document.getElementById('feIcon').value = 'star';
    document.getElementById('feLabelOn').value = '';
    document.getElementById('feLabelOff').value = '';
    document.getElementById('feCollapsible').checked = false;
    document.getElementById('feContent').value = '';
    document.getElementById('feWidth').value = 'full';
    document.getElementById('feHelpText').value = '';
    document.getElementById('feSys').value = '';
    document.getElementById('feOptionsList').innerHTML = '';
    onFieldTypeChange();
    onSysChange();
    document.getElementById('fieldEditorModal').classList.add('active');
  }

window.editField = function(index) {
    editingFieldIndex = index;
    var field = currentFormSchema[index];
    document.getElementById('fieldEditorTitle').textContent = '编辑字段';
    document.getElementById('feType').value = field.type;
    document.getElementById('feName').value = field.name;
    document.getElementById('feLabel').value = field.label;
    document.getElementById('fePlaceholder').value = field.placeholder || '';
    document.getElementById('feRequired').checked = !!field.required;
    document.getElementById('feDefault').value = field.default || '';
    document.getElementById('feMin').value = field.min || '';
    document.getElementById('feMax').value = field.max || '';
    document.getElementById('feStep').value = field.step || '';
    document.getElementById('feMinLength').value = field.min_length || '';
    document.getElementById('feMaxLength').value = field.max_length || '';
    document.getElementById('fePattern').value = field.pattern || '';
    document.getElementById('feRows').value = field.rows || '';
    document.getElementById('feAccept').value = field.accept || '';
    document.getElementById('feMultiple').checked = !!field.multiple;
    document.getElementById('feIcon').value = field.icon || 'star';
    document.getElementById('feLabelOn').value = field.label_on || '';
    document.getElementById('feLabelOff').value = field.label_off || '';
    document.getElementById('feCollapsible').checked = !!field.collapsible;
    document.getElementById('feContent').value = field.content || '';
    document.getElementById('feWidth').value = field.width || 'full';
    document.getElementById('feHelpText').value = field.help_text || '';
    document.getElementById('feSys').value = field.sys || '';
    onFieldTypeChange();
    onSysChange();
    var optList = document.getElementById('feOptionsList');
    optList.innerHTML = '';
    if (field.options) field.options.forEach(function(opt){ addOptionRow(opt); });
    document.getElementById('fieldEditorModal').classList.add('active');
  }

window.saveFormSchema = async function() {
    var project = allProjects.find(function(p){ return p.id === currentProjectId; });
    if (!project) return;
    var d = await apiJSON('/api/v1/admin/projects/' + currentProjectId, {
      method: 'PUT',
      body: JSON.stringify({name:project.name,slug:project.slug,description:project.description||'',is_active:project.is_active,form_schema:JSON.stringify(currentFormSchema)})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast('表单配置已保存', 'success');
    project.form_schema = JSON.stringify(currentFormSchema);
  }

window.editAdmin = function(id, username, role, isActive) {
    document.getElementById('adminModalTitle').textContent = '编辑成员';
    document.getElementById('adminEditId').value = id;
    document.getElementById('adminUsername').value = username;
    document.getElementById('adminUsername').disabled = true;
    document.getElementById('adminPassword').value = '';
    document.getElementById('adminPwdHint').textContent = '';
    document.getElementById('adminRole').value = role;
    document.getElementById('adminActive').checked = isActive;
    document.getElementById('adminGrantsSection').style.display = 'none';
    document.getElementById('adminModal').classList.add('active');
  }

window.deleteAdmin = async function(id, username) {
    if (!confirm('确定删除成员 "' + username + '"？此操作不可撤销。')) return;
    var d = await apiJSON('/api/v1/admin/admins/' + id, {method: 'DELETE'});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
    loadAdmins();
  }

window.updatePriority = async function(id, priority) {
    var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/priority', {
      method: 'PUT',
      body: JSON.stringify({priority: priority})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
    showDetail(id);
    loadFeedbacks();
  }

window.markDuplicate = async function(id) {
    var modal = document.getElementById('dupSimilarModal');
    var listEl = document.getElementById('dupSimilarList');
    var msgEl = document.getElementById('dupSimilarMsg');
    var manualEl = document.getElementById('dupSimilarManual');
    if (!modal || !listEl || !msgEl) {
      // Markup missing — fall back to the legacy manual prompt path.
      fallbackMarkDuplicate(id);
      return;
    }
    window.dupSimilarData = { id: id };
    if (manualEl) manualEl.value = '';
    listEl.innerHTML = '<div style="color:var(--muted);font-size:.85rem">正在加载候选相似反馈…</div>';
    msgEl.textContent = '正在加载与 #' + id + ' 内容相同的反馈，请选择主反馈。';
    modal.classList.add('active');

    var d;
    try {
      d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/similar');
    } catch (e) {
      listEl.innerHTML = '';
      msgEl.textContent = '加载候选列表失败，可手动输入主反馈 ID。';
      return;
    }
    if (!d) return;
    if (d.error) { msgEl.textContent = d.error; listEl.innerHTML = ''; return; }

    var cands = Array.isArray(d.candidates) ? d.candidates : [];
    if (!cands.length) {
      listEl.innerHTML = '<div style="color:var(--muted);font-size:.85rem">未找到内容相同的相似反馈，可手动输入主反馈 ID。</div>';
      return;
    }
    listEl.innerHTML = cands.map(function(c, i) {
      var summary = c.summary
        ? '<div style="color:var(--muted);font-size:.82rem;margin:4px 0 0 26px">' + esc(c.summary) + '</div>'
        : '';
      return '<label style="display:block;padding:10px;border:1px solid var(--border);border-radius:6px;margin-bottom:8px;cursor:pointer;background:var(--panel)">' +
        '<input type="radio" name="dupTarget" value="' + c.id + '"' + (i === 0 ? ' checked' : '') + '>' +
        '<span style="margin-left:8px"><strong>#' + c.id + '</strong> ' + esc(c.title) + '</span>' +
        summary +
        '</label>';
    }).join('');
  }

window.confirmMarkDuplicate = async function() {
    var data = window.dupSimilarData;
    if (!data) return;
    var id = data.id;

    var manualEl = document.getElementById('dupSimilarManual');
    var manualVal = manualEl && manualEl.value ? parseInt(manualEl.value.trim(), 10) : 0;
    var targetId = 0;
    if (manualVal && manualVal > 0) {
      targetId = manualVal;
    } else {
      var radio = document.querySelector('input[name="dupTarget"]:checked');
      targetId = radio ? parseInt(radio.value, 10) : 0;
    }
    if (!targetId || targetId === id) { showToast('请选择或输入有效的主反馈 ID', 'error'); return; }

    closeDupSimilar();
    var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/duplicate', {
      method: 'POST',
      body: JSON.stringify({duplicate_of: targetId})
    });
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message || '已标记为重复', 'success');
    showDetail(id);
    loadFeedbacks();
  }

window.closeDupSimilar = function() {
    var modal = document.getElementById('dupSimilarModal');
    if (modal) modal.classList.remove('active');
  }

window.unmarkDuplicate = async function(id) {
    var d = await apiJSON('/api/v1/admin/feedbacks/' + id + '/duplicate', {method: 'DELETE'});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
    showDetail(id);
    loadFeedbacks();
  }

window.logout = async function() {
    await apiJSON('/api/v1/admin/logout', {method: 'POST'});
    window.location.href = '/admin/login';
  }

window.toggleToken = async function(id, active) {
    var d = await apiJSON('/api/v1/admin/api-tokens/' + id, {method:'PUT', body:JSON.stringify({is_active:active})});
    if (!d) return;
    showToast(d.message, 'success');
    loadTokens();
  }

window.deleteToken = async function(id) {
    if (!confirm('确定删除此 Token？使用此 Token 的外部系统将无法提交反馈。')) return;
    var d = await apiJSON('/api/v1/admin/api-tokens/' + id, {method:'DELETE'});
    if (!d) return;
    showToast(d.message, 'success');
    loadTokens();
  }

window.rotateToken = async function(id) {
    if (!confirm('重新生成后旧 Token 立即失效，使用它的外部系统需更新密钥。确定？')) return;
    var d = await apiJSON('/api/v1/admin/api-tokens/' + id + '/rotate', {method:'POST'});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    document.getElementById('tokenRotateValue').textContent = d.token;
    document.getElementById('tokenRotateModal').classList.add('active');
    loadTokens();
  }

window.openTokenStats = async function(id) {
    var resp = await api('/api/v1/admin/api-tokens/' + id + '/stats');
    if (!resp) return;
    var d = await resp.json();
    var buckets = d.buckets || [];
    var max = 0;
    buckets.forEach(function(b) { if (b.count > max) max = b.count; });
    var barsEl = document.getElementById('tokenStatsBars');
    if (!barsEl) return;
    var html = '';
    buckets.forEach(function(b) {
      var okH = max > 0 && b.ok > 0 ? Math.max(2, Math.round((b.ok / max) * 100)) : 0;
      var failH = max > 0 && b.fail > 0 ? Math.max(2, Math.round((b.fail / max) * 100)) : 0;
      var dt = new Date(b.hour * 1000);
      var label = dt.getHours() + ':00';
      var title = dt.toLocaleString() + '  —  成功:' + b.ok + ' 受限:' + b.fail;
      html += '<div title="' + esc(title) + '" style="flex:1;min-width:13px;display:flex;flex-direction:column;justify-content:flex-end;height:100%">'
        + '<div style="height:' + okH + '%;background:var(--success-fg);border-radius:2px 2px 0 0"></div>'
        + '<div style="height:' + failH + '%;background:#e5484d;border-radius:0 0 2px 2px"></div>'
        + '<div style="font-size:.58rem;text-align:center;color:var(--muted);margin-top:2px">' + label + '</div>'
        + '</div>';
    });
    barsEl.innerHTML = html;
    var totalOK = 0, totalFail = 0;
    buckets.forEach(function(b) { totalOK += b.ok; totalFail += b.fail; });
    var sum = document.getElementById('tokenStatsSummary');
    if (sum) sum.textContent = '近 24 小时共 ' + (totalOK + totalFail) + ' 次调用（成功 ' + totalOK + '，受限 ' + totalFail + '）';
    document.getElementById('tokenStatsModal').classList.add('active');
  }

window.editWebhook = async function(id) {
    var resp = await api('/api/v1/admin/webhooks');
    if (!resp) return;
    var d = await resp.json();
    var subs = d.subscriptions || [];
    var sub = subs.find(function(s){ return s.id === id; });
    if (!sub) { showToast('订阅不存在', 'error'); return; }
    document.getElementById('webhookModalTitle').textContent = '编辑 Webhook 订阅';
    document.getElementById('webhookEditId').value = id;
    document.getElementById('whProject').value = sub.project_slug || '';
    document.getElementById('whUrl').value = sub.url || '';
    document.getElementById('whSecret').value = '';
    document.getElementById('whEvents').value = sub.events || '*';
    document.getElementById('whActive').checked = sub.is_active;
    document.getElementById('webhookModal').classList.add('active');
  }

window.deleteWebhook = async function(id) {
    if (!confirm('确定删除此 Webhook 订阅？')) return;
    var d = await apiJSON('/api/v1/admin/webhooks/' + id, {method:'DELETE'});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    showToast(d.message, 'success');
    loadWebhooks();
  }

window.testWebhook = async function(id) {
    showToast('正在发送测试事件...', 'info');
    var d = await apiJSON('/api/v1/admin/webhooks/' + id + '/test', {method:'POST'});
    if (!d) return;
    var box = document.getElementById('whTestResult');
    if (!box) return;
    var statusColor = (d.status && d.status >= 200 && d.status < 300) ? 'var(--success-fg)' : 'var(--priority-urgent-fg)';
    var bodyPreview = d.body ? (d.body.length > 500 ? d.body.substring(0,500) + '…' : d.body) : '';
    box.innerHTML = '<div class="settings-card" style="margin-top:0"><h3>测试事件结果</h3>' +
      '<p style="font-size:.85rem">HTTP 状态码：<strong style="color:'+statusColor+'">'+(d.status||0)+'</strong></p>' +
      (d.error ? '<p style="color:var(--priority-urgent-fg);font-size:.82rem">错误：'+esc(d.error)+'</p>' : '') +
      (bodyPreview ? '<pre style="background:var(--bg-secondary);padding:8px;border-radius:4px;max-height:180px;overflow:auto;font-size:.75rem;white-space:pre-wrap">'+esc(bodyPreview)+'</pre>' : '') +
      '</div>';
  }

window.showWebhookDeliveries = async function(id) {
    var card = document.getElementById('whDeliveriesCard');
    var box = document.getElementById('whDeliveries');
    if (!card || !box) return;
    card.style.display = '';
    box.innerHTML = '<p style="font-size:.8rem;color:var(--hint)">加载中...</p>';
    var resp = await api('/api/v1/admin/webhooks/' + id + '/deliveries?limit=20');
    if (!resp) return;
    var d = await resp.json();
    var list = d.deliveries || [];
    var st = d.stats;
    var statHtml = '';
    if (st) {
      var rateStr = (st.success_rate != null) ? st.success_rate.toFixed(1) : '0.0';
      statHtml = '<div style="font-size:.82rem;margin-bottom:10px">成功率：<strong style="color:var(--success-fg)">' + rateStr + '%</strong> ' +
        '（成功 ' + (st.success || 0) + ' / 失败 ' + (st.failed || 0) + ' / 共 ' + (st.total || 0) + '）</div>';
    }
    if (list.length === 0) { box.innerHTML = statHtml + '<p style="font-size:.8rem;color:var(--hint)">暂无投递记录</p>'; return; }
    var html = '<table style="width:100%"><thead><tr><th>时间</th><th>事件</th><th>状态</th><th>错误</th></tr></thead><tbody>';
    list.forEach(function(x){
      var dt = x.created_at ? x.created_at.replace('T',' ').substring(0,19) : '-';
      var st = x.response_status || 0;
      var stColor = (st >= 200 && st < 300) ? 'var(--success-fg)' : 'var(--priority-urgent-fg)';
      var err = x.error ? (x.error.length > 80 ? x.error.substring(0,80)+'…' : x.error) : '';
      html += '<tr><td style="font-size:.78rem;color:var(--muted)">'+dt+'</td>' +
        '<td style="font-size:.78rem">'+esc(x.event)+'</td>' +
        '<td style="font-size:.78rem;color:'+stColor+'">'+st+'</td>' +
        '<td style="font-size:.75rem;color:var(--priority-urgent-fg);max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'+esc(err)+'</td></tr>';
    });
    html += '</tbody></table>';
    box.innerHTML = statHtml + html;
  }

window.toggleArchive = async function(id, archived) {
    var d = await apiJSON('/api/v1/admin/projects/' + id + '/archive', {method:'POST', body:JSON.stringify({archived:archived})});
    if (!d) return;
    showToast(d.message, 'success');
    loadProjects();
  }

window.editMemberGrants = async function(adminId) {
    // Load current grants and project categories in parallel
    var resp = await api('/api/v1/admin/admins/' + adminId + '/grants');
    if (!resp) return;
    var d = await resp.json();
    var currentGrants = d.grants || [];

    // Build a map: project_slug → {role, categories:Set}
    var grantMap = {};
    currentGrants.forEach(function(g){
      if (!grantMap[g.project_slug]) grantMap[g.project_slug] = {role:'viewer', cats:[]};
      grantMap[g.project_slug].role = g.role;
      if (g.category_key !== '*') grantMap[g.project_slug].cats.push(g.category_key);
    });

    // Load all projects (reuse allProjects global if available, else fetch)
    if (!allProjects || allProjects.length === 0) {
      var pResp = await api('/api/v1/admin/projects');
      if (pResp) {
        var pd = await pResp.json();
        allProjects = pd.projects || [];
      }
    }

    // Cache for project categories
    var projCategories = {};

    var html = '<div style="max-height:400px;overflow-y:auto">';
    if (allProjects.length === 0) {
      html += '<p style="color:var(--muted);font-size:.85rem">暂无可用项目</p>';
    }
    allProjects.forEach(function(p){
      var g = grantMap[p.slug] || null;
      var checked = g ? ' checked' : '';
      var role = g ? g.role : 'editor';
      var roleOptions = ['viewer','editor','manager'].map(function(r){
        var label = r === 'viewer' ? '只读' : r === 'editor' ? '编辑' : '经理';
        var sel = r === role ? ' selected' : '';
        return '<option value="'+r+'"'+sel+'>'+label+'</option>';
      }).join('');

      html += '<div class="grant-project-row" style="margin-bottom:8px;padding:8px;border:1px solid var(--border-soft);border-radius:4px">';
      html += '<label style="display:flex;align-items:center;gap:6px;font-size:.85rem;font-weight:500">';
      html += '<input type="checkbox" class="grant-proj-cb" data-slug="'+esc(p.slug)+'"'+checked+' style="width:auto"> ';
      html += esc(p.name) + ' <span style="color:var(--hint);font-size:.75rem">('+esc(p.slug)+')</span></label>';
      html += '<div class="grant-project-detail" style="margin-top:6px;padding-left:22px;'+(g?'':'display:none')+'">';
      html += '<div style="display:flex;align-items:center;gap:8px;margin-bottom:4px"><span style="font-size:.8rem;color:var(--tag-fg)">角色:</span>';
      html += '<select class="grant-role-sel" data-slug="'+esc(p.slug)+'" style="font-size:.8rem;padding:2px 4px">'+roleOptions+'</select></div>';
      html += '<div class="grant-cats-area" data-slug="'+esc(p.slug)+'" style="font-size:.8rem;margin-top:4px"><span style="color:var(--muted)">加载分类中...</span></div>';
      html += '</div></div>';
    });
    html += '</div>';
    html += '<p style="font-size:.75rem;color:var(--muted);margin-top:8px">不勾选 = 无访问权限。勾选后设置角色和可选分类限制（不选分类 = 所有分类）。</p>';

    var overlay = document.createElement('div');
    overlay.className = 'modal-overlay active';
    overlay.innerHTML = '<div class="modal" style="max-width:520px"><h3>授权管理</h3>' +
      '<div id="grantList">' + html + '</div>' +
      '<div class="modal-actions"><button class="btn-sm" data-remove-closest=".modal-overlay">取消</button>' +
      '<button class="btn-save" data-click="saveMemberGrants" data-args="' + adminId + ',this">保存</button></div></div>';
    document.body.appendChild(overlay);

    // Load categories for a project and render checkboxes
    async function loadProjectCategories(slug) {
      if (projCategories.hasOwnProperty(slug)) return projCategories[slug];
      var cResp = await api('/api/v1/admin/projects/' + encodeURIComponent(slug) + '/categories');
      var cats = [];
      if (cResp) {
        var cd = await cResp.json();
        cats = (cd.categories || []).filter(function(c){ return c.is_active; });
      }
      projCategories[slug] = cats;
      return cats;
    }

    function renderCategoryCheckboxes(slug, cats, selectedCats) {
      var area = overlay.querySelector('.grant-cats-area[data-slug="'+CSS.escape(slug)+'"]');
      if (!area) return;
      if (cats.length === 0) {
        area.innerHTML = '<span style="color:var(--muted)">该项目暂无分类，授权覆盖所有反馈</span>';
        return;
      }
      var h = '<div style="color:var(--tag-fg);font-size:.75rem;margin-bottom:2px">分类限制（不选 = 全部）:</div>';
      cats.forEach(function(cat){
        var checked = selectedCats.indexOf(cat.key) >= 0 ? ' checked' : '';
        h += '<label style="display:inline-flex;align-items:center;gap:3px;margin-right:8px;font-size:.78rem;cursor:pointer">';
        h += '<input type="checkbox" class="grant-cat-cb" data-slug="'+esc(slug)+'" data-cat="'+esc(cat.key)+'"'+checked+' style="width:auto"> ';
        h += esc(cat.name) + '</label>';
      });
      area.innerHTML = h;
    }

    // Toggle detail visibility and load categories on checkbox change
    overlay.querySelectorAll('.grant-proj-cb').forEach(function(cb){
      cb.addEventListener('change', function(){
        var detail = this.closest('.grant-project-row').querySelector('.grant-project-detail');
        detail.style.display = this.checked ? '' : 'none';
        if (this.checked) {
          var slug = this.getAttribute('data-slug');
          var g = grantMap[slug] || {cats:[]};
          loadProjectCategories(slug).then(function(cats){
            renderCategoryCheckboxes(slug, cats, g.cats);
          });
        }
      });
      // Pre-load categories for already-checked projects
      if (cb.checked) {
        var slug = cb.getAttribute('data-slug');
        var g = grantMap[slug] || {cats:[]};
        loadProjectCategories(slug).then(function(cats){
          renderCategoryCheckboxes(slug, cats, g.cats);
        });
      }
    });
  }

window.saveMemberGrants = async function(adminId, btn) {
    var overlay = btn.closest('.modal-overlay');
    var grants = [];
    overlay.querySelectorAll('.grant-proj-cb').forEach(function(cb){
      if (!cb.checked) return;
      var slug = cb.getAttribute('data-slug');
      var roleSel = overlay.querySelector('.grant-role-sel[data-slug="'+CSS.escape(slug)+'"]');
      var role = roleSel ? roleSel.value : 'editor';
      var checkedCats = [];
      overlay.querySelectorAll('.grant-cat-cb[data-slug="'+CSS.escape(slug)+'"]').forEach(function(catCb){
        if (catCb.checked) checkedCats.push(catCb.getAttribute('data-cat'));
      });
      if (checkedCats.length === 0) {
        grants.push({project_slug: slug, category_key: '*', role: role});
      } else {
        checkedCats.forEach(function(c){
          grants.push({project_slug: slug, category_key: c, role: role});
        });
      }
    });

    var d = await apiJSON('/api/v1/admin/admins/' + adminId + '/grants', {
      method: 'PUT',
      body: JSON.stringify({grants: grants})
    });
    if (!d) return;
    showToast(d.message || '授权已更新', 'success');
    overlay.remove();
  }

window.openAdminLb = function(index, noteId) {
    adminLbFbId = currentDetailId;
    adminLbNoteId = noteId || 0;
    adminLbItems = (adminLbNoteId === 0) ? (adminImgFiles[adminLbFbId] || []) : (adminNoteImgFiles[adminLbNoteId] || []);
    adminLbIndex = (typeof index === 'number') ? index : 0;
    var lb = document.getElementById('adminLightbox');
    if (!lb) return;
    lb.classList.add('active');
    updateAdminLb();
  }

window.closeAdminLb = function() {
    var lb = document.getElementById('adminLightbox');
    if (lb) lb.classList.remove('active');
  }

window.adminLbPrev = function() {
    if (adminLbIndex > 0) { adminLbIndex--; updateAdminLb(); }
  }

window.adminLbNext = function() {
    if (adminLbIndex < adminLbItems.length - 1) { adminLbIndex++; updateAdminLb(); }
  }

(function(){
  "use strict";
  // 主题（亮 / 暗 / 跟随系统）由 /shared/theme.js 统一管理

  window.toggleKbdHint = function(){
    var b = document.getElementById('kbdHintBody');
    if (b) b.style.display = (b.style.display === 'none' ? 'block' : 'none');
  };

  // 列表键盘快捷键（仅管理端仪表盘列表生效）
  var selectedRowId = null;
  function getRows(){
    return Array.prototype.slice.call(document.querySelectorAll('#feedbackTable tr[data-id]'));
  }
  function highlightRow(id){
    var rows = getRows();
    for (var i = 0; i < rows.length; i++){
      if (rows[i].getAttribute('data-id') === String(id)) rows[i].classList.add('kb-selected');
      else rows[i].classList.remove('kb-selected');
    }
    selectedRowId = id;
  }
  function moveSelection(dir){
    var rows = getRows();
    if (!rows.length) return;
    var idx = -1;
    if (selectedRowId !== null){
      for (var i = 0; i < rows.length; i++){
        if (rows[i].getAttribute('data-id') === String(selectedRowId)){ idx = i; break; }
      }
    }
    var next = (idx < 0) ? 0 : idx + dir;
    if (next < 0) next = 0;
    if (next >= rows.length) next = rows.length - 1;
    var newId = rows[next].getAttribute('data-id');
    highlightRow(newId);
    rows[next].scrollIntoView({ block: 'nearest' });
  }
  function isTyping(el){
    if (!el) return false;
    var tag = el.tagName ? el.tagName.toLowerCase() : '';
    return tag === 'input' || tag === 'textarea' || tag === 'select' || el.isContentEditable;
  }
  document.addEventListener('keydown', function(e){
    if (isTyping(document.activeElement)) return;
    if (e.key === 'Escape'){
      var overlay = document.querySelector('.modal-overlay.active');
      if (overlay){ overlay.classList.remove('active'); e.preventDefault(); }
      return;
    }
    var dash = document.getElementById('tab-dashboard');
    if (!dash || dash.style.display === 'none') return;
    if (e.key === '/'){
      var search = document.getElementById('keywordSearch');
      if (search){ search.focus(); e.preventDefault(); }
      return;
    }
    if (e.key === 'j'){ moveSelection(1); e.preventDefault(); return; }
    if (e.key === 'k'){ moveSelection(-1); e.preventDefault(); return; }
    if (e.key === 'e'){
      if (selectedRowId !== null && typeof window.showDetail === 'function'){
        window.showDetail(Number(selectedRowId));
        e.preventDefault();
      }
      return;
    }
  });

  // 列表重新渲染后清除高亮态
  document.addEventListener('DOMContentLoaded', function(){
    var tbody = document.getElementById('feedbackTable');
    if (tbody && window.MutationObserver){
      new MutationObserver(function(){ selectedRowId = null; }).observe(tbody, { childList: true });
    }
  });
})();

(function bindAdminLb() {
    var lb = document.getElementById('adminLightbox');
    if (!lb) return;
    lb.addEventListener('click', function(e){ if (e.target === lb) window.closeAdminLb(); });
    document.addEventListener('keydown', function(e){
      if (e.key === 'Escape' && lb.classList.contains('active')) window.closeAdminLb();
    });
  })();

// Opens a feedback's detail panel. Used by the dashboard/pending/roadmap
// "打开" buttons; resolved as window.openPendingDetail by the event delegate.
window.openPendingDetail = function(id){
  if (currentTab !== 'dashboard') switchTab('dashboard');
  showDetailInner(id);
};

// Initial load — deferred so all admin-*.js are parsed first.
async function __adminInit(){
window.dupSimilarData = null;
window.addEventListener('hashchange', handleHash);
if (keywordSearchEl) keywordSearchEl.addEventListener('keyup', window.debounceSearch);
loadCurrentUser();
restoreFilters();
populateCategoryFilter();
await fetchCSRFToken();      // ensure CSRF is ready before any write call
handleHash();                // resolve/apply the initial tab from the URL
// Guarantee the dashboard (default tab) loads its data on entry, via the
// exact same code path as clicking the tab — fixes the "empty on entry" symptom.
if (!window.location.hash || window.location.hash === '#dashboard') {
  switchTab('dashboard');
}
}
if (document.readyState === 'loading'){
  document.addEventListener('DOMContentLoaded', __adminInit);
} else {
  __adminInit();
}
