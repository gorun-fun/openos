<script setup lang="ts">
// 仓库 — 双登录模式的仓库治理（v0.0.32 重构，三区）：
// ① 账号连接：直接登录 GitLab（账密 / PAT）与 GitHub（PAT）平台，凭据只在
//    服务端（GitLab 连接兼流水线数据源，「设为数据源」切换）；
// ② 账号仓库：浏览账号下全部仓库，一键接管（挂连接动态解析推送凭据，GitHub
//    仓自动配 issue 同步）、远程新建项目；
// ③ 已接管仓库：项目级单仓注册（模式一，手动填 clone 地址 + 仓级凭据），含
//    处理模式 / GitHub 同步 / 发版默认值。
import { ref } from 'vue'
import { OsButton, OsInput, OsSelect, OsModal, OsForm, OsField, OsTable, OsEmpty, OsToggle, toast, osConfirm } from '../../../ui'
import type { TableColumn } from '../../../ui'
import { grf } from '../core'

// ---- ① 账号连接 ----

const conns = ref<any[]>([])
const connDlg = ref(false)
const connBusy = ref(false)
const connForm = ref<any>({})

async function loadConns() {
  try {
    const c = await grf.get<any>('/api/steward/connections')
    conns.value = Array.isArray(c) ? c : [] // 空态整包回落防御
  } catch { /* 引擎未启用等场景静默，仓库区仍可用 */ }
}

function openConn() {
  connForm.value = { platform: 'gitlab', base_url: 'https://git.gorun.fun', auth: 'password', pat: '', user: '', password: '' }
  connDlg.value = true
}

async function saveConn() {
  const f = connForm.value
  const gitlab = f.platform === 'gitlab'
  if (gitlab && f.auth === 'password' && (!f.user || !f.password)) { toast('用户名与密码必填', { type: 'error' }); return }
  if (!gitlab || f.auth === 'pat') {
    if (!f.pat) { toast('Personal Access Token 必填', { type: 'error' }); return }
  }
  connBusy.value = true
  try {
    const body: Record<string, string> = { platform: f.platform, base_url: gitlab ? f.base_url.trim() : '' }
    if (gitlab && f.auth === 'password') { body.user = f.user.trim(); body.password = f.password }
    else body.pat = f.pat.trim()
    const v = await grf.post<any>('/api/steward/connections', body)
    toast(`已连接：${v?.login || 'ok'}`, { type: 'success' })
    connDlg.value = false
    await loadConns()
    if (v?.id) { selConn.value = v.id; await browse() }
  } catch (e: any) { toast(e.message || '连接失败', { type: 'error' }) } finally { connBusy.value = false }
}

async function testConn(c: any) {
  try {
    const v = await grf.post<any>(`/api/steward/connections/${c.id}/test`)
    toast(`连接正常：${v?.login || c.login}`, { type: 'success' })
    await loadConns()
  } catch (e: any) { toast(e.message || '验证失败', { type: 'error' }) }
}

async function activateConn(c: any) {
  try {
    await grf.post<any>(`/api/steward/connections/${c.id}/activate`)
    toast('已切换为流水线数据源（版本页下一轮即生效）', { type: 'success' })
    await loadConns()
  } catch (e: any) { toast(e.message || '切换失败', { type: 'error' }) }
}

async function delConn(c: any) {
  if (!await osConfirm({ title: '断开账号连接', body: `断开 ${c.login} @ ${c.base_url}？挂接的仓库回落仓级凭据（若无凭据，修复推送会失败）。`, danger: true })) return
  try {
    await grf.del<any>(`/api/steward/connections/${c.id}`)
    toast('已断开', { type: 'success' })
    if (selConn.value === c.id) { selConn.value = ''; browseRows.value = []; browsePage.value = 1 }
    await Promise.all([loadConns(), load()])
  } catch (e: any) { toast(e.message || '断开失败', { type: 'error' }) }
}

const platIcon = (p: string) => (p === 'gitlab' ? '🦊 GitLab' : '🐙 GitHub')

// ---- ② 账号仓库浏览 ----

const selConn = ref('')
const browseQ = ref('')
const browseRows = ref<any[]>([])
const browsePage = ref(1)
const browseNext = ref(0)
const browseBusy = ref(false)
const projDlg = ref(false)
const projBusy = ref(false)
const projForm = ref<any>({})

async function browse() {
  if (!selConn.value) { browseRows.value = []; return }
  browseBusy.value = true
  try {
    const qs = new URLSearchParams({ page: String(browsePage.value) })
    if (browseQ.value.trim()) qs.set('q', browseQ.value.trim())
    const r = await grf.get<any>(`/api/steward/connections/${selConn.value}/repos?${qs}`)
    browseRows.value = r?.repos || []
    browseNext.value = r?.next_page || 0
  } catch (e: any) { toast(e.message || '仓库浏览失败', { type: 'error' }); browseRows.value = [] } finally { browseBusy.value = false }
}

function search() { browsePage.value = 1; browse() }
function turnPage(d: number) {
  const next = browsePage.value + d
  if (next < 1) return
  browsePage.value = next
  browse()
}

async function takeOver(row: any) {
  const conn = conns.value.find((c: any) => c.id === selConn.value)
  try {
    await grf.post<any>('/api/steward/repos', {
      name: row.path, clone_url: row.clone_url, branch: row.default_branch || '', mode: 'manual',
      connection_id: selConn.value, platform: conn?.platform || '',
      github_owner: conn?.platform === 'github' ? row.path.split('/')[0] : '',
      github_repo: conn?.platform === 'github' ? row.name : '',
      auto_release: false,
    })
    toast(`已接管：${row.path}`, { type: 'success' })
    await Promise.all([browse(), load()])
  } catch (e: any) { toast(e.message || '接管失败', { type: 'error' }) }
}

function openProj() {
  projForm.value = { name: '', description: '', visibility: 'private', auto_init: true }
  projDlg.value = true
}

async function saveProj() {
  const f = projForm.value
  if (!f.name.trim()) { toast('项目名必填', { type: 'error' }); return }
  projBusy.value = true
  try {
    const r = await grf.post<any>(`/api/steward/connections/${selConn.value}/projects`, {
      name: f.name.trim(), description: f.description.trim(), visibility: f.visibility, auto_init: !!f.auto_init,
    })
    toast(`已创建：${r?.path || f.name}（在列表中接管即可纳入修复管理）`, { type: 'success' })
    projDlg.value = false
    browsePage.value = 1
    browseQ.value = ''
    await browse()
  } catch (e: any) { toast(e.message || '创建失败', { type: 'error' }) } finally { projBusy.value = false }
}

const browseCols: TableColumn[] = [
  { key: 'path', label: '仓库' },
  { key: 'visibility', label: '可见性', width: '80px' },
  { key: 'default_branch', label: '默认分支', width: '100px' },
  { key: 'last_activity', label: '最近活动', width: '150px', format: (v: string) => v ? String(v).replace('T', ' ').slice(0, 16) : '—' },
  { key: 'state', label: '状态', width: '80px' },
]
const visZh = (v: string) => ({ private: '私有', internal: '内部', public: '公开' } as Record<string, string>)[v] || v

// ---- ③ 已接管仓库（项目级，模式一）----

const repos = ref<any[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    repos.value = (await grf.get<any>('/api/steward/repos')) || []
  } catch (e: any) { toast(e.message || '仓库列表加载失败', { type: 'error' }) } finally { loading.value = false }
}
load()
// 连接加载完成后默认选中第一个并浏览账号仓库
loadConns().then(() => {
  if (conns.value.length && !selConn.value) {
    selConn.value = conns.value[0].id
    browse()
  }
})

const cols: TableColumn[] = [
  { key: 'name', label: '仓库' },
  { key: 'clone_url', label: 'Clone 地址' },
  { key: 'branch', label: '分支', width: '90px' },
  { key: 'mode', label: '模式', width: '90px' },
  { key: 'src', label: '凭据来源', width: '110px' },
  { key: 'issue_sync', label: 'Issue 同步', width: '170px' },
  { key: 'auto_release', label: '默认发版', width: '80px' },
  { key: 'last_issue_sync', label: '上次同步', width: '150px', format: (v: string) => v ? String(v).replace('T', ' ').slice(0, 19) : '—' },
]

// ---- 手动新增 / 编辑（模式一表单）----

const editDlg = ref(false)
const busy = ref(false)
const form = ref<any>({})
const editId = ref('')

function openAdd() {
  editId.value = ''
  form.value = { name: '', clone_url: '', branch: '', mode: 'manual', git_user: '', git_token: '', github_owner: '', github_repo: '', issue_labels: '', auto_release: false }
  editDlg.value = true
}
function openEdit(r: any) {
  editId.value = r.id
  form.value = {
    name: r.name, clone_url: r.clone_url, branch: r.branch || r.def_branch || '', mode: r.mode,
    git_user: r.git_user || '', git_token: '', // 掩码：空 = 不改
    github_owner: r.github_owner || '', github_repo: r.github_repo || '',
    issue_labels: (r.issue_labels || []).join(', '), auto_release: !!r.auto_release,
  }
  editDlg.value = true
}
async function save() {
  const f = form.value
  if (!f.name || !f.clone_url) { toast('名称与 Clone 地址必填', { type: 'error' }); return }
  busy.value = true
  try {
    const body: Record<string, unknown> = {
      name: f.name, clone_url: f.clone_url, branch: f.branch.trim(), mode: f.mode,
      git_user: f.git_user.trim(), git_token: f.git_token.trim(),
      github_owner: f.github_owner.trim(), github_repo: f.github_repo.trim(),
      issue_labels: f.issue_labels.split(/[,，]/).map((s: string) => s.trim()).filter(Boolean),
      auto_release: !!f.auto_release,
    }
    if (editId.value) await grf.put<any>(`/api/steward/repos/${editId.value}`, body)
    else await grf.post<any>('/api/steward/repos', body)
    toast('已保存', { type: 'success' })
    editDlg.value = false
    await Promise.all([load(), browse()])
  } catch (e: any) { toast(e.message || '保存失败', { type: 'error' }) } finally { busy.value = false }
}

async function del(r: any) {
  if (!await osConfirm({ title: '删除仓库', body: `删除「${r.name}」的接管配置与本地工作区（远端仓库不受影响）？`, danger: true })) return
  try {
    await grf.del<any>(`/api/steward/repos/${r.id}`)
    toast('已删除', { type: 'success' })
    await Promise.all([load(), browse()])
  } catch (e: any) { toast(e.message || '删除失败', { type: 'error' }) }
}

async function syncIssues(r: any) {
  try {
    const n = await grf.post<any>(`/api/steward/repos/${r.id}/sync-issues`)
    toast(n?.added ? `同步完成：新增 ${n.added} 个问题` : '同步完成：无新问题', { type: 'success' })
    await load()
  } catch (e: any) { toast(e.message || '同步失败', { type: 'error' }) }
}

const connName = (id: string) => {
  const c = conns.value.find((x: any) => x.id === id)
  return c ? `${c.platform === 'gitlab' ? 'GitLab' : 'GitHub'} · ${c.login}` : ''
}

// GitLab 仓的项目路径展示（与引擎同规则：clone 地址去 host 去 .git）
function glPath(u: string) {
  try { return new URL(u).pathname.replace(/\.git$/, '').replace(/^\//, '') } catch { return '' }
}
</script>

<template>
  <div class="tabpage">
    <!-- ① 账号连接 -->
    <section class="zone">
      <div class="zonebar">
        <h4>账号连接</h4>
        <span class="dim">登录 GitLab / GitHub 平台，管理账号下的全部仓库；GitLab 连接兼作「版本」页流水线数据源</span>
        <span class="grow" />
        <OsButton variant="primary" size="sm" @click="openConn">连接账号</OsButton>
      </div>
      <div v-if="conns.length" class="cards">
        <div v-for="c in conns" :key="c.id" class="card">
          <div class="chead">
            <span class="plat">{{ platIcon(c.platform) }}</span>
            <b class="mono">{{ c.login }}</b>
            <span v-if="c.active" class="badge info">流水线数据源</span>
            <span class="grow" />
            <span class="dim host" :title="c.base_url">{{ c.base_url }}</span>
          </div>
          <div class="cops">
            <OsButton size="sm" @click="testConn(c)">验证</OsButton>
            <OsButton v-if="c.platform === 'gitlab' && !c.active" size="sm" @click="activateConn(c)">设为数据源</OsButton>
            <OsButton size="sm" variant="danger" @click="delConn(c)">断开</OsButton>
          </div>
        </div>
      </div>
      <OsEmpty v-else text="尚未连接平台账号——「连接账号」用 GitHub/GitLab 凭据直接登录（账密或访问令牌，凭据只存服务端）" />
    </section>

    <!-- ② 账号仓库 -->
    <section v-if="conns.length" class="zone">
      <div class="zonebar">
        <h4>账号仓库</h4>
        <OsSelect v-model="selConn" :options="conns.map((c) => ({ value: c.id, label: `${c.platform === 'gitlab' ? '🦊' : '🐙'} ${c.login}` }))" style="width: 170px" @update:model-value="search" />
        <OsInput v-model="browseQ" placeholder="搜索仓库" style="width: 180px" @keyup.enter="search" />
        <OsButton size="sm" @click="search">搜索</OsButton>
        <span class="grow" />
        <OsButton size="sm" @click="openProj">新建项目</OsButton>
      </div>
      <OsTable :columns="browseCols" :rows="browseRows">
        <template #cell-path="{ row }">
          <a :href="row.web_url" target="_blank" class="srclink" @click.stop>{{ row.path }}</a>
          <em v-if="row.archived" class="hint"> 已归档</em>
        </template>
        <template #cell-visibility="{ row }">{{ visZh(row.visibility) }}</template>
        <template #cell-default_branch="{ row }"><span class="mono dim2">{{ row.default_branch || '—' }}</span></template>
        <template #cell-state="{ row }">
          <span v-if="row.taken_over" class="badge ok">已接管</span>
          <span v-else class="dim2">未接管</span>
        </template>
        <template #ops="{ row }">
          <OsButton v-if="!row.taken_over && !row.archived" size="sm" variant="primary" :disabled="browseBusy" @click.stop="takeOver(row)">接管</OsButton>
          <span v-else class="dim2">—</span>
        </template>
      </OsTable>
      <div v-if="browseRows.length || browsePage > 1" class="pager">
        <OsButton size="sm" :disabled="browsePage <= 1 || browseBusy" @click="turnPage(-1)">上一页</OsButton>
        <span class="dim">第 {{ browsePage }} 页</span>
        <OsButton size="sm" :disabled="!browseNext || browseBusy" @click="turnPage(1)">下一页</OsButton>
      </div>
      <OsEmpty v-if="!browseRows.length && !browseBusy" text="该账号下没有匹配的仓库" />
    </section>

    <!-- ③ 已接管仓库 -->
    <section class="zone">
      <div class="zonebar">
        <h4>已接管仓库</h4>
        <span class="dim">{{ repos.length }} 个 · 修复工作区与推送凭据的管理面</span>
        <span class="grow" />
        <OsButton size="sm" @click="openAdd">手动添加</OsButton>
        <OsButton size="sm" @click="load">刷新</OsButton>
      </div>
      <OsTable :columns="cols" :rows="repos">
        <template #cell-name="{ row }">
          <span :title="row.name">{{ row.name }}</span>
        </template>
        <template #cell-clone_url="{ row }">
          <span class="mono" :title="row.clone_url">{{ row.clone_url }}</span>
          <em v-if="row.git_token_set" class="hint"> 🔑</em>
        </template>
        <template #cell-branch="{ row }">{{ row.branch || row.def_branch || '默认' }}</template>
        <template #cell-mode="{ row }">
          <span class="badge" :class="row.mode === 'auto' ? 'info' : 'warn'">{{ row.mode === 'auto' ? '自动' : '手动' }}</span>
        </template>
        <template #cell-src="{ row }">
          <span v-if="row.connection_id" class="badge info" :title="`连接 ${connName(row.connection_id)} 动态解析推送凭据`">{{ connName(row.connection_id) || '平台连接' }}</span>
          <span v-else-if="row.git_token_set" class="dim2">仓级令牌</span>
          <span v-else class="dim2">无凭据</span>
        </template>
        <template #cell-issue_sync="{ row }">
          <template v-if="row.github_owner">
            <span class="mono">🐙 {{ row.github_owner }}/{{ row.github_repo }}</span>
            <em v-if="row.issue_labels?.length" class="hint"> [{{ row.issue_labels.join(', ') }}]</em>
          </template>
          <template v-else-if="row.platform === 'gitlab'">
            <span class="mono">🦊 {{ glPath(row.clone_url) || '（clone 地址无法解析项目路径）' }}</span>
            <em v-if="row.issue_labels?.length" class="hint"> [{{ row.issue_labels.join(', ') }}]</em>
          </template>
          <span v-else class="dim2">—</span>
        </template>
        <template #cell-auto_release="{ row }">{{ row.auto_release ? '✓' : '—' }}</template>
        <template #ops="{ row }">
          <OsButton size="sm" @click.stop="openEdit(row)">编辑</OsButton>
          <OsButton v-if="row.github_owner || row.platform === 'gitlab'" size="sm" @click.stop="syncIssues(row)">同步 issue</OsButton>
          <OsButton size="sm" variant="danger" @click.stop="del(row)">删除</OsButton>
        </template>
      </OsTable>
      <OsEmpty v-if="!repos.length && !loading" text="暂无接管仓库——从上方账号仓库一键「接管」，或「手动添加」填入 clone 地址（仅支持 https）" />

      <OsModal v-if="editDlg" :title="editId ? '编辑仓库' : '手动添加仓库'" okText="保 存" cancelText="取 消" @ok="save" @cancel="editDlg = false">
        <OsForm :model="form">
          <OsField label="名称" required><OsInput v-model="form.name" placeholder="如 grf-gateway（上报问题时按此名定位）" /></OsField>
          <OsField label="Clone" required><OsInput v-model="form.clone_url" placeholder="https://git.gorun.fun/share/xxx.git" /></OsField>
          <OsField label="分支"><OsInput v-model="form.branch" placeholder="空 = 远端默认分支" /></OsField>
          <OsField label="模式">
            <OsSelect v-model="form.mode" :options="[{ value: 'manual', label: '手动（问题需人工确认）' }, { value: 'auto', label: '自动（问题直接修复）' }]" />
          </OsField>
          <OsField label="凭据">
            <div class="duo">
              <OsInput v-model="form.git_user" placeholder="用户名（可空）" />
              <OsInput v-model="form.git_token" type="password" :placeholder="editId ? '令牌：留空 = 不改' : '访问令牌（私有仓 pull/push）'" />
            </div>
          </OsField>
          <OsField label="GitHub">
            <div class="duo">
              <OsInput v-model="form.github_owner" placeholder="owner（开源仓同步 issue，可空）" />
              <OsInput v-model="form.github_repo" placeholder="repo" />
            </div>
          </OsField>
          <OsField label="标签"><OsInput v-model="form.issue_labels" placeholder="只同步命中这些 label 的 issue（逗号分隔，空 = 全部）" /></OsField>
          <OsField label="发版">
            <label class="rel"><OsToggle v-model="form.auto_release" /> 确认修复时默认勾选「发版」（tag 末位 +1，触发各仓 CI）</label>
          </OsField>
          <p class="hint">修复流程：clone / pull 最新代码 → Goder 会话修复 → 提交并直推{{ form.branch ? ` ${form.branch}` : '默认' }}分支{{ form.auto_release ? ' → 打 tag 发版' : '' }}。凭据仅命令行注入，不落 .git/config。经账号连接接管的仓库凭据走连接动态解析，此处留空即可。issue 同步：GitLab 仓接管即同步（项目路径取 clone 地址），GitHub 仓填 owner/repo 后同步，手动/自动模式决定新问题是否需确认。</p>
        </OsForm>
      </OsModal>
    </section>

    <!-- 连接账号弹窗 -->
    <OsModal v-if="connDlg" title="连接平台账号" okText="连 接" cancelText="取 消" @ok="saveConn" @cancel="connDlg = false">
      <OsForm :model="connForm">
        <OsField label="平台">
          <OsSelect v-model="connForm.platform" :options="[{ value: 'gitlab', label: '🦊 GitLab（自建或 gitlab.com）' }, { value: 'github', label: '🐙 GitHub' }]" @update:model-value="connForm.auth = connForm.platform === 'gitlab' ? 'password' : 'pat'" />
        </OsField>
        <OsField v-if="connForm.platform === 'gitlab'" label="实例">
          <OsInput v-model="connForm.base_url" placeholder="https://git.gorun.fun" />
        </OsField>
        <template v-if="connForm.platform === 'gitlab'">
          <OsField label="方式">
            <OsSelect v-model="connForm.auth" :options="[{ value: 'password', label: '账号密码' }, { value: 'pat', label: '个人访问令牌（PAT）' }]" />
          </OsField>
          <template v-if="connForm.auth === 'password'">
            <OsField label="账号" required><OsInput v-model="connForm.user" placeholder="用户名" /></OsField>
            <OsField label="密码" required><OsInput v-model="connForm.password" type="password" placeholder="服务端兑换访问令牌，密码不回传" /></OsField>
          </template>
          <OsField v-else label="PAT" required><OsInput v-model="connForm.pat" type="password" placeholder="scope: api（读项目 + 推送）" /></OsField>
        </template>
        <OsField v-else label="PAT" required>
          <OsInput v-model="connForm.pat" type="password" placeholder="ghp_…（scope: repo）" />
          <p class="hint">GitHub 已停用账密授权，仅支持 Personal Access Token。</p>
        </OsField>
        <p class="hint">凭据只存服务端（界面一律掩码），用于浏览仓库、推送修复与流水线取数。</p>
      </OsForm>
    </OsModal>

    <!-- 新建项目弹窗 -->
    <OsModal v-if="projDlg" title="新建项目（远程建仓）" okText="创 建" cancelText="取 消" @ok="saveProj" @cancel="projDlg = false">
      <OsForm :model="projForm">
        <OsField label="名称" required><OsInput v-model="projForm.name" placeholder="如 my-tool（建在账号名下）" /></OsField>
        <OsField label="描述"><OsInput v-model="projForm.description" placeholder="一句话简介（可空）" /></OsField>
        <OsField label="可见性">
          <OsSelect v-model="projForm.visibility" :options="[{ value: 'private', label: '私有' }, { value: 'internal', label: '内部' }, { value: 'public', label: '公开' }]" />
        </OsField>
        <OsField label="初始化">
          <label class="rel"><OsToggle v-model="projForm.auto_init" /> 创建 README（空仓无法被接管与修复）</label>
        </OsField>
      </OsForm>
    </OsModal>
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 16px; }
.zone { display: flex; flex-direction: column; gap: 8px; }
.zonebar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.zonebar h4 { margin: 0; font-size: 13px; font-weight: 600; color: var(--os-text-secondary); }
.grow { flex: 1; }
.cards { display: flex; gap: 10px; flex-wrap: wrap; }
.card { display: flex; flex-direction: column; gap: 8px; padding: 10px 12px; border-radius: 10px; border: 1px solid var(--os-border); background: var(--os-input-bg); min-width: 260px; }
.chead { display: flex; align-items: center; gap: 8px; min-width: 0; }
.chead .host { margin-left: auto; font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 180px; }
.plat { font-size: 12px; color: var(--os-text-secondary); white-space: nowrap; }
.cops { display: flex; gap: 6px; }
.pager { display: flex; align-items: center; gap: 10px; }
.badge { display: inline-block; padding: 1px 8px; border-radius: 8px; font-size: 11px; background: var(--os-active); }
.badge.ok { background: rgba(48, 209, 88, .16); color: var(--grf-ok); }
.badge.warn { background: rgba(255, 159, 10, .16); color: #ffb340; }
.badge.info { background: rgba(10, 132, 255, .18); color: #6db2ff; }
.hint { font-size: 11px; color: var(--os-text-tertiary); font-style: normal; }
.dim { font-size: 11.5px; color: var(--os-text-tertiary); }
.dim2 { color: var(--os-text-tertiary); font-size: 11.5px; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.srclink { color: var(--os-accent, #4da3ff); text-decoration: none; }
.duo { display: flex; gap: 8px; }
.duo > * { flex: 1; min-width: 0; }
.rel { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--os-text-secondary); }
</style>
