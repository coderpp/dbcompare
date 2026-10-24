<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { copyText } from './clipboard'
import { autoCompareAfterApply } from './preferences'
import {
  NSelect,
  NInput,
  NInputNumber,
  NCheckbox,
  NButton,
  NForm,
  NFormItem,
  NModal,
  NDrawer,
  NDrawerContent,
  NTabs,
  NTabPane,
  NProgress,
  NSpin,
  NSpace,
} from 'naive-ui'
import { message, confirmBox, promptBox } from './ui'
import * as ConnectionService from '../bindings/github.com/pp/db-compare/internal/service/connectionservice'
import * as CompareService from '../bindings/github.com/pp/db-compare/internal/service/compareservice'
import * as SyncService from '../bindings/github.com/pp/db-compare/internal/service/syncservice'
import * as ProfileService from '../bindings/github.com/pp/db-compare/internal/service/profileservice'
import { Events } from '@wailsio/runtime'
import WorkflowPanel from './WorkflowPanel.vue'
import SnapshotPanel from './SnapshotPanel.vue'
import * as SnapshotService from '../bindings/github.com/pp/db-compare/internal/service/snapshotservice'
import { saveArtifact } from './exports'
import SQLCode from './SQLCode.vue'
import { formatSQL } from './sqlPresentation'
import SettingsTransfer from './SettingsTransfer.vue'
import { exportSettings, settingsExporting } from './settingsTransfer'

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
const showSnapshots = ref(false)
const snapshotSource = ref<any>(null)
const showConnDialog = ref(false)
const showConnDrawer = ref(false)
const managerTab = ref('profiles')
watch(showConnDrawer, (show) => {
  if (show) managerTab.value = 'profiles'
})
const showOptions = ref(false)
const showWarnings = ref(false)
const onlyDifferences = ref(false)
const activeDiffSection = ref('columns')
const connFormDatabases = ref<string[]>([])
const loadingConnDatabases = ref(false)
const testingConn = ref(false)
const comparing = ref(false)
const planning = ref(false)
const applying = ref(false)
const showExecutionConfirm = ref(false)
const executionSide = ref('target')
const repairRefreshToken = ref(0)
const executionContext = computed(() => JSON.stringify([sourceId.value, sourceDatabase.value, targetId.value, targetDatabase.value, normalize.value, snapshotSource.value?.record.id]))
const executionEndpoint = computed(() => findConn(executionSide.value === 'source' ? sourceId.value : targetId.value))
const activeTab = ref('modify')
const sessionId = ref('')
const added = ref<ChangeItem[]>([])
const modified = ref<ChangeItem[]>([])
const dropped = ref<ChangeItem[]>([])
const warnings = ref<string[]>([])
const sourceInfo = ref('')
const targetInfo = ref('')
const planSQL = ref('')
const currentPlan = ref<any>(null)
const currentReport = ref<any>(null)
const autoSourceRepairs = ref<any[]>([])
const autoTargetRepairs = ref<any[]>([])
const showWorkflow = ref(false)
const workflowTab = ref<'report' | 'history' | 'encoding'>('report')
function openWorkflow(tab: 'report' | 'history' | 'encoding') {
  workflowTab.value = tab
  showWorkflow.value = true
}
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
  autoRepairComments: false,
  stripIntDisplayWidth: true,
  unifyUtf8Aliases: true,
  unifyUtf8mb4CiCollations: true,
  ignoreAutoIncrement: true,
  ignoreIndexOrder: true,
  ignoreIndexes: false,
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
    activeTab.value === 'all' ? tableRows.value :
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

// A preview is valid only for the selection/options that produced it.
const planInputKey = computed(() =>
  JSON.stringify([sessionId.value, sourceId.value, sourceDatabase.value, targetId.value, targetDatabase.value, [...selectedIds.value].sort(), allowDrop.value, allowUnsafe.value]),
)
const plannedInputKey = ref('')
function clearPlan() {
  showExecutionConfirm.value = false
  currentPlan.value = null
  plannedInputKey.value = ''
  planSQL.value = ''
  planStmtCount.value = 0
  planBlocked.value = false
  prechecks.value = []
}
watch(planInputKey, clearPlan, { flush: 'sync' })
watch(
  () => JSON.stringify([sourceId.value, sourceDatabase.value, targetId.value, targetDatabase.value, normalize.value]),
  () => {
    sessionId.value = ''
    autoSourceRepairs.value = []; autoTargetRepairs.value = []
    added.value = []; modified.value = []; dropped.value = []
    warnings.value = []; selectedTable.value = ''; tableDiff.value = null; loadingTableDiff.value = false
    clearPlan()
  },
  { flush: 'sync' },
)


const selectedTableCount = computed(
  () => tableRows.value.filter((r) => r.selected).length,
)

const hasCompared = computed(() => !!sessionId.value)
const automaticRepairCount = computed(() => normalize.value.autoRepairComments ? autoTargetRepairs.value.length + (snapshotSource.value && !sourceId.value ? 0 : autoSourceRepairs.value.length) : 0)
const canGeneratePlan = computed(() => hasCompared.value && (selectedTableCount.value > 0 || automaticRepairCount.value > 0))
const executionGroups = computed(() => {
  const plan = currentPlan.value
  if (!plan) return []
  const groups: any[] = []
  if (plan.sourceRepairPlan?.statements?.length) groups.push({ side: 'source', label: '源库注释修复', name: findConn(sourceId.value)?.name, endpoint: findConn(sourceId.value) ? connEndpoint(findConn(sourceId.value)!) : '', plan: plan.sourceRepairPlan })
  if (plan.statements?.length) groups.push({ side: plan.mode === 'repair' ? executionSide.value : 'target', label: plan.mode === 'repair' ? '注释修复' : '目标库同步及注释修复', name: executionEndpoint.value?.name, endpoint: executionEndpoint.value ? connEndpoint(executionEndpoint.value) : '', plan })
  return groups
})
function updatePlanPresentation() {
  planStmtCount.value = executionGroups.value.reduce((n, g) => n + g.plan.statements.length, 0)
  planSQL.value = (executionGroups.value.length > 1 ? '-- 按源库/目标库分组连接对应服务器执行，勿在单一连接中直接执行整个文件。\n\n' : '') + executionGroups.value.map(g => `-- [${g.side === 'source' ? '源库' : '目标库'}] ${g.name || ''}（${g.endpoint}）/ ${g.plan.targetDatabase} · ${g.label}\n` + g.plan.statements.map((s: any) => `-- ${s.comment || s.id}\n${s.sql};`).join('\n\n')).join('\n\n')
}
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

const connectionOptions = computed(() =>
  connections.value.map((c) => ({
    label: c.name || connEndpoint(c),
    value: c.id!,
  })),
)
const sourceDbOptions = computed(() => sourceDatabases.value.map((db) => ({ label: db, value: db })))
const targetDbOptions = computed(() => targetDatabases.value.map((db) => ({ label: db, value: db })))
const connFormDbOptions = computed(() => connFormDatabases.value.map((db) => ({ label: db, value: db })))
const profileSelectOptions = computed(() =>
  saveOverwriteOptions.value.map((p) => ({
    label: p.id === loadedProfileId.value ? `${p.name}（当前）` : p.name,
    value: p.id,
  })),
)

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


function columnParts(definition: string) {
  const match = definition.match(/\s+COMMENT\s+'((?:\\.|''|[^'])*)'$/i)
  const comment = match ? match[1].replace(/''/g, "'").replace(/\\'/g, "'") : ''
  const body = (match ? definition.slice(0, match.index) : definition).replace(/\s+CHARACTER SET\s+\w+/ig, '').replace(/\s+COLLATE\s+\w+/ig, '')
  const type = body.match(/^\w+(?:\([^)]*\))?/i)?.[0] || ''
  return { type, constraints: body.slice(type.length).trim(), comment }
}
const commonColumns = computed(() => (tableDiff.value?.columns || []).filter((x: any) => x.kind === 'same').map((x: any) => ({ ...x, ...columnParts(x.source || '') })))
const diffCounts = computed(() => ({
  columns: (tableDiff.value?.columns || []).filter((x: any) => x.kind !== 'same').length,
  indexes: (tableDiff.value?.indexes || []).filter((x: any) => x.kind !== 'same').length,
  attrs: (tableDiff.value?.attrs || []).filter((x: any) => x.kind !== 'same').length,
  foreignKeys: (tableDiff.value?.foreignKeys || []).filter((x: any) => x.kind !== 'same').length,
}))
const selectedDropCount = computed(() => tableRows.value.filter(r => r.selected && r.kind === 'drop').length)
const warningSummary = computed(() => warnings.value.some(w => w.includes('lower_case_table_names')) ? '大小写规则不同' : `${warnings.value.length} 项对比提醒`)
const planStatements = computed(() => {
  let line = 1
  return executionGroups.value.flatMap(g => g.plan.statements.map((stmt: any) => ({ ...stmt, id: `${g.side}-${stmt.id}`, executionSide: g.side, isRepair: g.plan.mode === 'repair' || stmt.id?.startsWith('repair-') }))).map((stmt: any) => {
    const sql = formatSQL(String(stmt.sql))
    const startLine = line
    line += sql.split('\n').length + 2
    return { ...stmt, sql, startLine }
  })
})
function tableChangeSummary(row: TableRow) {
  if (row.kind === 'add') return '新增整张表'
  if (row.kind === 'drop') return '删除整张表'
  const fields = row.items.filter(i => i.objectType === 'column').length
  const indexes = row.items.filter(i => i.objectType === 'index').length
  return [fields ? `${fields} 字段` : '', indexes ? `${indexes} 索引` : ''].filter(Boolean).join(' · ') || '表结构变更'
}
function scrollDiffSection(id: string) {
  activeDiffSection.value = id
  document.getElementById(`section-${id}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
}

function toggleTableRow(row: TableRow, val: boolean, openDetails = true) {
  if (applying.value || comparing.value) return
  row.items.forEach(i => { i.selected = val && i.objectType === 'table' })
  if (val && openDetails) void selectTable(row)
}

function selectAllTables(flag: boolean) {
  currentTableRows.value.forEach((r) => toggleTableRow(r, flag, false))
  if (flag && !selectedTable.value && currentTableRows.value[0]) void selectTable(currentTableRows.value[0])
}

function swapEnds() {
  if (applying.value || comparing.value) return
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

const databaseRequests = { source: 0, target: 0 }
function changeConnection(side: 'source' | 'target', id: string) {
  const database = findConn(id)?.database || ''
  if (side === 'source') {
    sourceId.value = id
    sourceDatabase.value = database
    sourceDatabases.value = database ? [database] : []
    snapshotSource.value = null
  } else {
    targetId.value = id
    targetDatabase.value = database
    targetDatabases.value = database ? [database] : []
  }
}
async function loadDatabases(side: 'source' | 'target') {
  const ids = side === 'source' ? sourceId : targetId
  const database = side === 'source' ? sourceDatabase : targetDatabase
  const databases = side === 'source' ? sourceDatabases : targetDatabases
  const id = ids.value
  const request = ++databaseRequests[side]
  const conn = findConn(id)
  if (!conn) {
    databases.value = []
    if (!(side === 'source' && snapshotSource.value)) database.value = ''
    return
  }
  try {
    const dbs = (await ConnectionService.ListDatabases({ id: conn.id } as any)) || []
    if (request !== databaseRequests[side] || id !== ids.value) return
    databases.value = dbs
    if (database.value && !dbs.includes(database.value)) {
      databases.value = [...dbs, database.value]
      message.warning(`连接「${conn.name}」的数据库「${database.value}」不在可访问列表中，请检查配置或权限`)
    }
  } catch (e: any) {
    if (request === databaseRequests[side] && id === ids.value) message.error(formatApiError(e))
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
    if (!silent) message.warning('请先填写主机和用户')
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
    if (!silent) message.error(`加载数据库列表失败：${formatApiError(e)}`)
  } finally {
    loadingConnDatabases.value = false
  }
}

function validateConnForm(c: Conn): boolean {
  if (!c.host?.trim() || !c.user?.trim()) {
    message.warning('请填写主机和用户')
    return false
  }
  return true
}

async function refreshConnections() {
  connections.value = (await ConnectionService.List()) || []
}

async function refreshSettings() {
  await Promise.all([refreshConnections(), refreshProfiles()])
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
    message.success(editing ? '连接已更新' : '连接已保存')
  } catch (e: any) {
    message.error(formatApiError(e))
  }
}

async function testConnPayload(c: Conn, reloadFormDatabases = false) {
  if (!validateConnForm(c)) return
  testingConn.value = true
  try {
    const info = await ConnectionService.Test(c as any)
    message.success(`连接成功：MySQL ${info.version}`)
    if (reloadFormDatabases) await loadConnFormDatabases(true)
  } catch (e: any) {
    message.error(formatApiError(e))
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

async function runCompare(automatic = false) {
  if (applying.value || comparing.value) return
  if (snapshotSource.value && !sourceId.value) {
    const target = { connectionId: targetId.value, database: targetDatabase.value }
    if (!target.connectionId || !target.database) { message.warning('请选择目标数据库'); return }
    const document = snapshotSource.value
    const options = { ...normalize.value }
    const context = executionContext.value
    comparing.value = true
    try {
      const compare = await SnapshotService.RestoreCompare(document.record.id, target, options as any)
      if (context !== executionContext.value) return
      await acceptSnapshot({ document, target, compare, options, automatic })
    } catch (e) { message.error(`${automatic ? '执行后自动重新对比失败：' : ''}${String(e)}`) } finally { comparing.value = false }
    return
  }
  snapshotSource.value = null
  const srcConn = findConn(sourceId.value)
  const dstConn = findConn(targetId.value)
  if (!srcConn || !dstConn) {
    message.warning('请选择源连接与目标连接')
    return
  }
  if (!sourceDatabase.value || !targetDatabase.value) {
    message.warning('请选择源数据库与目标数据库')
    return
  }
  const src = { ...srcConn, database: sourceDatabase.value }
  const dst = { ...dstConn, database: targetDatabase.value }
  const compareKey = JSON.stringify([sourceId.value, sourceDatabase.value, targetId.value, targetDatabase.value, normalize.value])
  comparing.value = true
  try {
    const res = await CompareService.Compare({
      source: src,
      target: dst,
      normalize: normalize.value,
    } as any)
    if (compareKey !== JSON.stringify([sourceId.value, sourceDatabase.value, targetId.value, targetDatabase.value, normalize.value])) return
    if (!res) throw new Error('empty compare result')
    sessionId.value = res.sessionId
    autoSourceRepairs.value = res.sourceRepairs || []; autoTargetRepairs.value = res.targetRepairs || []
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
    if (tableRows.value.length === 1) void selectTable(tableRows.value[0])
    message.success(
      `对比完成：+${counts.value.add} ~${counts.value.modify} -${counts.value.drop} 张表`,
    )
  } catch (e: any) {
    message.error(`${automatic ? '执行后自动重新对比失败：' : ''}${String(e)}`)
  } finally {
    comparing.value = false
  }
}

const selectedObjectItems = computed(() =>
  [...added.value, ...modified.value, ...dropped.value].filter(i => i.table === selectedTable.value && i.objectType !== 'table'),
)
const unrepresentedChanges = computed(() => selectedObjectItems.value.filter(item => {
  const rows = ({ column: visibleDiffColumns.value, index: visibleDiffIndexes.value, foreign_key: visibleDiffFKs.value, attr: visibleDiffAttrs.value } as Record<string, any[]>)[item.objectType]
  return !rows?.some(row => row.name === item.objectName)
}))
function acceptRepairPlan(plan: any, direct = false, side = 'target') {
  executionSide.value = side
  clearPlan()
  currentPlan.value = plan
  plannedInputKey.value = planInputKey.value
  updatePlanPresentation()
  prechecks.value = plan.prechecks || []
  planBlocked.value = !!plan.blocked
  if (direct) void runApply()
}
async function acceptSnapshot(result: any) {
 if(applying.value)return
 sourceId.value='';targetId.value=result.target.connectionId
 await nextTick(); await loadDatabases('target')
 sourceDatabase.value=result.document.record.database;targetDatabase.value=result.target.database
 normalize.value={...result.options,autoRepairComments:!!result.options?.autoRepairComments};allowDrop.value=false;allowUnsafe.value=false
 snapshotSource.value=result.document
 const res=result.compare
 sessionId.value=res.sessionId
 autoSourceRepairs.value=res.sourceRepairs||[];autoTargetRepairs.value=res.targetRepairs||[]
 added.value=(res.added||[]).map((i:any)=>({...i,selected:false}))
 modified.value=(res.modified||[]).map((i:any)=>({...i,selected:false}))
 dropped.value=(res.dropped||[]).map((i:any)=>({...i,selected:false}))
 warnings.value=[`期望结构来自快照：${result.document.record.database} · ${result.document.record.capturedAt}`,...(res.warnings||[])]
 sourceInfo.value=`快照 · ${result.document.record.capturedAt}`;targetInfo.value=`${res.targetInfo.version} / ${res.targetInfo.collationServer}`
 selectedTable.value='';tableDiff.value=null;clearPlan();showSnapshots.value=false
 activeTab.value=counts.value.modify?'modify':counts.value.add?'add':'drop'
 message.success(result.automatic ? '执行后已重新读取目标库并刷新对比结果' : '已读取实际目标库，请选择变更并生成 SQL，尚未执行')
}
async function acceptResume(result: any) {
  const run = result.run
  if(run.source.snapshotId){
   const doc=await SnapshotService.Get(run.source.snapshotId)
   await acceptSnapshot({document:doc,target:{connectionId:run.target.id,database:run.target.database},compare:result.compare,options:run.normalize})
   const wanted=new Set(result.changeIds||[])
   const tables=new Set([...added.value,...modified.value,...dropped.value].filter(i=>wanted.has(i.id)).map(i=>i.table))
   for(const i of [...added.value,...modified.value,...dropped.value])i.selected=i.objectType==='table'&&tables.has(i.table)
   allowDrop.value=run.allowDrop;allowUnsafe.value=run.allowUnsafe;return
  }
  snapshotSource.value=null
  sourceId.value = run.source.id
  targetId.value = run.target.id
  await Promise.all([loadDatabases('source'), loadDatabases('target')])
  sourceDatabase.value = run.source.database
  targetDatabase.value = run.target.database
  normalize.value = { ...normalize.value, ...run.normalize, ignoreIndexes: !!run.normalize?.ignoreIndexes, autoRepairComments: !!run.normalize?.autoRepairComments }
  allowDrop.value = run.allowDrop
  allowUnsafe.value = run.allowUnsafe
  const res = result.compare
  sessionId.value = res.sessionId
  autoSourceRepairs.value = res.sourceRepairs || []; autoTargetRepairs.value = res.targetRepairs || []
  const wanted = new Set(result.changeIds || [])
  const selectedTables = new Set([...(res.added || []), ...(res.modified || []), ...(res.dropped || [])].filter((i: any) => wanted.has(i.id)).map((i: any) => i.table))
  const restoreTable = (i: any) => ({ ...i, selected: i.objectType === 'table' && selectedTables.has(i.table) })
  added.value = (res.added || []).map(restoreTable)
  modified.value = (res.modified || []).map(restoreTable)
  dropped.value = (res.dropped || []).map(restoreTable)
  warnings.value = res.warnings || []
  sourceInfo.value = `${res.sourceInfo.version} / ${res.sourceInfo.collationServer}`
  targetInfo.value = `${res.targetInfo.version} / ${res.targetInfo.collationServer}`
  selectedTable.value = ''; tableDiff.value = null; clearPlan()
  if (counts.value.modify) activeTab.value = 'modify'
  else if (counts.value.add) activeTab.value = 'add'
  else activeTab.value = 'drop'
  if (result.mode === 'repair' && result.repairIds?.length) {
    const plan = await SyncService.PlanCommentRepair({ sessionId: res.sessionId, side: 'target', ids: result.repairIds } as any)
    if (plan) acceptRepairPlan(plan)
  }
}
async function focusLinkedItem(id: string) {
  const item = [...added.value, ...modified.value, ...dropped.value].find(i => i.id === id)
  if (!item) return
  const row = tableRows.value.find(r => r.table === item.table)
  if (!row) return
  activeTab.value = row.kind
  await selectTable(row)
  await nextTick()
  document.getElementById(diffRowID(item.objectType, item.objectName))?.scrollIntoView({ block: 'center', behavior: 'smooth' })
}
function diffRowID(type: string, name: string) { return `diff-${type}-${encodeURIComponent(name)}` }

function selectAll(flag: boolean) {
  if (flag) selectAllTables(true)
  else tableRows.value.forEach(row => toggleTableRow(row, false, false))
}

async function selectTable(row: TableRow) {
  if (selectedTable.value === row.table && (tableDiff.value || loadingTableDiff.value)) return
  selectedTable.value = row.table
  activeDiffSection.value = 'columns'
  tableDiff.value = null
  const requestSession = sessionId.value
  if (!requestSession) return
  loadingTableDiff.value = true
  const isCurrent = () => sessionId.value === requestSession && selectedTable.value === row.table
  try {
    const res = await CompareService.TableDiff(requestSession, row.table)
    if (isCurrent()) tableDiff.value = res
  } catch (e: any) {
    if (isCurrent()) message.error(`加载表对比失败：${formatApiError(e)}`)
  } finally {
    if (isCurrent()) loadingTableDiff.value = false
  }
}

const focusedTableRow = computed(() => tableRows.value.find(row => row.table === selectedTable.value))
function linkedChangeLabel(id: string) {
  const item = currentPlan.value?.selected?.find((i: any) => i.id === id)
  if (!item) return '查看关联对象'
  const action = ({ add: '新增', modify: '修改', drop: '删除' } as Record<string, string>)[item.kind] || '变更'
  const object = ({ table: '表', column: '字段', index: '索引', foreign_key: '外键', check: '检查约束', attr: '表属性', comment: '注释' } as Record<string, string>)[item.objectType] || '对象'
  return `${action}${object}：${item.objectName}`
}
function statementTitle(stmt: any) {
  if (currentPlan.value?.mode === 'repair') return '修复注释'
  if (/^DROP TABLE\b/i.test(stmt.sql.trim())) return '删除表'
  if (/^CREATE TABLE\b/i.test(stmt.sql.trim())) return '新增表'
  return '修改表结构'
}

async function runPlan() {
  if (applying.value || comparing.value || !sessionId.value) return
  const inputKey = planInputKey.value
  clearPlan()
  planning.value = true
  progress.value = { total: 0, done: 0, message: '', failed: false }
  try {
    const res = await SyncService.Plan({
      sessionId: sessionId.value,
      changeIds: selectedIds.value,
      allowDrop: allowDrop.value,
      allowUnsafe: allowUnsafe.value,
    } as any)
    if (inputKey !== planInputKey.value) return
    if (!res) throw new Error('empty plan result')
    executionSide.value = 'target'
    currentPlan.value = res
    plannedInputKey.value = inputKey
    prechecks.value = res.prechecks || []
    planBlocked.value = !!res.blocked
    updatePlanPresentation()
    const dropsSkipped = (res.prechecks || []).some((p: any) => p.code === 'drops_skipped')
    if (planStmtCount.value) {
      // Presentation includes both source repair and target SQL groups.
    } else if (dropsSkipped) {
      planSQL.value =
        '-- 未生成 DDL：当前勾选的差异仅含删除（如目标多出的列/索引/表）。\n' +
        '-- 请展开「对比选项」勾选「允许删除多余对象」后，再点「生成 SQL」。'
    } else {
      planSQL.value = '-- 未生成 DDL：所选差异在过滤后为空，请检查勾选或同步选项。'
    }
    if (res.blocked) message.warning('预检存在阻断项，请查看 SQL 区问题或勾选允许不安全同步')
    else if (!planStmtCount.value && dropsSkipped) message.warning('差异仅为删除操作，请勾选「允许删除多余对象」')
    else if (!planStmtCount.value) message.warning('未生成 DDL')
    else message.success(`已生成 ${planStmtCount.value} 条 DDL`)
  } catch (e: any) {
    message.error(String(e))
  } finally {
    planning.value = false
  }
}

async function runApply() {
  if (!sessionId.value || !planStmtCount.value || plannedInputKey.value !== planInputKey.value) {
    message.warning('请先生成同步 SQL')
    return
  }
  if (currentPlan.value?.mode !== 'repair') executionSide.value = 'target'
  showExecutionConfirm.value = true
}

async function confirmExecution() {
  const plan = currentPlan.value
  if (!plan || applying.value || comparing.value || plannedInputKey.value !== planInputKey.value || (plan.blocked || planBlocked.value || executionGroups.value.some(g => g.plan.blocked))) {
    showExecutionConfirm.value = false
    message.warning('计划已失效，请重新生成 SQL')
    return
  }
  const context = executionContext.value
  const refresh = autoCompareAfterApply.value
  showExecutionConfirm.value = false
  applying.value = true
  try {
    const groups = executionGroups.value.slice()
    let applied = 0
    let clean = true
    for (let i = 0; i < groups.length; i++) {
      const group = groups[i]
      const res = await SyncService.Apply({ sessionId: group.plan.sessionId, planId: group.plan.id, dryRun: false } as any)
      if (!res) throw new Error('执行结果为空；请查看执行历史并重新对比')
      applied += res.applied
      currentReport.value = res.run
      workflowTab.value = plan.mode === 'repair' ? 'encoding' : 'report'
      showWorkflow.value = true
      if (res.errors?.length) message.error(res.errors.join('\n'))
      if (!res.verifyClean) {
        clean = false
        message.warning(`${group.label}执行结束：${res.verifySummary}${i < groups.length - 1 ? '；后续数据库操作已停止' : ''}`)
        break
      }
    }
    clearPlan()
    if (clean) message.success(`执行完成：${applied} 条，复查通过${groups.length > 1 ? '；源库与目标库报告分别保存在执行历史' : ''}`)
  } catch (e: any) {
    message.error(String(e))
    // An interrupted call may have applied DDL. Never leave a replayable old plan.
    clearPlan()
  } finally {
    applying.value = false
  }
  if (refresh && context === executionContext.value) {
    await runCompare(true)
  }
  if (plan.mode === 'repair') repairRefreshToken.value++
}

function openSaveProfileDialog() {
  if (!sourceId.value || !targetId.value || !sourceDatabase.value || !targetDatabase.value) {
    message.warning('请先选择源与目标连接和数据库')
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
    message.warning('请填写方案名称')
    return
  }
  if (saveProfileMode.value === 'overwrite') {
    if (!saveProfileOverwriteId.value) {
      message.warning('请选择要覆盖的方案')
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
    message.success(saveProfileMode.value === 'overwrite' ? '方案已覆盖保存' : '同步方案已保存')
  } catch (e: any) {
    message.error(formatApiError(e))
  } finally {
    savingProfile.value = false
  }
}

async function saveProfile() {
  openSaveProfileDialog()
}

async function renameProfile(p: any) {
  const name = await promptBox('重命名方案', p.name || '', '方案名称')
  if (name == null || name === p.name) return
  await ProfileService.Save({ ...p, name } as any)
  await refreshProfiles()
  message.success('方案已重命名')
}

async function removeProfile(id: string) {
  if (!(await confirmBox('删除后无法恢复，确认删除此方案？', '删除方案'))) return
  await ProfileService.Delete(id)
  if (loadedProfileId.value === id) loadedProfileId.value = ''
  await refreshProfiles()
  message.success('方案已删除')
}

function profileEndpointLabel(p: any): string {
  const src = findConn(p.sourceId)
  const dst = findConn(p.targetId)
  const srcName = src ? src.name || connEndpoint(src) : '未知连接'
  const dstName = dst ? dst.name || connEndpoint(dst) : '未知连接'
  return `${srcName} / ${p.sourceDatabase || '—'}  →  ${dstName} / ${p.targetDatabase || '—'}`
}

async function loadProfile(p: any) {
  if (applying.value || comparing.value) return
  loadedProfileId.value = p.id || ''
  sourceId.value = p.sourceId
  targetId.value = p.targetId
  sourceDatabase.value = p.sourceDatabase || ''
  targetDatabase.value = p.targetDatabase || ''
  normalize.value = { ...normalize.value, ...p.normalize, ignoreIndexes: !!p.normalize?.ignoreIndexes, autoRepairComments: !!p.normalize?.autoRepairComments }
  allowDrop.value = !!p.allowDrop
  await loadDatabases('source')
  await loadDatabases('target')
  showConnDrawer.value = false
  message.success(`已加载方案：${p.name}`)
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
  if (kind === 'same') return ''
  return '~'
}

async function copySQL() {
  if (!planStmtCount.value) { message.warning('暂无可复制的 SQL'); return }
  await copyText(planSQL.value)
}

</script>

<template>
  <div class="app">
    <header class="topbar">
      <div class="brand-block">
        <img class="brand-mark" src="/brand-mark.png" alt="" width="24" height="24" />
        <div class="brand">DB Compare</div>
      </div>
      <nav class="main-nav" aria-label="主导航">
        <button class="nav-item current" type="button" @click="showWorkflow = false; showConnDrawer = false">结构对比</button>
        <button class="nav-item" type="button" @click="showSnapshots = true">结构快照</button>
        <button class="nav-item" type="button" @click="openWorkflow('history')">执行历史与报告</button>
        <button class="nav-item" type="button" @click="openWorkflow('encoding')" title="检测乱码、预览修复 SQL">乱码检测与修复</button>
      </nav>
      <button type="button" class="connection-button" @click="showConnDrawer = true">连接与方案</button>
    </header>

    <section class="setup">
      <div class="endpoint source">
        <div class="endpoint-label">
          源
        </div>
        <n-select
          :value="sourceId"
          @update:value="changeConnection('source', $event)"
          :disabled="applying || comparing"
          :options="connectionOptions"
          :placeholder="snapshotSource && !sourceId ? '快照：' + snapshotSource.record.database : '选择连接'"
          filterable
          class="full"
        />
        <n-select
          v-model:value="sourceDatabase"
          :options="sourceDbOptions"
          placeholder="数据库"
          filterable
          tag
          :disabled="!sourceId || applying || comparing"
          class="full"
        />
        <div class="endpoint-meta" v-if="sourceInfo">{{ sourceInfo }}</div>
        <div class="endpoint-meta muted" v-else>{{ sourceConnLabel }}</div>
      </div>

      <button type="button" class="swap-btn" :disabled="applying || comparing" title="对调源与目标" @click="swapEnds">⇄</button>

      <div class="endpoint target">
        <div class="endpoint-label">
          目标
        </div>
        <n-select
          :value="targetId"
          @update:value="changeConnection('target', $event)"
          :disabled="applying || comparing"
          :options="connectionOptions"
          placeholder="选择连接"
          filterable
          class="full"
        />
        <n-select
          v-model:value="targetDatabase"
          :options="targetDbOptions"
          placeholder="数据库"
          filterable
          tag
          :disabled="!targetId || applying || comparing"
          class="full"
        />
        <div class="endpoint-meta" v-if="targetInfo">{{ targetInfo }}</div>
        <div class="endpoint-meta muted" v-else>{{ targetConnLabel }}</div>
      </div>
      <button type="button" class="ghost-btn compare-button" :disabled="comparing || applying || planning" @click="runCompare()">{{ comparing ? '对比中…' : hasCompared ? '重新对比' : '开始对比' }}</button>
    </section>

    <section class="quick-options">
      <n-checkbox :disabled="applying || comparing" :checked="!normalize.ignoreIndexes" @update:checked="normalize.ignoreIndexes = !$event" title="对比已有表的索引（含主键）；新增表始终保留完整建表结构">对比索引</n-checkbox>
      <n-checkbox :disabled="applying || comparing || planning" v-model:checked="normalize.autoRepairComments" title="对比时还原能够可靠恢复的乱码注释，生成 SQL 时自动纳入源库和目标库修复；确认前不写库">自动修复乱码注释</n-checkbox>
      <n-checkbox :disabled="applying || comparing" v-model:checked="allowDrop" class="opt-danger">允许删除多余对象</n-checkbox>
      <button type="button" class="toolbar-text" :class="{ active: showOptions }" @click="showOptions = !showOptions" :aria-expanded="showOptions">高级选项 {{ showOptions ? '收起' : '展开' }}</button>
      <span class="scheme-label" :title="loadedProfile?.name || '未保存方案'">方案：<b>{{ loadedProfile?.name || '未保存' }}</b></span>
      <button type="button" class="toolbar-text save-scheme" @click="saveProfile">保存方案</button>
      <button v-if="warnings.length" type="button" class="warning-summary" @click="showWarnings = !showWarnings" :aria-expanded="showWarnings">{{ warningSummary }} · {{ showWarnings ? '收起说明' : '查看说明' }}</button>
    </section>
    <section v-if="showOptions" class="options-bar">
      <n-checkbox :disabled="applying || comparing" v-model:checked="normalize.stripIntDisplayWidth">忽略整数显示宽度</n-checkbox>
      <n-checkbox :disabled="applying || comparing" v-model:checked="normalize.unifyUtf8Aliases">统一 utf8 / utf8mb3</n-checkbox>
      <n-checkbox :disabled="applying || comparing" v-model:checked="normalize.unifyUtf8mb4CiCollations">统一 general_ci / 0900_ai_ci</n-checkbox>
      <n-checkbox :disabled="applying || comparing" v-model:checked="normalize.ignoreAutoIncrement">忽略 AUTO_INCREMENT</n-checkbox>
      <n-checkbox :disabled="applying || comparing" v-model:checked="allowUnsafe" class="opt-danger">允许不安全降级</n-checkbox>

    </section>

    <div v-if="warnings.length && showWarnings" class="warn-strip">
      <div v-for="(w, i) in warnings" :key="i" class="warn-item" :class="{ block: w.startsWith('[BLOCK]') }">
        {{ w }}
      </div>
    </div>

    <details v-if="hasCompared && normalize.autoRepairComments" style="padding: 8px 18px; border-bottom: 1px solid #e0e6ee; font-size: 12px">
      <summary>自动注释修复：{{ snapshotSource && !sourceId ? '快照模型' : '源库' }} {{ autoSourceRepairs.length }} 项，目标库 {{ autoTargetRepairs.length }} 项（点击查看）</summary>
      <p>生成 SQL 时自动纳入本次对比范围内全部可修复注释，执行前统一确认。无法可靠还原的内容不自动修改。{{ snapshotSource && !sourceId ? '快照注释仅纠正期望模型，原快照保持不变。' : '' }}</p>
      <div style="max-height: 160px; overflow: auto">
        <p v-for="r in autoSourceRepairs" :key="'source-' + r.id"><b>{{ snapshotSource && !sourceId ? '快照模型' : '源库' }} · {{ r.table }}.{{ r.column || '(表注释)' }}</b>：{{ r.original }} → {{ r.recovered }}</p>
        <p v-for="r in autoTargetRepairs" :key="'target-' + r.id"><b>目标库 · {{ r.table }}.{{ r.column || '(表注释)' }}</b>：{{ r.original }} → {{ r.recovered }}</p>
      </div>
    </details>

    <main class="workspace" v-if="hasCompared">
      <aside class="pane tables-pane">
        <div class="pane-head table-heading">
          <div class="pane-title">差异表</div>
          <span class="selection-count">已选 {{ selectedTableCount }} / {{ totalDiffTables }}</span>
        </div>
        <div class="table-filter"><n-input v-model:value="tableFilter" clearable placeholder="搜索表名" size="small" /></div>
        <div class="kind-tabs">
          <button type="button" :class="{ on: activeTab === 'all' }" @click="activeTab = 'all'">全部 <em>{{ totalDiffTables }}</em></button>
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

        <div class="table-list" v-if="currentTableRows.length">
          <div
            v-for="row in currentTableRows"
            :key="row.table"
            class="table-row"
            :class="{ active: row.table === selectedTable, [`kind-${row.kind}`]: true }"
            tabindex="0" :aria-label="`查看 ${row.table} 的结构差异`" :aria-current="row.table === selectedTable ? 'true' : undefined"
            @keydown.enter="selectTable(row)" @keydown.space.prevent="selectTable(row)"
            @click="selectTable(row)"
          >
            <n-checkbox
              :checked="row.selected"
              :aria-label="`同步 ${row.table}`"
              :disabled="applying || comparing"
              @update:checked="(v: boolean) => toggleTableRow(row, v)"
              @click.stop
            />
            <div class="table-row-main">
              <code class="table-name" :title="row.table">{{ row.table }}</code>
              <span class="table-row-meta">{{ tableChangeSummary(row) }}<span v-if="row.risk !== 'info'" class="risk-chip" :class="`risk-${row.risk}`">{{ riskLabel(row.risk) }}</span></span>
            </div>
          </div>
        </div>
        <div v-else class="empty-inline">{{ tableFilter ? '没有匹配的差异表' : '当前分类无差异表' }}</div>
        <div class="table-selection-footer"><button type="button" class="toolbar-text" :disabled="applying || comparing || !currentTableRows.length" @click="selectAllTables(true)">全选筛选结果</button><button type="button" class="mini-btn" :disabled="applying || comparing || !selectedTableCount" @click="selectAll(false)">清空选择</button></div>
      </aside>

      <section class="pane diff-pane">
        <div class="pane-head diff-heading">
          <div>
            <div class="pane-title"><code v-if="selectedTable">{{ selectedTable }}</code><template v-else>结构对比</template><span v-if="focusedTableRow" class="table-kind" :class="`kind-${focusedTableRow.kind}`">{{ focusedTableRow.kind === 'add' ? '新增' : focusedTableRow.kind === 'drop' ? '删除' : '修改' }}</span></div>
            <div class="pane-sub">源（期望）与目标（当前）的结构</div>
          </div>
          <n-spin v-if="loadingTableDiff" :size="18" />
          <n-checkbox v-else-if="selectedTable" v-model:checked="onlyDifferences" class="only-diff">仅看差异</n-checkbox>
        </div>
        <nav v-if="selectedTable && tableDiff" class="diff-navigation" aria-label="结构分类">
          <button type="button" :class="{ active: activeDiffSection === 'columns' }" @click="scrollDiffSection('columns')">字段 <em>{{ diffCounts.columns }}</em></button>
          <button type="button" :class="{ active: activeDiffSection === 'indexes' }" @click="scrollDiffSection('indexes')">索引 <em>{{ diffCounts.indexes }}</em></button>
          <button type="button" :class="{ active: activeDiffSection === 'attrs' }" @click="scrollDiffSection('attrs')">表属性 <em>{{ diffCounts.attrs }}</em></button>
          <button v-if="diffCounts.foreignKeys" type="button" :class="{ active: activeDiffSection === 'foreignKeys' }" @click="scrollDiffSection('foreignKeys')">外键 <em>{{ diffCounts.foreignKeys }}</em></button>
        </nav>

        <div v-if="!selectedTable" class="empty-state">
          <p>从左侧选择一张表，查看字段 / 索引差异</p>
        </div>
        <div v-else-if="loadingTableDiff" class="empty-state"><p>加载中…</p></div>
        <div v-else-if="tableDiff && !hasVisibleDiff && !unrepresentedChanges.length && (onlyDifferences || !commonColumns.length)" class="empty-state">
          <p>未检测到可展示的结构差异</p>
        </div>
        <div v-else-if="tableDiff" class="diff-scroll">
          <div v-if="focusedTableRow?.kind === 'drop'" class="table-operation operation-drop">
            <strong>删除整张表</strong>
            <p>源库中不存在此表。执行后将删除目标库 <b>{{ targetDatabase }}</b> 中的表及其全部数据。</p>
            <span>下方展示即将删除的目标表结构。结构快照不包含业务数据。</span>
          </div>
          <div v-else-if="focusedTableRow?.kind === 'add'" class="table-operation operation-add">
            <strong>新增整张表</strong>
            <p>目标库中不存在此表，将按下方源库结构创建；不复制业务数据。</p>
          </div>
          <div v-if="unrepresentedChanges.length" class="diff-section">
            <div class="diff-section-title">其他结构差异</div>
            <ul class="other-differences"><li v-for="item in unrepresentedChanges" :id="diffRowID(item.objectType, item.objectName)" :key="item.id">{{ item.summary }}</li></ul>
          </div>
          <div v-if="visibleDiffAttrs.length" id="section-attrs" class="diff-section">
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
                <tr v-for="attr in visibleDiffAttrs" :id="diffRowID('attr', attr.name)" :key="attr.name" :class="`diff-row-${attr.kind}`">
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

          <div v-if="visibleDiffColumns.length" id="section-columns" class="diff-section">
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
                <tr v-for="col in visibleDiffColumns" :id="diffRowID('column', col.name)" :key="col.name" :class="`diff-row-${col.kind}`">
                  <td class="col-name">
                    <span class="diff-badge" :class="`badge-${col.kind}`">{{ kindMark(col.kind) }}</span>
                    {{ col.name }}
                  </td>
                  <td class="mono">{{ col.source || '不存在' }}</td>
                  <td class="mono">{{ col.target || '不存在' }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div v-if="visibleDiffIndexes.length" id="section-indexes" class="diff-section">
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
                <tr v-for="idx in visibleDiffIndexes" :id="diffRowID('index', idx.name)" :key="idx.name" :class="`diff-row-${idx.kind}`">
                  <td class="col-name">
                    <span class="diff-badge" :class="`badge-${idx.kind}`">{{ kindMark(idx.kind) }}</span>
                    {{ idx.name }}
                  </td>
                  <td class="mono">{{ idx.source || '不存在' }}</td>
                  <td class="mono">{{ idx.target || '不存在' }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div v-if="visibleDiffFKs.length" id="section-foreignKeys" class="diff-section">
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
                <tr v-for="fk in visibleDiffFKs" :id="diffRowID('foreign_key', fk.name)" :key="fk.name" :class="`diff-row-${fk.kind}`">
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
          <div v-if="!onlyDifferences && commonColumns.length" class="diff-section common-structure">
            <div class="diff-section-title">表结构 <span>（{{ commonColumns.length }} 个相同字段）</span></div>
            <table class="diff-table"><thead><tr><th>字段</th><th>类型</th><th>约束</th><th>注释</th></tr></thead><tbody><tr v-for="col in commonColumns" :key="col.name" :id="diffRowID('column', col.name)" :title="col.source"><td>{{ col.name }}</td><td class="mono">{{ col.type }}</td><td class="mono">{{ col.constraints || '—' }}</td><td>{{ col.comment || '—' }}</td></tr></tbody></table>
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
          <div class="export-actions">
            <button type="button" class="mini-btn" :disabled="!planStmtCount" @click="copySQL">复制</button>
            <button type="button" class="mini-btn" :disabled="!currentPlan" @click="saveArtifact({ sessionId: currentPlan.sessionId, kind: 'sql' })">导出 SQL</button>
            <button type="button" class="mini-btn" :disabled="!currentPlan" @click="saveArtifact({ sessionId: currentPlan.sessionId, kind: 'snapshot' })">导出快照</button>
          </div>
        </div>

        <div v-if="prechecks.length" class="prechecks">
          <div v-for="(p, i) in prechecks" :key="i" class="precheck" :class="`level-${p.level}`">
            <strong>{{ p.code }}</strong>
            <span>{{ p.message }}</span>
          </div>
        </div>

        <div v-if="applying || progress.total" class="progress-wrap">
          <n-progress
            type="line"
            :percentage="progress.total ? Math.round((100 * progress.done) / progress.total) : 0"
            :status="progress.failed ? 'error' : undefined"
            :height="8"
            :border-radius="4"
            :indicator-placement="'outside'"
          />
        </div>

        <div v-if="currentPlan?.sourceRepairPlan" class="plan-target">先修复源库：<strong>{{ currentPlan.sourceRepairPlan.targetDatabase }}</strong>{{ currentPlan.statements?.length ? '，再执行目标库计划' : '；目标库无待执行 SQL' }}；所有 SQL 均列在下方。</div>
        <div v-if="currentPlan?.statements?.length" class="plan-target">{{ currentPlan.mode === 'repair' ? '注释修复目标' : '执行目标' }}：<strong>{{ currentPlan.targetDatabase }}</strong><span> · {{ currentPlan.mode === 'repair' ? '仅更新注释' : '仅同步结构' }}</span></div>
        <div v-if="currentPlan" class="statement-list">
          <section v-for="(stmt, index) in planStatements" :key="stmt.id" class="statement-card" :class="{ 'statement-focused': stmt.table === selectedTable }">
            <div class="statement-heading"><span class="statement-number">{{ index + 1 }}</span><span class="statement-description">{{ stmt.executionSide === 'source' ? '源库' : '目标库' }} · {{ stmt.table }} · {{ statementTitle(stmt) }}</span><button v-if="!stmt.isRepair && stmt.changeIds?.length" type="button" class="change-link" @click="focusLinkedItem(stmt.changeIds[0])">查看关联差异</button></div>
            <details v-if="stmt.changeIds?.length" class="statement-associations"><summary>关联 {{ stmt.changeIds.length }} 项变更</summary><div class="statement-links"><template v-for="id in stmt.changeIds" :key="id"><span v-if="stmt.isRepair" class="change-reference">{{ linkedChangeLabel(id) }}</span><button v-else type="button" class="change-link" @click="focusLinkedItem(id)">{{ linkedChangeLabel(id) }}</button></template></div></details>
            <SQLCode :sql="stmt.sql" :start-line="stmt.startLine" />
          </section>
        </div>
        <div v-else class="sql-placeholder"><span class="placeholder-label">SQL PREVIEW</span><p>{{ planning ? '正在生成同步计划…' : '选择差异表后，生成同步 SQL' }}</p><span>预览并确认执行目标，再进行同步。</span><button type="button" class="secondary-btn" :disabled="!canGeneratePlan || planning || applying || comparing" @click="runPlan">{{ planning ? '生成中…' : '生成所选 SQL' }}</button></div>
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

    <footer class="action-bar">
      <div class="action-summary"><strong>已选 {{ selectedTableCount }} 张表<span v-if="planStmtCount"> · {{ planStmtCount }} 条 SQL</span></strong><span v-if="selectedDropCount" class="deletion-notice">删除 {{ selectedDropCount }} 张表 · 需确认</span><span v-else-if="hasCompared && !totalDiffTables" class="sync-state">{{ automaticRepairCount ? '结构一致，仍有注释待修复' : '结构一致' }}</span></div>
      <div class="execution-actions"><n-checkbox v-model:checked="autoCompareAfterApply" :disabled="applying || comparing">执行后自动重新对比</n-checkbox><span class="plan-status">{{ applying ? '正在执行，请勿关闭' : currentPlan ? (planBlocked && !allowUnsafe ? '预检查未通过' : '已生成计划，尚未执行') : hasCompared ? (canGeneratePlan ? '待生成 SQL（含自动注释修复）' : '请选择要同步的表') : '尚未开始对比' }}</span><button type="button" class="secondary-btn" :disabled="!canGeneratePlan || planning || applying || comparing" @click="runPlan">{{ planning ? '生成中…' : currentPlan ? '重新生成 SQL' : '生成所选 SQL' }}</button><button v-if="currentPlan" type="button" class="primary-btn execute-button" :title="executionGroups.map(g => `${g.side === 'source' ? '源库' : '目标库'}：${g.plan.targetDatabase}`).join('；')" :disabled="!planStmtCount || applying || comparing || (planBlocked && !allowUnsafe)" @click="runApply">{{ applying ? '执行中…' : currentPlan.sourceRepairPlan ? (currentPlan.statements?.length ? '执行源库修复及目标库同步' : '执行源库注释修复') : `执行到 ${currentPlan.targetDatabase}` }}</button></div>
    </footer>

    <n-drawer v-model:show="showConnDrawer" :width="400" placement="right" :trap-focus="false">
      <n-drawer-content :native-scrollbar="false" body-content-style="padding: 0; height: 100%; background: #eef2f0;">
        <div class="mgr">
          <header class="mgr-header">
            <div>
              <div class="mgr-title">连接与方案</div>
              <div class="mgr-sub">管理数据库连接与已存对比方案</div>
            </div>
            <button type="button" class="mini-btn" @click="showConnDrawer = false">关闭</button>
          </header>
          <SettingsTransfer :disabled="applying || planning || comparing" :empty="!connections.length && !profiles.length" @imported="refreshSettings" />

          <n-tabs v-model:value="managerTab" class="mgr-tabs" type="line" justify-content="space-evenly" :pane-style="{ padding: '0' }" animated>
            <n-tab-pane name="profiles" :tab="`方案 (${profiles.length})`">
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
                    <button type="button" class="text-btn" :disabled="applying || planning || settingsExporting" @click="exportSettings({ profileIds: [p.id] })">导出</button>
                    <button type="button" class="text-btn" @click="loadProfile(p)">加载</button>
                    <button type="button" class="text-btn" @click="renameProfile(p)">重命名</button>
                    <button type="button" class="text-btn danger" @click="removeProfile(p.id)">删除</button>
                  </div>
                </div>
              </section>
            </n-tab-pane>
            <n-tab-pane name="connections" :tab="`连接 (${connections.length})`">
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
                    <button type="button" class="text-btn" :disabled="applying || planning || settingsExporting" @click="exportSettings({ connectionIds: [c.id!] })">导出</button>
                    <button type="button" class="text-btn" @click="openEditConn(c)">编辑</button>
                    <button type="button" class="text-btn" :disabled="testingConn" @click="testConn(c)">测试</button>
                    <button type="button" class="text-btn danger" @click="removeConn(c.id!)">删除</button>
                  </div>
                </div>
              </section>
            </n-tab-pane>
          </n-tabs>
        </div>
      </n-drawer-content>
    </n-drawer>

    <SnapshotPanel v-model:show="showSnapshots" :connections="connections" :disabled="applying || planning || comparing" @restore="acceptSnapshot" />

    <n-modal v-model:show="showExecutionConfirm" preset="card" :title="currentPlan?.mode === 'repair' ? '确认执行注释修复' : '确认执行同步'" style="width: min(900px, 94vw); max-height: 92vh" content-style="overflow: auto; min-height: 0" :mask-closable="false">
      <template v-if="currentPlan">
        <p v-for="(group, i) in executionGroups" :key="group.side"><b>{{ i + 1 }}. {{ group.side === 'source' ? '源库' : '目标库' }} · {{ group.name }}</b>（{{ group.endpoint }}） / <b>{{ group.plan.targetDatabase }}</b> · {{ group.plan.statements.length }} 条 SQL · {{ group.label }}</p>
        <p v-if="currentPlan.sourceRepairPlan" class="deletion-notice">{{ currentPlan.statements?.length ? '本次会修改源库和目标库。先修复源库注释，成功后再执行目标库计划' : '本次仅修改源库注释，目标库无待执行 SQL' }}；任一步失败即停止，已执行部分不可自动回滚。</p>
        <p v-if="currentPlan.mode === 'repair' && executionSide === 'source'" class="deletion-notice">本次将修改源库注释，请确认连接和数据库。</p>
        <p>执行前自动保存结构快照，不含业务数据；DDL 不可事务回滚。</p>
        <div style="max-height: 50vh; overflow: auto"><SQLCode :sql="planSQL" /></div>
      </template>
      <p v-else>计划已失效，请关闭后重新生成 SQL。</p>
      <template #footer>
        <n-space v-if="currentPlan" justify="space-between" align="center" :wrap="true">
          <n-space align="center" :wrap="true">
            <n-button size="small" @click="copySQL">复制 SQL</n-button>
            <n-button size="small" @click="saveArtifact({ sessionId: currentPlan.sessionId, kind: 'sql' })">导出 SQL</n-button>
            <n-checkbox v-model:checked="autoCompareAfterApply">执行后自动重新对比</n-checkbox>
          </n-space>
          <n-space :wrap="false">
            <n-button @click="showExecutionConfirm = false">取消</n-button>
            <n-button type="primary" :disabled="applying || comparing" @click="confirmExecution">确认执行</n-button>
          </n-space>
        </n-space>
      </template>
    </n-modal>

    <WorkflowPanel v-model:show="showWorkflow" :initial-tab="workflowTab" :session-id="sessionId" :report="currentReport" :refresh-token="repairRefreshToken" :disabled="applying || planning || comparing" @repair-plan="acceptRepairPlan" @resume="acceptResume" />

    <n-modal
      v-model:show="showSaveProfileDialog"
      preset="card"
      title="保存方案"
      style="width: 440px"
      :bordered="false"
      :segmented="{ content: true, footer: 'soft' }"
    >
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
          <n-select
            v-model:value="saveProfileOverwriteId"
            :options="profileSelectOptions"
            placeholder="选择要覆盖的方案"
            filterable
            @update:value="onOverwriteTargetChange"
          />
        </div>

        <div class="save-field">
          <div class="save-label">{{ saveProfileMode === 'overwrite' ? '方案名称（可修改）' : '方案名称' }}</div>
          <n-input v-model:value="saveProfileName" placeholder="输入方案名称" :maxlength="80" />
        </div>

        <div v-if="loadedProfile && saveProfileMode === 'overwrite' && saveProfileOverwriteId === loadedProfile.id" class="save-hint">
          将覆盖当前已加载的方案「{{ loadedProfile.name }}」
        </div>
      </div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showSaveProfileDialog = false">取消</n-button>
          <n-button type="primary" :loading="savingProfile" @click="confirmSaveProfile">
            {{ saveProfileMode === 'overwrite' ? '覆盖保存' : '新建保存' }}
          </n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal
      v-model:show="showConnDialog"
      preset="card"
      :title="connDialogTitle"
      style="width: 480px"
      :bordered="false"
      :segmented="{ content: true, footer: 'soft' }"
    >
      <n-form label-placement="left" label-width="72" require-mark-placement="right-hanging">
        <n-form-item label="名称"><n-input v-model:value="connForm.name" /></n-form-item>
        <n-form-item label="主机"><n-input v-model:value="connForm.host" /></n-form-item>
        <n-form-item label="端口">
          <n-input-number v-model:value="connForm.port" :min="1" :max="65535" style="width: 100%" />
        </n-form-item>
        <n-form-item label="用户"><n-input v-model:value="connForm.user" /></n-form-item>
        <n-form-item label="密码">
          <n-input
            v-model:value="connForm.password"
            type="password"
            show-password-on="click"
            :placeholder="connForm.id ? '留空则不修改' : ''"
          />
        </n-form-item>
        <n-form-item label="默认库">
          <div class="db-select-row">
            <n-select
              v-model:value="connForm.database"
              :options="connFormDbOptions"
              placeholder="选择或输入库名"
              filterable
              tag
              clearable
              style="flex: 1"
            />
            <n-button :loading="loadingConnDatabases" @click="loadConnFormDatabases()">刷新</n-button>
          </div>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button :loading="testingConn" @click="testConnForm">测试连接</n-button>
          <n-button @click="showConnDialog = false">取消</n-button>
          <n-button type="primary" @click="saveConn">保存</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.other-differences { margin: 8px 0; padding-left: 20px; color: #496257; font-size: 12px; line-height: 1.8; }
.export-actions { display: flex; gap: 5px; flex-wrap: wrap; }
.statement-list { overflow-y: auto; flex: 1; min-height: 0; padding: 12px; }
.statement-card { border: 1px solid #cddfd5; border-radius: 7px; margin: 10px 0; padding: 10px; font-size: 12px; }
.statement-links { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin: 10px 0; }
.statement-heading { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px; }
.statement-heading code { overflow-wrap: anywhere; font-size: 12px; color: #496257; }
.statement-number, .association-label { font-size: 11px; color: #74867d; }
.change-link, .change-reference { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; border: 1px solid #d4e3db; border-radius: 5px; background: #f1f7f3; color: #226c52; font: inherit; font-size: 11px; text-align: left; overflow-wrap: anywhere; }
.change-link { cursor: pointer; }
.change-link:hover { background: #e1f1e8; border-color: #96c5ae; }
.change-link:focus-visible { outline: 2px solid #267c59; outline-offset: 2px; }
.table-operation { padding: 12px; border-radius: 7px; margin-bottom: 16px; font-size: 12px; line-height: 1.6; }
.table-operation p { margin: 6px 0; }
.table-operation span { font-size: 11px; }
.operation-drop { background: #fff2ee; color: #923a2c; border: 1px solid #efc7bd; }
.operation-add { background: #edf8f1; color: #276b4a; border: 1px solid #c4e4d1; }
.statement-card .sql { max-height: 360px; margin: 8px 0 0; padding: 12px; overflow: auto; }
.plan-target { font-size: 12px; background: #fff0ce; padding: 9px; border-radius: 6px; }

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

.opt-danger :deep(.n-checkbox__label) {
  color: #8a3b1d;
}
.progress-wrap {
  padding: 0 12px 10px;
}
.table-filter {
  padding: 8px 10px;
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
  table-layout: fixed;
  border-collapse: collapse;
  font-size: 12px;
}
.diff-table th {
  white-space: nowrap;
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
  width: 28%;
  white-space: normal;
  overflow-wrap: anywhere;
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
.mgr > :not(.mgr-tabs) {
  flex-shrink: 0;
}
.mgr-tabs {
  flex: 1;
  min-height: 0;
}
.mgr-tabs :deep(.n-tabs-nav) {
  flex-shrink: 0;
  padding: 0 18px;
  background: rgba(247, 249, 247, 0.95);
}
.mgr-tabs :deep(.n-tabs-pane-wrapper) {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
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

<style scoped src="./workbench.css"></style>
