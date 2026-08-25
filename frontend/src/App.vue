<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Loading } from '@element-plus/icons-vue'
import * as ConnectionService from '../bindings/github.com/pp/db-compare/internal/service/connectionservice'
import * as CompareService from '../bindings/github.com/pp/db-compare/internal/service/compareservice'
import * as SyncService from '../bindings/github.com/pp/db-compare/internal/service/syncservice'
import * as ProfileService from '../bindings/github.com/pp/db-compare/internal/service/profileservice'
import { Events } from '@wailsio/runtime'

type Conn = {
  id?: string
  name: string
  host: string
  port: number
  user: string
  password?: string
  database?: string
  params?: string
}

type ChangeItem = {
  id: string
  kind: string
  table: string
  objectType: string
  objectName: string
  summary: string
  detail?: string
  risk: string
  sqlPreview?: string
  selected: boolean
}

type TableRow = {
  table: string
  kind: string
  risk: string
  selected: boolean
  items: ChangeItem[]
}

const connections = ref<Conn[]>([])
const sourceId = ref('')
const targetId = ref('')
const sourceDatabase = ref('')
const targetDatabase = ref('')
const sourceDatabases = ref<string[]>([])
const targetDatabases = ref<string[]>([])
const connForm = ref<Conn>({ name: '', host: '127.0.0.1', port: 3306, user: 'root', password: '', database: '' })
const showConnDialog = ref(false)
const showConnDrawer = ref(false)
const showOptions = ref(false)
const connFormDatabases = ref<string[]>([])
const loadingConnDatabases = ref(false)
const testingConn = ref(false)
const comparing = ref(false)
const planning = ref(false)
const applying = ref(false)
const activeTab = ref('modify')
const sessionId = ref('')
const added = ref<ChangeItem[]>([])
const modified = ref<ChangeItem[]>([])
const dropped = ref<ChangeItem[]>([])
const warnings = ref<string[]>([])
const sourceInfo = ref('')
const targetInfo = ref('')
const planSQL = ref('')
const planStmtCount = ref(0)
const prechecks = ref<any[]>([])
const planBlocked = ref(false)
const selectedTable = ref('')
const tableDiff = ref<any>(null)
const loadingTableDiff = ref(false)
const tableFilter = ref('')
const progress = ref({ total: 0, done: 0, message: '', failed: false })
const profiles = ref<any[]>([])
const loadedProfileId = ref('')
const showSaveProfileDialog = ref(false)
const saveProfileMode = ref<'overwrite' | 'new'>('new')
const saveProfileName = ref('')
const saveProfileOverwriteId = ref('')
const savingProfile = ref(false)
const normalize = ref({
  stripIntDisplayWidth: true,
  unifyUtf8Aliases: true,
  unifyUtf8mb4CiCollations: true,
  ignoreAutoIncrement: true,
  ignoreIndexOrder: true,
  ignoreCommentsOnly: false,
  compareRowFormat: true,
  hideGipk: true,
})
const allowDrop = ref(false)
const allowUnsafe = ref(false)

const tableRows = computed((): TableRow[] => {
  const all = [...added.value, ...modified.value, ...dropped.value]
  const map = new Map<string, TableRow>()
  for (const item of all) {
    if (!item.table || item.objectType !== 'table') continue
    map.set(item.table, {
      table: item.table,
      kind: item.kind,
      risk: item.risk,
      selected: item.selected,
      items: [],
    })
  }
  for (const item of all) {
    if (!item.table) continue
    const row = map.get(item.table)
    if (!row) continue
    row.items.push(item)
    const rankOf = (r: string) => ['info', 'warn', 'error', 'critical'].indexOf(r)
    if (rankOf(item.risk) > rankOf(row.risk)) row.risk = item.risk
  }
  return [...map.values()]
})

const counts = computed(() => ({
  add: tableRows.value.filter((r) => r.kind === 'add').length,
  modify: tableRows.value.filter((r) => r.kind === 'modify').length,
  drop: tableRows.value.filter((r) => r.kind === 'drop').length,
}))

const currentTableRows = computed((): TableRow[] => {
  let rows =
    activeTab.value === 'add'
      ? tableRows.value.filter((r) => r.kind === 'add')
      : activeTab.value === 'drop'
        ? tableRows.value.filter((r) => r.kind === 'drop')
        : tableRows.value.filter((r) => r.kind === 'modify')
  const q = tableFilter.value.trim().toLowerCase()
  if (q) rows = rows.filter((r) => r.table.toLowerCase().includes(q))
  return rows
})

const selectedIds = computed(() =>
  [...added.value, ...modified.value, ...dropped.value].filter((i) => i.selected).map((i) => i.id),
)

const selectedTableCount = computed(
  () => tableRows.value.filter((r) => r.selected).length,
)

const hasCompared = computed(() => !!sessionId.value)
const totalDiffTables = computed(() => counts.value.add + counts.value.modify + counts.value.drop)

const sourceConnLabel = computed(() => {
  const c = findConn(sourceId.value)
  return c ? c.name || connEndpoint(c) : '未选择'
})
const targetConnLabel = computed(() => {
  const c = findConn(targetId.value)
  return c ? c.name || connEndpoint(c) : '未选择'
})

const loadedProfile = computed(() => profiles.value.find((p) => p.id === loadedProfileId.value) || null)

const saveOverwriteOptions = computed(() => {
  const list = [...profiles.value]
  // Prefer currently loaded profile at the top of the select list.
  if (loadedProfileId.value) {
    list.sort((a, b) => {
      if (a.id === loadedProfileId.value) return -1
      if (b.id === loadedProfileId.value) return 1
      return String(a.name).localeCompare(String(b.name))
    })
  }
  return list
})

const connDialogTitle = computed(() => (connForm.value.id ? '编辑连接' : '新建连接'))

const visibleDiffColumns = computed(() =>
  (tableDiff.value?.columns || []).filter((c: any) => c.kind !== 'same'),
)
const visibleDiffIndexes = computed(() =>
  (tableDiff.value?.indexes || []).filter((i: any) => i.kind !== 'same'),
)
const visibleDiffFKs = computed(() =>
  (tableDiff.value?.foreignKeys || []).filter((i: any) => i.kind !== 'same'),
)
const visibleDiffAttrs = computed(() =>
  (tableDiff.value?.attrs || []).filter((a: any) => a.kind !== 'same'),
)
const hasVisibleDiff = computed(
  () =>
    visibleDiffAttrs.value.length +
      visibleDiffColumns.value.length +
      visibleDiffIndexes.value.length +
      visibleDiffFKs.value.length >
    0,
)

function toggleTableRow(row: TableRow, val: boolean) {
  row.selected = val
  row.items.forEach((i) => (i.selected = val))
}

function selectAllTables(flag: boolean) {
  currentTableRows.value.forEach((r) => toggleTableRow(r, flag))
}

function swapEnds() {
  const sid = sourceId.value
  const sdb = sourceDatabase.value
  const sdbs = sourceDatabases.value
  sourceId.value = targetId.value
  sourceDatabase.value = targetDatabase.value
  sourceDatabases.value = targetDatabases.value
  targetId.value = sid
  targetDatabase.value = sdb
  targetDatabases.value = sdbs
}

let unsub: (() => void) | undefined

onMounted(async () => {
  await refreshConnections()
  await refreshProfiles()
  try {
    normalize.value = await CompareService.DefaultNormalize()
  } catch {}
  unsub = Events.On('sync:progress', (ev: any) => {
    const p = ev?.data ?? ev
    progress.value = {
      total: p.total ?? 0,
      done: p.done ?? 0,
      message: p.message ?? '',
      failed: !!p.failed,
    }
  })
})

onUnmounted(() => {
  unsub?.()
})

watch(sourceId, () => {
  void loadDatabases('source')
})

watch(targetId, () => {
  void loadDatabases('target')
})

async function loadDatabases(side: 'source' | 'target') {
  const id = side === 'source' ? sourceId.value : targetId.value
  const conn = findConn(id)
  if (!conn) {
    if (side === 'source') {
      sourceDatabases.value = []
      sourceDatabase.value = ''
    } else {
      targetDatabases.value = []
      targetDatabase.value = ''
    }
    return
  }
  try {
    const dbs = (await ConnectionService.ListDatabases({ id: conn.id } as any)) || []
    if (side === 'source') {
      sourceDatabases.value = dbs
      if (!sourceDatabase.value && conn.database) sourceDatabase.value = conn.database
      else if (sourceDatabase.value && !dbs.includes(sourceDatabase.value)) {
        sourceDatabases.value = [...dbs, sourceDatabase.value]
      }
    } else {
      targetDatabases.value = dbs
      if (!targetDatabase.value && conn.database) targetDatabase.value = conn.database
      else if (targetDatabase.value && !dbs.includes(targetDatabase.value)) {
        targetDatabases.value = [...dbs, targetDatabase.value]
      }
    }
  } catch (e: any) {
    ElMessage.error(formatApiError(e))
  }
}

function connEndpoint(c: Conn): string {
  const base = `${c.user}@${c.host}:${c.port}`
  return c.database ? `${base}/${c.database}` : base
}

function formatApiError(e: unknown): string {
  const msg = String(e ?? 'unknown error')
  if (/failed to fetch|networkerror|load failed/i.test(msg)) {
    return '无法连接本地服务，请确认 dbcompare-server 是否在运行（http://127.0.0.1:13880）'
  }
  return msg
}

function defaultConnForm(): Conn {
  return { name: '', host: '127.0.0.1', port: 3306, user: 'root', password: '', database: '' }
}

function openNewConn() {
  connForm.value = defaultConnForm()
  connFormDatabases.value = []
  showConnDialog.value = true
}

function openEditConn(c: Conn) {
  connForm.value = { ...c, password: '' }
  connFormDatabases.value = []
  showConnDialog.value = true
  void loadConnFormDatabases(true)
}

async function loadConnFormDatabases(silent = false) {
  const c = connForm.value
  if (!c.host?.trim() || !c.user?.trim()) {
    if (!silent) ElMessage.warning('请先填写主机和用户')
    return
  }
  loadingConnDatabases.value = true
  try {
    const dbs = (await ConnectionService.ListDatabases(c as any)) || []
    connFormDatabases.value = dbs
    if (c.database && !dbs.includes(c.database)) {
      connFormDatabases.value = [...dbs, c.database]
    }
  } catch (e: any) {
    if (!silent) ElMessage.error(`加载数据库列表失败：${formatApiError(e)}`)
  } finally {
    loadingConnDatabases.value = false
  }
}

function validateConnForm(c: Conn): boolean {
  if (!c.host?.trim() || !c.user?.trim()) {
    ElMessage.warning('请填写主机和用户')
    return false
  }
  return true
}

async function refreshConnections() {
  connections.value = (await ConnectionService.List()) || []
}

async function refreshProfiles() {
  profiles.value = (await ProfileService.List()) || []
}

async function saveConn() {
  if (!validateConnForm(connForm.value)) return
  const editing = !!connForm.value.id
  try {
    await ConnectionService.Save(connForm.value as any)
    showConnDialog.value = false
    await refreshConnections()
    ElMessage.success(editing ? '连接已更新' : '连接已保存')
  } catch (e: any) {
    ElMessage.error(formatApiError(e))
  }
}

async function testConnPayload(c: Conn, reloadFormDatabases = false) {
  if (!validateConnForm(c)) return
  testingConn.value = true
  try {
    const info = await ConnectionService.Test(c as any)
    ElMessage.success(`连接成功：MySQL ${info.version}`)
    if (reloadFormDatabases) await loadConnFormDatabases(true)
  } catch (e: any) {
    ElMessage.error(formatApiError(e))
  } finally {
    testingConn.value = false
  }
}

async function testConnForm() {
  await testConnPayload(connForm.value, true)
}

async function testConn(c: Conn) {
  await testConnPayload(c)
}

async function removeConn(id: string) {
  await ConnectionService.Delete(id)
  await refreshConnections()
}

function findConn(id: string): Conn | undefined {
  return connections.value.find((c) => c.id === id)
}

async function runCompare() {
  const srcConn = findConn(sourceId.value)
  const dstConn = findConn(targetId.value)
  if (!srcConn || !dstConn) {
    ElMessage.warning('请选择源连接与目标连接')
    return
  }
  if (!sourceDatabase.value || !targetDatabase.value) {
    ElMessage.warning('请选择源数据库与目标数据库')
    return
  }
  const src = { ...srcConn, database: sourceDatabase.value }
  const dst = { ...dstConn, database: targetDatabase.value }
  comparing.value = true
  try {
    const res = await CompareService.Compare({
      source: src,
      target: dst,
      normalize: normalize.value,
    } as any)
    if (!res) throw new Error('empty compare result')
    sessionId.value = res.sessionId
    added.value = (res.added || []).map((i: any) => ({ ...i, selected: false }))
    modified.value = (res.modified || []).map((i: any) => ({ ...i, selected: false }))
    dropped.value = (res.dropped || []).map((i: any) => ({ ...i, selected: false }))
    warnings.value = res.warnings || []
    sourceInfo.value = `${res.sourceInfo.version} / ${res.sourceInfo.collationServer}`
    targetInfo.value = `${res.targetInfo.version} / ${res.targetInfo.collationServer}`
    planSQL.value = ''
    planStmtCount.value = 0
    planBlocked.value = false
    prechecks.value = []
    selectedTable.value = ''
    tableDiff.value = null
    tableFilter.value = ''
    if (counts.value.modify) activeTab.value = 'modify'
    else if (counts.value.add) activeTab.value = 'add'
    else if (counts.value.drop) activeTab.value = 'drop'
    ElMessage.success(
      `对比完成：+${counts.value.add} ~${counts.value.modify} -${counts.value.drop} 张表`,
    )
  } catch (e: any) {
    ElMessage.error(String(e))
  } finally {
    comparing.value = false
  }
}

function selectAll(flag: boolean) {
  selectAllTables(flag)
}

async function selectTable(row: TableRow) {
  if (selectedTable.value === row.table && tableDiff.value) {
    selectedTable.value = ''
    tableDiff.value = null
    return
  }
  selectedTable.value = row.table
  tableDiff.value = null
  if (!sessionId.value) return
  loadingTableDiff.value = true
  try {
    const res = await CompareService.TableDiff(sessionId.value, row.table)
    tableDiff.value = res
  } catch (e: any) {
    ElMessage.error(`加载表对比失败：${formatApiError(e)}`)
  } finally {
    loadingTableDiff.value = false
  }
}

async function runPlan() {
  if (!sessionId.value) return
  planning.value = true
  progress.value = { total: 0, done: 0, message: '', failed: false }
  try {
    const res = await SyncService.Plan({
      sessionId: sessionId.value,
      changeIds: selectedIds.value,
      allowDrop: allowDrop.value,
      allowUnsafe: allowUnsafe.value,
    } as any)
    if (!res) throw new Error('empty plan result')
    prechecks.value = res.prechecks || []
    planBlocked.value = !!res.blocked
    const stmts = res.statements || []
    planStmtCount.value = stmts.length
    const dropsSkipped = (res.prechecks || []).some((p: any) => p.code === 'drops_skipped')
    if (stmts.length) {
      planSQL.value = stmts.map((s: any) => `-- ${s.comment || s.id}\n${s.sql};`).join('\n\n')
    } else if (dropsSkipped) {
      planSQL.value =
        '-- 未生成 DDL：当前勾选的差异仅含删除（如目标多出的列/索引/表）。\n' +
        '-- 请展开「对比选项」勾选「允许删除多余对象」后，再点「生成 SQL」。'
    } else {
      planSQL.value = '-- 未生成 DDL：所选差异在过滤后为空，请检查勾选或同步选项。'
    }
    if (res.blocked) ElMessage.warning('预检存在阻断项，请查看 SQL 区问题或勾选允许不安全同步')
    else if (!stmts.length && dropsSkipped) ElMessage.warning('差异仅为删除操作，请勾选「允许删除多余对象」')
    else if (!stmts.length) ElMessage.warning('未生成 DDL')
    else ElMessage.success(`已生成 ${stmts.length} 条 DDL`)
  } catch (e: any) {
    ElMessage.error(String(e))
  } finally {
    planning.value = false
  }
}

async function runApply() {
  if (!sessionId.value || !planStmtCount.value) {
    ElMessage.warning('请先生成同步 SQL')
    return
  }
  try {
    await ElMessageBox.confirm('将在目标库执行 DDL，且 DDL 不可事务回滚。确认继续？', '执行同步', {
      type: 'warning',
    })
  } catch {
    return
  }
  applying.value = true
  try {
    const res = await SyncService.Apply({ sessionId: sessionId.value, dryRun: false } as any)
    if (!res) throw new Error('empty apply result')
    if (res.verifyClean) ElMessage.success(`同步完成：${res.applied} 条，复查通过`)
    else ElMessage.warning(`同步结束：${res.verifySummary}`)
    if (res.errors?.length) ElMessage.error(res.errors.join('\n'))
  } catch (e: any) {
    ElMessage.error(String(e))
  } finally {
    applying.value = false
  }
}

function openSaveProfileDialog() {
  if (!sourceId.value || !targetId.value || !sourceDatabase.value || !targetDatabase.value) {
    ElMessage.warning('请先选择源与目标连接和数据库')
    return
  }
  const hasProfiles = profiles.value.length > 0
  const loadedStillExists = !!profiles.value.find((p) => p.id === loadedProfileId.value)
  if (hasProfiles && (loadedStillExists || profiles.value.length > 0)) {
    saveProfileMode.value = 'overwrite'
    saveProfileOverwriteId.value = loadedStillExists
      ? loadedProfileId.value
      : profiles.value[0].id
    const target = profiles.value.find((p) => p.id === saveProfileOverwriteId.value)
    saveProfileName.value = target?.name || ''
  } else {
    saveProfileMode.value = 'new'
    saveProfileOverwriteId.value = ''
    saveProfileName.value = `sync-${new Date().toISOString().slice(0, 19).replace('T', ' ')}`
  }
  showSaveProfileDialog.value = true
}

function onSaveProfileModeChange() {
  if (saveProfileMode.value === 'overwrite') {
    const loadedStillExists = !!profiles.value.find((p) => p.id === loadedProfileId.value)
    if (!saveProfileOverwriteId.value || !profiles.value.find((p) => p.id === saveProfileOverwriteId.value)) {
      saveProfileOverwriteId.value = loadedStillExists
        ? loadedProfileId.value
        : profiles.value[0]?.id || ''
    }
    const target = profiles.value.find((p) => p.id === saveProfileOverwriteId.value)
    saveProfileName.value = target?.name || saveProfileName.value
  } else if (!saveProfileName.value.trim()) {
    saveProfileName.value = `sync-${new Date().toISOString().slice(0, 19).replace('T', ' ')}`
  }
}

function onOverwriteTargetChange(id: string) {
  const target = profiles.value.find((p) => p.id === id)
  if (target) saveProfileName.value = target.name || ''
}

async function confirmSaveProfile() {
  const name = saveProfileName.value.trim()
  if (!name) {
    ElMessage.warning('请填写方案名称')
    return
  }
  if (saveProfileMode.value === 'overwrite') {
    if (!saveProfileOverwriteId.value) {
      ElMessage.warning('请选择要覆盖的方案')
      return
    }
  }
  savingProfile.value = true
  try {
    const base =
      saveProfileMode.value === 'overwrite'
        ? profiles.value.find((p) => p.id === saveProfileOverwriteId.value) || {}
        : {}
    const saved = await ProfileService.Save({
      ...base,
      id: saveProfileMode.value === 'overwrite' ? saveProfileOverwriteId.value : undefined,
      name,
      sourceId: sourceId.value,
      sourceDatabase: sourceDatabase.value,
      targetId: targetId.value,
      targetDatabase: targetDatabase.value,
      normalize: normalize.value,
      allowDrop: allowDrop.value,
    } as any)
    await refreshProfiles()
    if (saved?.id) loadedProfileId.value = saved.id
    showSaveProfileDialog.value = false
    ElMessage.success(saveProfileMode.value === 'overwrite' ? '方案已覆盖保存' : '同步方案已保存')
  } catch (e: any) {
    ElMessage.error(formatApiError(e))
  } finally {
    savingProfile.value = false
  }
}

async function saveProfile() {
  openSaveProfileDialog()
}

async function renameProfile(p: any) {
  let name = p.name || ''
  try {
    const { value } = await ElMessageBox.prompt('修改方案名称', '重命名方案', {
      inputValue: name,
      confirmButtonText: '保存',
      cancelButtonText: '取消',
      inputPattern: /\S+/,
      inputErrorMessage: '名称不能为空',
    })
    name = String(value).trim()
  } catch {
    return
  }
  if (name === p.name) return
  await ProfileService.Save({ ...p, name } as any)
  await refreshProfiles()
  ElMessage.success('方案已重命名')
}

async function removeProfile(id: string) {
  try {
    await ElMessageBox.confirm('删除后无法恢复，确认删除此方案？', '删除方案', { type: 'warning' })
  } catch {
    return
  }
  await ProfileService.Delete(id)
  if (loadedProfileId.value === id) loadedProfileId.value = ''
  await refreshProfiles()
  ElMessage.success('方案已删除')
}

function profileEndpointLabel(p: any): string {
  const src = findConn(p.sourceId)
  const dst = findConn(p.targetId)
  const srcName = src ? src.name || connEndpoint(src) : '未知连接'
  const dstName = dst ? dst.name || connEndpoint(dst) : '未知连接'
  return `${srcName} / ${p.sourceDatabase || '—'}  →  ${dstName} / ${p.targetDatabase || '—'}`
}

async function loadProfile(p: any) {
  loadedProfileId.value = p.id || ''
  sourceId.value = p.sourceId
  targetId.value = p.targetId
  sourceDatabase.value = p.sourceDatabase || ''
  targetDatabase.value = p.targetDatabase || ''
  normalize.value = p.normalize || normalize.value
  allowDrop.value = !!p.allowDrop
  await loadDatabases('source')
  await loadDatabases('target')
  showConnDrawer.value = false
  ElMessage.success(`已加载方案：${p.name}`)
}

function riskType(r: string) {
  if (r === 'critical' || r === 'error') return 'danger'
  if (r === 'warn') return 'warning'
  return 'info'
}

function riskLabel(r: string) {
  if (r === 'critical') return '危险'
  if (r === 'error') return '错误'
  if (r === 'warn') return '警告'
  return '信息'
}

function kindMark(kind: string) {
  if (kind === 'add') return '+'
  if (kind === 'drop') return '−'
  return '~'
}

async function copySQL() {
  if (!planSQL.value || !planStmtCount.value) {
    ElMessage.warning('暂无可复制的 SQL')
    return
  }
  try {
    await navigator.clipboard.writeText(planSQL.value)
    ElMessage.success('已复制到剪贴板')
  } catch {
    ElMessage.error('复制失败')
  }
}
</script>

<template>
  <div class="app">
    <header class="topbar">
      <div class="brand-block">
        <img class="brand-mark" src="/brand-mark.png" alt="" width="28" height="28" />
        <div>
          <div class="brand">DB Compare</div>
          <div class="brand-sub">MySQL 结构对比与同步</div>
        </div>
      </div>

      <div class="top-actions">
        <button type="button" class="ghost-btn" @click="showConnDrawer = true">连接与方案</button>
        <button type="button" class="ghost-btn" :class="{ active: showOptions }" @click="showOptions = !showOptions">
          对比选项
        </button>
        <button type="button" class="primary-btn" :disabled="comparing" @click="runCompare">
          {{ comparing ? '对比中…' : '开始对比' }}
        </button>
        <button type="button" class="secondary-btn" :disabled="!hasCompared || planning" @click="runPlan">
          {{ planning ? '生成中…' : '生成 SQL' }}
        </button>
        <button
          type="button"
          class="accent-btn"
          :disabled="!planStmtCount || applying || (planBlocked && !allowUnsafe)"
          @click="runApply"
        >
          {{ applying ? '执行中…' : '执行同步' }}
        </button>
      </div>
    </header>

    <section class="setup">
      <div class="endpoint source">
        <div class="endpoint-label">
          <span class="dot source-dot" />
          源 · 期望结构
        </div>
        <el-select v-model="sourceId" placeholder="选择连接" class="full" filterable>
          <el-option v-for="c in connections" :key="c.id" :label="c.name || connEndpoint(c)" :value="c.id!" />
        </el-select>
        <el-select
          v-model="sourceDatabase"
          placeholder="数据库"
          class="full"
          filterable
          allow-create
          default-first-option
          :disabled="!sourceId"
        >
          <el-option v-for="db in sourceDatabases" :key="db" :label="db" :value="db" />
        </el-select>
        <div class="endpoint-meta" v-if="sourceInfo">{{ sourceInfo }}</div>
        <div class="endpoint-meta muted" v-else>{{ sourceConnLabel }}</div>
      </div>

      <button type="button" class="swap-btn" title="对调源与目标" @click="swapEnds">⇄</button>

      <div class="endpoint target">
        <div class="endpoint-label">
          <span class="dot target-dot" />
          目标 · 将被修改
        </div>
        <el-select v-model="targetId" placeholder="选择连接" class="full" filterable>
          <el-option v-for="c in connections" :key="c.id" :label="c.name || connEndpoint(c)" :value="c.id!" />
        </el-select>
        <el-select
          v-model="targetDatabase"
          placeholder="数据库"
          class="full"
          filterable
          allow-create
          default-first-option
          :disabled="!targetId"
        >
          <el-option v-for="db in targetDatabases" :key="db" :label="db" :value="db" />
        </el-select>
        <div class="endpoint-meta" v-if="targetInfo">{{ targetInfo }}</div>
        <div class="endpoint-meta muted" v-else>{{ targetConnLabel }}</div>
      </div>
    </section>

    <section v-if="showOptions" class="options-bar">
      <label class="opt"><el-checkbox v-model="normalize.stripIntDisplayWidth" />忽略整数显示宽度</label>
      <label class="opt"><el-checkbox v-model="normalize.unifyUtf8Aliases" />统一 utf8 / utf8mb3</label>
      <label class="opt"><el-checkbox v-model="normalize.unifyUtf8mb4CiCollations" />统一 general_ci / 0900_ai_ci</label>
      <label class="opt"><el-checkbox v-model="normalize.ignoreAutoIncrement" />忽略 AUTO_INCREMENT</label>
      <label class="opt danger"><el-checkbox v-model="allowDrop" />允许删除多余对象</label>
      <label class="opt danger"><el-checkbox v-model="allowUnsafe" />允许不安全降级</label>
      <button type="button" class="link-btn" @click="saveProfile">保存当前方案</button>
    </section>

    <div v-if="warnings.length" class="warn-strip">
      <div v-for="(w, i) in warnings" :key="i" class="warn-item" :class="{ block: w.startsWith('[BLOCK]') }">
        {{ w }}
      </div>
    </div>

    <main class="workspace" v-if="hasCompared">
      <aside class="pane tables-pane">
        <div class="pane-head">
          <div>
            <div class="pane-title">差异表</div>
            <div class="pane-sub">已选 {{ selectedTableCount }} / {{ totalDiffTables }} · 默认不勾选，请手动选择</div>
          </div>
          <div class="pane-tools">
            <button type="button" class="mini-btn" @click="selectAll(true)">全选</button>
            <button type="button" class="mini-btn" @click="selectAll(false)">清空</button>
          </div>
        </div>

        <div class="kind-tabs">
          <button type="button" :class="{ on: activeTab === 'add' }" @click="activeTab = 'add'">
            新增 <em>{{ counts.add }}</em>
          </button>
          <button type="button" :class="{ on: activeTab === 'modify' }" @click="activeTab = 'modify'">
            修改 <em>{{ counts.modify }}</em>
          </button>
          <button type="button" :class="{ on: activeTab === 'drop' }" @click="activeTab = 'drop'">
            删除 <em>{{ counts.drop }}</em>
          </button>
        </div>

        <el-input v-model="tableFilter" clearable placeholder="筛选表名" size="small" class="table-filter" />

        <div class="table-list" v-if="currentTableRows.length">
          <div
            v-for="row in currentTableRows"
            :key="row.table"
            class="table-row"
            :class="{ active: row.table === selectedTable, [`kind-${row.kind}`]: true }"
            @click="selectTable(row)"
          >
            <el-checkbox
              :model-value="row.selected"
              @change="(v: boolean) => toggleTableRow(row, v)"
              @click.stop
            />
            <div class="table-row-main">
              <code class="table-name">{{ row.table }}</code>
              <span class="risk-chip" :class="`risk-${row.risk}`">{{ riskLabel(row.risk) }}</span>
            </div>
          </div>
        </div>
        <div v-else class="empty-inline">当前分类无差异表</div>
      </aside>

      <section class="pane diff-pane">
        <div class="pane-head">
          <div>
            <div class="pane-title">
              结构对比
              <template v-if="selectedTable">
                · <code>{{ selectedTable }}</code>
              </template>
            </div>
            <div class="pane-sub">源（期望） vs 目标（当前）</div>
          </div>
          <el-icon v-if="loadingTableDiff" class="is-loading"><Loading /></el-icon>
        </div>

        <div v-if="!selectedTable" class="empty-state">
          <p>从左侧选择一张表，查看字段 / 索引差异</p>
        </div>
        <div v-else-if="loadingTableDiff" class="empty-state"><p>加载中…</p></div>
        <div v-else-if="tableDiff && !hasVisibleDiff" class="empty-state">
          <p>未检测到可展示的结构差异</p>
        </div>
        <div v-else-if="tableDiff" class="diff-scroll">
          <div v-if="visibleDiffAttrs.length" class="diff-section">
            <div class="diff-section-title">表属性</div>
            <table class="diff-table">
              <thead>
                <tr>
                  <th class="col-name">属性</th>
                  <th>源（期望）</th>
                  <th>目标（当前）</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="attr in visibleDiffAttrs" :key="attr.name" :class="`diff-row-${attr.kind}`">
                  <td class="col-name">
                    <span class="diff-badge" :class="`badge-${attr.kind}`">{{ kindMark(attr.kind) }}</span>
                    {{ attr.name }}
                  </td>
                  <td class="mono">{{ attr.source || '—' }}</td>
                  <td class="mono">{{ attr.target || '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div v-if="visibleDiffColumns.length" class="diff-section">
            <div class="diff-section-title">字段</div>
            <table class="diff-table">
              <thead>
                <tr>
                  <th class="col-name">字段名</th>
                  <th>源（期望）</th>
                  <th>目标（当前）</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="col in visibleDiffColumns" :key="col.name" :class="`diff-row-${col.kind}`">
                  <td class="col-name">
                    <span class="diff-badge" :class="`badge-${col.kind}`">{{ kindMark(col.kind) }}</span>
                    {{ col.name }}
                  </td>
                  <td class="mono">{{ col.source || '—' }}</td>
                  <td class="mono">{{ col.target || '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div v-if="visibleDiffIndexes.length" class="diff-section">
            <div class="diff-section-title">索引</div>
            <table class="diff-table">
              <thead>
                <tr>
                  <th class="col-name">索引名</th>
                  <th>源（期望）</th>
                  <th>目标（当前）</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="idx in visibleDiffIndexes" :key="idx.name" :class="`diff-row-${idx.kind}`">
                  <td class="col-name">
                    <span class="diff-badge" :class="`badge-${idx.kind}`">{{ kindMark(idx.kind) }}</span>
                    {{ idx.name }}
                  </td>
                  <td class="mono">{{ idx.source || '—' }}</td>
                  <td class="mono">{{ idx.target || '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div v-if="visibleDiffFKs.length" class="diff-section">
            <div class="diff-section-title">外键</div>
            <table class="diff-table">
              <thead>
                <tr>
                  <th class="col-name">外键名</th>
                  <th>源（期望）</th>
                  <th>目标（当前）</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="fk in visibleDiffFKs" :key="fk.name" :class="`diff-row-${fk.kind}`">
                  <td class="col-name">
                    <span class="diff-badge" :class="`badge-${fk.kind}`">{{ kindMark(fk.kind) }}</span>
                    {{ fk.name }}
                  </td>
                  <td class="mono">{{ fk.source || '—' }}</td>
                  <td class="mono">{{ fk.target || '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <section class="pane sql-pane">
        <div class="pane-head">
          <div>
            <div class="pane-title">SQL 预览</div>
            <div class="pane-sub">
              <template v-if="planStmtCount">{{ planStmtCount }} 条语句</template>
              <template v-else>勾选差异后生成</template>
            </div>
          </div>
          <button type="button" class="mini-btn" :disabled="!planStmtCount" @click="copySQL">复制</button>
        </div>

        <div v-if="prechecks.length" class="prechecks">
          <div v-for="(p, i) in prechecks" :key="i" class="precheck" :class="`level-${p.level}`">
            <strong>{{ p.code }}</strong>
            <span>{{ p.message }}</span>
          </div>
        </div>

        <el-progress
          v-if="applying || progress.total"
          :percentage="progress.total ? Math.round((100 * progress.done) / progress.total) : 0"
          :status="progress.failed ? 'exception' : undefined"
          style="margin: 0 0 10px"
        />

        <pre class="sql">{{ planSQL || '-- 勾选左侧差异表，点击「生成 SQL」' }}</pre>
      </section>
    </main>

    <main v-else class="workspace empty-workspace">
      <div class="hero-empty">
        <div class="hero-title">选择源与目标，开始对比</div>
        <p class="hero-text">
          源库定义期望结构，目标库将被生成的 DDL 修改。可先在右上角管理连接，再点「开始对比」。
        </p>
        <div class="hero-steps">
          <div class="step"><span>1</span>选源连接与数据库</div>
          <div class="step"><span>2</span>选目标连接与数据库</div>
          <div class="step"><span>3</span>对比 → 生成 SQL → 执行</div>
        </div>
      </div>
    </main>

    <el-drawer
      v-model="showConnDrawer"
      size="400px"
      :with-header="false"
      class="mgr-drawer"
      append-to-body
    >
      <div class="mgr">
        <header class="mgr-header">
          <div>
            <div class="mgr-title">连接与方案</div>
            <div class="mgr-sub">管理数据库连接与已存对比方案</div>
          </div>
          <button type="button" class="mini-btn" @click="showConnDrawer = false">关闭</button>
        </header>

        <section class="mgr-section">
          <div class="mgr-section-head">
            <span>连接</span>
            <button type="button" class="ghost-btn sm" @click="openNewConn">新增连接</button>
          </div>
          <div v-if="!connections.length" class="mgr-empty">还没有连接，先新增一个</div>
          <div v-for="c in connections" :key="c.id" class="mgr-card">
            <div class="mgr-card-main">
              <div class="mgr-card-title">{{ c.name || connEndpoint(c) }}</div>
              <div class="mgr-card-sub">{{ connEndpoint(c) }}</div>
            </div>
            <div class="mgr-card-actions">
              <button type="button" class="text-btn" @click="openEditConn(c)">编辑</button>
              <button type="button" class="text-btn" :disabled="testingConn" @click="testConn(c)">测试</button>
              <button type="button" class="text-btn danger" @click="removeConn(c.id!)">删除</button>
            </div>
          </div>
        </section>

        <section class="mgr-section">
          <div class="mgr-section-head">
            <span>已存方案</span>
            <button type="button" class="ghost-btn sm" @click="saveProfile">保存当前</button>
          </div>
          <div v-if="!profiles.length" class="mgr-empty">暂无方案，对比前可先保存源/目标组合</div>
          <div v-for="p in profiles" :key="p.id" class="mgr-card" :class="{ current: p.id === loadedProfileId }">
            <div class="mgr-card-main clickable" @click="loadProfile(p)">
              <div class="mgr-card-title">
                {{ p.name }}
                <span v-if="p.id === loadedProfileId" class="current-badge">当前</span>
              </div>
              <div class="mgr-card-sub">{{ profileEndpointLabel(p) }}</div>
            </div>
            <div class="mgr-card-actions">
              <button type="button" class="text-btn" @click="loadProfile(p)">加载</button>
              <button type="button" class="text-btn" @click="renameProfile(p)">重命名</button>
              <button type="button" class="text-btn danger" @click="removeProfile(p.id)">删除</button>
            </div>
          </div>
        </section>
      </div>
    </el-drawer>

    <el-dialog v-model="showSaveProfileDialog" title="保存方案" width="440px" append-to-body>
      <div class="save-profile-form">
        <div class="save-mode">
          <label class="save-mode-opt" :class="{ on: saveProfileMode === 'overwrite', disabled: !profiles.length }">
            <input
              type="radio"
              value="overwrite"
              v-model="saveProfileMode"
              :disabled="!profiles.length"
              @change="onSaveProfileModeChange"
            />
            覆盖已有方案
          </label>
          <label class="save-mode-opt" :class="{ on: saveProfileMode === 'new' }">
            <input type="radio" value="new" v-model="saveProfileMode" @change="onSaveProfileModeChange" />
            新建方案
          </label>
        </div>

        <div v-if="saveProfileMode === 'overwrite'" class="save-field">
          <div class="save-label">选择方案</div>
          <el-select
            v-model="saveProfileOverwriteId"
            placeholder="选择要覆盖的方案"
            filterable
            style="width: 100%"
            @change="onOverwriteTargetChange"
          >
            <el-option
              v-for="p in saveOverwriteOptions"
              :key="p.id"
              :label="p.id === loadedProfileId ? `${p.name}（当前）` : p.name"
              :value="p.id"
            />
          </el-select>
        </div>

        <div class="save-field">
          <div class="save-label">{{ saveProfileMode === 'overwrite' ? '方案名称（可修改）' : '方案名称' }}</div>
          <el-input v-model="saveProfileName" placeholder="输入方案名称" maxlength="80" />
        </div>

        <div v-if="loadedProfile && saveProfileMode === 'overwrite' && saveProfileOverwriteId === loadedProfile.id" class="save-hint">
          将覆盖当前已加载的方案「{{ loadedProfile.name }}」
        </div>
      </div>
      <template #footer>
        <el-button @click="showSaveProfileDialog = false">取消</el-button>
        <el-button type="primary" :loading="savingProfile" @click="confirmSaveProfile">
          {{ saveProfileMode === 'overwrite' ? '覆盖保存' : '新建保存' }}
        </el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showConnDialog" :title="connDialogTitle" width="480px" append-to-body>
      <el-form label-width="88px">
        <el-form-item label="名称"><el-input v-model="connForm.name" /></el-form-item>
        <el-form-item label="主机"><el-input v-model="connForm.host" /></el-form-item>
        <el-form-item label="端口"><el-input-number v-model="connForm.port" :min="1" :max="65535" /></el-form-item>
        <el-form-item label="用户"><el-input v-model="connForm.user" /></el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="connForm.password"
            type="password"
            show-password
            :placeholder="connForm.id ? '留空则不修改' : ''"
          />
        </el-form-item>
        <el-form-item label="默认库">
          <div class="db-select-row">
            <el-select
              v-model="connForm.database"
              placeholder="选择或输入库名"
              filterable
              allow-create
              clearable
              default-first-option
              style="flex: 1"
            >
              <el-option v-for="db in connFormDatabases" :key="db" :label="db" :value="db" />
            </el-select>
            <el-button :loading="loadingConnDatabases" @click="loadConnFormDatabases()">刷新</el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button :loading="testingConn" @click="testConnForm">测试连接</el-button>
        <el-button @click="showConnDialog = false">取消</el-button>
        <el-button type="primary" @click="saveConn">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.app {
  --bg: #eef2f0;
  --surface: #f7f9f7;
  --panel: #ffffff;
  --ink: #15201c;
  --muted: #5d6b64;
  --line: #d5ddd7;
  --accent: #0f6b5c;
  --accent-soft: #d8efe8;
  --source: #1d6b4f;
  --target: #9a4b1a;
  min-height: 100dvh;
  height: 100dvh;
  display: flex;
  flex-direction: column;
  background:
    radial-gradient(1200px 480px at 10% -10%, rgba(15, 107, 92, 0.08), transparent 60%),
    radial-gradient(900px 420px at 100% 0%, rgba(154, 75, 26, 0.06), transparent 55%),
    var(--bg);
  color: var(--ink);
  font-family: "Sora", "PingFang SC", "Hiragino Sans GB", "Noto Sans SC", sans-serif;
}

.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--line);
  background: rgba(247, 249, 247, 0.92);
  backdrop-filter: blur(10px);
  -webkit-app-region: drag;
}

.brand-block {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 180px;
}

.brand-mark {
  width: 28px;
  height: 28px;
  border-radius: 7px;
  object-fit: cover;
  box-shadow: 0 4px 12px rgba(15, 107, 92, 0.16);
  flex-shrink: 0;
}

.brand {
  font-weight: 700;
  font-size: 15px;
  letter-spacing: -0.02em;
  line-height: 1.1;
}

.brand-sub {
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}

.top-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: flex-end;
  -webkit-app-region: no-drag;
}

.ghost-btn,
.primary-btn,
.secondary-btn,
.accent-btn,
.mini-btn,
.swap-btn,
.link-btn {
  font: inherit;
  cursor: pointer;
  border-radius: 8px;
  transition: transform 0.15s ease, background 0.15s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}

.ghost-btn:active,
.primary-btn:active,
.secondary-btn:active,
.accent-btn:active,
.mini-btn:active,
.swap-btn:active {
  transform: scale(0.98);
}

.ghost-btn {
  border: 1px solid var(--line);
  background: transparent;
  color: var(--ink);
  padding: 8px 12px;
  font-size: 13px;
}
.ghost-btn:hover,
.ghost-btn.active {
  background: var(--accent-soft);
  border-color: #b7d8ce;
}

.primary-btn {
  border: none;
  background: var(--accent);
  color: #fff;
  padding: 8px 14px;
  font-size: 13px;
  font-weight: 600;
  box-shadow: 0 6px 14px rgba(15, 107, 92, 0.2);
}
.primary-btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
  box-shadow: none;
}

.secondary-btn {
  border: 1px solid var(--accent);
  background: #fff;
  color: var(--accent);
  padding: 8px 14px;
  font-size: 13px;
  font-weight: 600;
}
.secondary-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.accent-btn {
  border: none;
  background: #1f2a26;
  color: #f4f7f5;
  padding: 8px 14px;
  font-size: 13px;
  font-weight: 600;
}
.accent-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.mini-btn {
  border: 1px solid var(--line);
  background: #fff;
  color: var(--muted);
  padding: 4px 10px;
  font-size: 12px;
  white-space: nowrap;
  flex-shrink: 0;
  line-height: 1.2;
}
.mini-btn:hover:not(:disabled) {
  color: var(--ink);
  border-color: #b7c4bc;
}
.mini-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.link-btn {
  border: none;
  background: transparent;
  color: var(--accent);
  font-size: 12px;
  padding: 0;
  margin-left: auto;
}

.setup {
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  gap: 12px;
  align-items: stretch;
  padding: 14px 18px 8px;
}

.endpoint {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 14px;
  padding: 12px;
  display: grid;
  gap: 8px;
  box-shadow: 0 10px 24px rgba(21, 32, 28, 0.04);
}

.endpoint.source {
  border-top: 3px solid var(--source);
}
.endpoint.target {
  border-top: 3px solid var(--target);
}

.endpoint-label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.02em;
  color: var(--muted);
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.source-dot {
  background: var(--source);
}
.target-dot {
  background: var(--target);
}

.endpoint-meta {
  font-size: 11px;
  color: var(--ink);
  font-variant-numeric: tabular-nums;
}
.endpoint-meta.muted {
  color: var(--muted);
}

.full {
  width: 100%;
}

.swap-btn {
  align-self: center;
  width: 40px;
  height: 40px;
  border-radius: 999px;
  border: 1px solid var(--line);
  background: #fff;
  color: var(--ink);
  font-size: 16px;
  box-shadow: 0 6px 14px rgba(21, 32, 28, 0.06);
}
.swap-btn:hover {
  background: var(--accent-soft);
}

.options-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 16px;
  align-items: center;
  margin: 0 18px 8px;
  padding: 10px 12px;
  background: rgba(255, 255, 255, 0.72);
  border: 1px solid var(--line);
  border-radius: 12px;
}

.opt {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--muted);
}
.opt.danger {
  color: #8a3b1d;
}

.warn-strip {
  padding: 0 18px 8px;
  display: grid;
  gap: 6px;
}
.warn-item {
  background: #fff7ed;
  border: 1px solid #f0d2b0;
  color: #7a4518;
  border-radius: 10px;
  padding: 8px 12px;
  font-size: 12px;
}
.warn-item.block {
  background: #fef2f2;
  border-color: #f0c2c2;
  color: #8f1f1f;
}

.workspace {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(240px, 280px) minmax(0, 1.2fr) minmax(280px, 0.95fr);
  gap: 12px;
  padding: 8px 18px 18px;
}

.empty-workspace {
  display: grid;
  place-items: center;
  grid-template-columns: 1fr;
}

.hero-empty {
  max-width: 520px;
  text-align: left;
  padding: 28px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 18px;
  box-shadow: 0 18px 40px rgba(21, 32, 28, 0.06);
  animation: rise 0.45s ease both;
}

.hero-title {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.03em;
  margin-bottom: 8px;
}
.hero-text {
  margin: 0 0 18px;
  color: var(--muted);
  font-size: 13px;
  line-height: 1.6;
}
.hero-steps {
  display: grid;
  gap: 8px;
}
.step {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
  color: var(--ink);
}
.step span {
  width: 22px;
  height: 22px;
  border-radius: 7px;
  display: grid;
  place-items: center;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: 12px;
  font-weight: 700;
}

.pane {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 14px;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 10px 24px rgba(21, 32, 28, 0.04);
  animation: rise 0.4s ease both;
}
.diff-pane {
  animation-delay: 0.04s;
}
.sql-pane {
  animation-delay: 0.08s;
}

.pane-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 8px;
  padding: 12px 12px 8px;
  border-bottom: 1px solid #eef2ef;
  flex-wrap: nowrap;
}
.pane-head > div:first-child {
  min-width: 0;
  flex: 1;
}

.pane-title {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: -0.01em;
}
.pane-title code {
  font-family: "IBM Plex Mono", ui-monospace, Menlo, monospace;
  font-weight: 500;
  color: var(--accent);
}
.pane-sub {
  margin-top: 2px;
  font-size: 11px;
  color: var(--muted);
}
.pane-tools {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
  align-items: center;
}

.kind-tabs {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 4px;
  padding: 8px 10px 0;
}
.kind-tabs button {
  border: 1px solid transparent;
  background: transparent;
  border-radius: 8px;
  padding: 7px 4px;
  font: inherit;
  font-size: 12px;
  color: var(--muted);
  cursor: pointer;
}
.kind-tabs button em {
  font-style: normal;
  font-variant-numeric: tabular-nums;
  margin-left: 2px;
}
.kind-tabs button.on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}

.table-filter {
  padding: 8px 10px;
}

.table-list {
  flex: 1;
  overflow: auto;
  padding: 0 6px 8px;
}

.table-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 8px;
  border-radius: 10px;
  cursor: pointer;
  border-left: 3px solid transparent;
  transition: background 0.15s ease;
}
.table-row:hover {
  background: #f3f7f4;
}
.table-row.active {
  background: var(--accent-soft);
}
.table-row.kind-add {
  border-left-color: #2f9e6b;
}
.table-row.kind-modify {
  border-left-color: #c4841d;
}
.table-row.kind-drop {
  border-left-color: #c44b4b;
}

.table-row-main {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.table-name {
  font-family: "IBM Plex Mono", ui-monospace, Menlo, monospace;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.risk-chip {
  flex-shrink: 0;
  font-size: 10px;
  padding: 2px 6px;
  border-radius: 999px;
  background: #eef2f0;
  color: var(--muted);
}
.risk-critical,
.risk-error {
  background: #fde8e8;
  color: #a33;
}
.risk-warn {
  background: #fff1d6;
  color: #8a5a00;
}

.diff-scroll {
  flex: 1;
  overflow: auto;
  padding: 10px 12px 14px;
}

.diff-section {
  margin-bottom: 14px;
}
.diff-section-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  letter-spacing: 0.04em;
  margin-bottom: 6px;
}

.diff-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.diff-table th {
  text-align: left;
  color: var(--muted);
  padding: 6px 8px;
  border-bottom: 1px solid var(--line);
  font-weight: 500;
  position: sticky;
  top: 0;
  background: #fff;
}
.diff-table td {
  padding: 7px 8px;
  border-bottom: 1px solid #eef2ef;
  vertical-align: top;
}
.col-name {
  width: 140px;
  white-space: nowrap;
  color: var(--ink);
  font-weight: 500;
}
.mono {
  font-family: "IBM Plex Mono", ui-monospace, Menlo, monospace;
  color: #44554d;
  white-space: pre-wrap;
  word-break: break-word;
}

.diff-row-add {
  background: #f1faf5;
}
.diff-row-add .mono {
  color: #1f7a4d;
}
.diff-row-drop {
  background: #fdf3f3;
}
.diff-row-drop .mono {
  color: #a33;
}
.diff-row-modify {
  background: #fff8eb;
}
.diff-row-modify .mono {
  color: #8a5a00;
}

.diff-badge {
  display: inline-block;
  width: 14px;
  text-align: center;
  font-weight: 700;
  margin-right: 4px;
  font-size: 11px;
}
.badge-add {
  color: #1f7a4d;
}
.badge-drop {
  color: #a33;
}
.badge-modify {
  color: #8a5a00;
}

.prechecks {
  padding: 0 12px 8px;
  display: grid;
  gap: 6px;
  max-height: 120px;
  overflow: auto;
}
.precheck {
  font-size: 11px;
  line-height: 1.4;
  border-radius: 8px;
  padding: 6px 8px;
  background: #f4f7f5;
  color: var(--muted);
  display: grid;
  gap: 2px;
}
.precheck strong {
  color: var(--ink);
  font-family: "IBM Plex Mono", ui-monospace, Menlo, monospace;
  font-weight: 500;
}
.precheck.level-error,
.precheck.level-critical {
  background: #fde8e8;
  color: #7a2e2e;
}
.precheck.level-warn {
  background: #fff1d6;
  color: #7a5410;
}

.sql {
  flex: 1;
  margin: 0 12px 12px;
  padding: 12px;
  border-radius: 10px;
  background: #15201c;
  color: #d7e4dd;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: "IBM Plex Mono", ui-monospace, Menlo, monospace;
  font-size: 12px;
  line-height: 1.55;
  overflow: auto;
  min-height: 0;
}

.empty-state,
.empty-inline {
  color: var(--muted);
  font-size: 13px;
  padding: 28px 16px;
  text-align: center;
}
.empty-inline {
  padding: 16px;
}

.primary-btn.sm,
.ghost-btn.sm {
  padding: 5px 10px;
  font-size: 12px;
  border-radius: 7px;
}

.text-btn {
  border: none;
  background: transparent;
  color: var(--accent);
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 4px;
}
.text-btn:hover:not(:disabled) {
  background: var(--accent-soft);
}
.text-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.text-btn.danger {
  color: #a33;
}
.text-btn.danger:hover:not(:disabled) {
  background: #fde8e8;
}

.mgr {
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--bg);
  color: var(--ink);
  font-family: "Sora", "PingFang SC", "Hiragino Sans GB", "Noto Sans SC", sans-serif;
}
.mgr-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
  padding: 18px 18px 14px;
  border-bottom: 1px solid var(--line);
  background: rgba(247, 249, 247, 0.95);
}
.mgr-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: -0.02em;
}
.mgr-sub {
  margin-top: 4px;
  font-size: 12px;
  color: var(--muted);
}
.mgr-section {
  padding: 14px 18px;
  border-bottom: 1px solid #e5ebe7;
}
.mgr-section-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  font-size: 13px;
  font-weight: 700;
}
.mgr-empty {
  font-size: 12px;
  color: var(--muted);
  padding: 12px;
  text-align: center;
  background: rgba(255, 255, 255, 0.55);
  border: 1px dashed var(--line);
  border-radius: 10px;
}
.mgr-card {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 12px;
  padding: 12px;
  margin-bottom: 10px;
  box-shadow: 0 6px 16px rgba(21, 32, 28, 0.04);
}
.mgr-card-main.clickable {
  cursor: pointer;
}
.mgr-card-main.clickable:hover .mgr-card-title {
  color: var(--accent);
}
.mgr-card-title {
  font-size: 13px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.mgr-card-sub {
  margin-top: 4px;
  font-size: 11px;
  color: var(--muted);
  font-family: "IBM Plex Mono", ui-monospace, Menlo, monospace;
  line-height: 1.45;
  word-break: break-all;
}
.mgr-card-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid #eef2ef;
}

.mgr-card.current {
  border-color: #b7d8ce;
  box-shadow: 0 0 0 1px rgba(15, 107, 92, 0.18), 0 6px 16px rgba(21, 32, 28, 0.04);
}
.current-badge {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: 10px;
  font-weight: 600;
  vertical-align: middle;
}

.save-profile-form {
  display: grid;
  gap: 14px;
}
.save-mode {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.save-mode-opt {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: 10px;
  font-size: 13px;
  cursor: pointer;
  background: #fff;
}
.save-mode-opt.on {
  border-color: #b7d8ce;
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}
.save-mode-opt.disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.save-field {
  display: grid;
  gap: 6px;
}
.save-label {
  font-size: 12px;
  color: var(--muted);
  font-weight: 500;
}
.save-hint {
  font-size: 12px;
  color: #8a5a00;
  background: #fff8eb;
  border-radius: 8px;
  padding: 8px 10px;
}

.db-select-row {
  display: flex;
  gap: 8px;
  width: 100%;
}

@keyframes rise {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@media (max-width: 1100px) {
  .workspace {
    grid-template-columns: 1fr;
    grid-template-rows: minmax(180px, 28vh) minmax(220px, 36vh) minmax(220px, 1fr);
  }
  .setup {
    grid-template-columns: 1fr;
  }
  .swap-btn {
    justify-self: center;
  }
}
</style>

<style>
/* Drawer shell — unscoped so Element Plus teleport styles apply */
.mgr-drawer.el-drawer {
  background: #eef2f0 !important;
}
.mgr-drawer .el-drawer__body {
  padding: 0 !important;
  height: 100%;
  overflow: auto;
}
</style>
