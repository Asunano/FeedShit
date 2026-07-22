// admin-team.js
// Auto-split from dashboard.js — classic script (no IIFE) so all top-level
// var/function are globals shared across admin-*.js. Handlers attach to window.*.

window.openInviteModal = async function() {
    document.getElementById('inviteModal').classList.add('active');
    document.getElementById('inviteResultArea').style.display = 'none';
    // Load projects for selection
    var resp = await api('/api/v1/admin/projects');
    if (!resp) return;
    var d = await resp.json();
    var projs = d.projects || [];
    var area = document.getElementById('inviteProjectsArea');
    if (projs.length === 0) {
      area.innerHTML = '<p style="font-size:.8rem;color:var(--muted)">暂无可选项目</p>';
    } else {
      area.innerHTML = projs.map(function(p){ return '<label style="display:flex;align-items:center;gap:6px;padding:4px 0;font-size:.85rem"><input type="checkbox" class="invite-project-cb" value="' + esc(p.slug) + '"> ' + esc(p.name) + '</label>'; }).join('');
    }
    onInviteRoleChange();
  }

window.onInviteRoleChange = function() {
    var isAdmin = document.getElementById('inviteRole').value === 'admin';
    document.getElementById('inviteProjectsField').style.display = isAdmin ? 'none' : '';
  }

window.closeInviteModal = function() { document.getElementById('inviteModal').classList.remove('active'); }

window.createInvitation = async function() {
    var role = document.getElementById('inviteRole').value;
    var cbs = document.querySelectorAll('.invite-project-cb:checked');
    var projectIds = Array.from(cbs).map(function(cb){ return cb.value; });
    var maxUses = parseInt(document.getElementById('inviteMaxUses').value) || 1;
    var expireDays = parseInt(document.getElementById('inviteExpireDays').value) || 0;
    var d = await apiJSON('/api/v1/admin/invitations', {method:'POST', body:JSON.stringify({role:role, project_ids:projectIds, max_uses:maxUses, expires_in_days:expireDays})});
    if (!d) return;
    if (d.error) { showToast(d.error, 'error'); return; }
    document.getElementById('inviteURL').value = d.url;
    document.getElementById('inviteResultArea').style.display = '';
    showToast('邀请链接已生成', 'success');
    loadInvitations();
  }

window.copyInviteURL = function() {
    var input = document.getElementById('inviteURL');
    input.select();
    document.execCommand('copy');
    showToast('已复制到剪贴板', 'success');
  }

window.openAdminModal = function() {
    document.getElementById('adminModalTitle').textContent = '新建成员';
    document.getElementById('adminEditId').value = '';
    document.getElementById('adminUsername').value = '';
    document.getElementById('adminUsername').disabled = false;
    document.getElementById('adminPassword').value = '';
    document.getElementById('adminPwdHint').textContent = '*';
    document.getElementById('adminRole').value = 'editor';
    document.getElementById('adminActive').checked = true;
    document.getElementById('adminGrantsSection').style.display = '';
    renderCreateGrants();
    document.getElementById('adminModal').classList.add('active');
  }

window.closeAdminModal = function() {
    document.getElementById('adminModal').classList.remove('active');
    var ga = document.getElementById('createGrantsArea');
    if (ga) ga.innerHTML = '';
  }

window.saveAdmin = async function() {
    var editId = document.getElementById('adminEditId').value;
    var username = document.getElementById('adminUsername').value.trim();
    var password = document.getElementById('adminPassword').value;
    var role = document.getElementById('adminRole').value;
    var isActive = document.getElementById('adminActive').checked;

    if (editId) {
      // Update
      var body = {role: role, is_active: isActive};
      if (password) body.password = password;
      var d = await apiJSON('/api/v1/admin/admins/' + editId, {method: 'PUT', body: JSON.stringify(body)});
      if (!d) return;
      if (d.error) { showToast(d.error, 'error'); return; }
      showToast(d.message, 'success');
    } else {
      // Create
      if (!username) { showToast('用户名不能为空', 'error'); return; }
      if (!password) { showToast('密码不能为空', 'error'); return; }
      var body = {username: username, password: password, role: role};
      var grants = collectCreateGrants();
      if (grants.length > 0) body.grants = grants;
      var d = await apiJSON('/api/v1/admin/admins', {
        method: 'POST',
        body: JSON.stringify(body)
      });
      if (!d) return;
      if (d.error) { showToast(d.error, 'error'); return; }
      showToast(d.message, 'success');
    }
    closeAdminModal();
    loadAdmins();
  }

