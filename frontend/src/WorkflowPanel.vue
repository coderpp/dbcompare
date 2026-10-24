<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { NDrawer, NDrawerContent, NCheckbox, NSpin } from 'naive-ui'
import * as SyncService from '../bindings/github.com/pp/db-compare/internal/service/syncservice'
import { saveArtifact } from './exports'
import { message } from './ui'
import { copyText } from './clipboard'

const props = defineProps<{ show: boolean; sessionId: string; report?: any; disabled?: boolean; initialTab?: 'report' | 'history' | 'encoding'; refreshToken?: number }>()
const emit = defineEmits(['update:show', 'repair-plan', 'resume'])
const tab = ref('report')
const busy = ref(false)
const runs = ref<any[]>([])
const report = ref<any>(null)
const legacy = ref<any[]>([])
const side = ref('target')
const candidates = ref<any[]>([])
const repairIds = ref<string[]>([])
const scanned = ref(false)
const filter = ref('')
const displayedCandidates = computed(() => candidates.value.filter(x => `${x.table}.${x.column}`.toLowerCase().includes(filter.value.toLowerCase())))
const labels: Record<string, string> = { success: '成功', failed: '失败', pending: '待执行', running: '执行中（若已中断请重新规划）', not_executed: '未执行', incomplete: '所选变更仍有差异', verification_failed: '执行结束，复查失败', report_error: '报告保存异常', selected: '本次所选，尚未完成', unselected: '其他未选差异', skipped_drop: '按选项保留的目标多余对象' }
const label = (s: string) => labels[s] || s
watch(() => props.report, value => { if (value) { report.value = value } }, { immediate: true })
watch(() => props.show, value => { if (value) { tab.value = props.initialTab || 'report'; void refresh() } })
watch(() => props.initialTab, value => { if (props.show && value) tab.value = value })
watch([() => props.sessionId, side], () => { candidates.value = []; repairIds.value = []; scanned.value = false })
watch(() => props.refreshToken, () => { if (props.show && tab.value === 'encoding' && props.sessionId) void scan() })
async function refresh() {
  try { runs.value = await SyncService.ListRuns(100) || [] } catch (e) { message.error(String(e)) }
}
async function openRun(id: string) {
  busy.value = true
  try { report.value = await SyncService.GetRun(id); tab.value = 'report' } catch (e) { message.error(String(e)) } finally { busy.value = false }
}
async function resume() {
  if (!report.value || props.disabled) return
  busy.value = true
  try {
    const res = await SyncService.Resume(report.value.id)
    if (!res) throw new Error('重新对比结果为空')
    emit('resume', { ...res, run: report.value })
    emit('update:show', false)
    message.success('已重新读取两库，并勾选原任务仍有差异的表；请审阅并重新生成 SQL')
  } catch (e) { message.error(String(e)) } finally { busy.value = false }
}
async function scan() {
  if (!props.sessionId) return
  busy.value = true; scanned.value = false; repairIds.value = []
  const session = props.sessionId, selectedSide = side.value
  try {
    const result = await SyncService.DiagnoseComments(session, selectedSide) || []
    if (session !== props.sessionId || selectedSide !== side.value) return
    candidates.value = result; scanned.value = true
  } catch (e) { message.error(String(e)) } finally { busy.value = false }
}
async function planRepair(direct = false) {
  if (props.disabled || !repairIds.value.length) return
  busy.value = true
  try {
    const session = props.sessionId
    const selectedSide = side.value
    const plan = await SyncService.PlanCommentRepair({ sessionId: session, side: selectedSide, ids: repairIds.value } as any)
    if (session !== props.sessionId || selectedSide !== side.value) return
    if (!plan) throw new Error('修复计划为空')
    emit('repair-plan', plan, direct, selectedSide)
    if (!direct) emit('update:show', false)
    message.success(direct ? '请核对修复 SQL 和实际数据库，确认后执行' : '已生成注释修复 SQL，尚未执行；请核对实际目标库')
  } catch (e) { message.error(String(e)) } finally { busy.value = false }
}
function setRepair(id: string, checked: boolean) { repairIds.value = checked ? [...new Set([...repairIds.value, id])] : repairIds.value.filter(x => x !== id) }
async function loadLegacy() {
  try { legacy.value = await SyncService.ListAudit(100) || [] } catch (e) { message.error(String(e)) }
}
</script>

<template>
  <n-drawer :show="show" :width="960" @update:show="emit('update:show', $event)">
    <n-drawer-content title="同步报告、执行历史与注释修复" closable>
      <div class="workflow">
        <nav>
          <button :class="{ active: tab === 'report' }" @click="tab = 'report'">结果报告</button>
          <button :class="{ active: tab === 'history' }" @click="tab = 'history'; refresh()">执行历史</button>
          <button :class="{ active: tab === 'encoding' }" @click="tab = 'encoding'">乱码检测与修复</button>
          <n-spin v-if="busy" :size="18" />
        </nav>
        <section v-if="tab === 'report'">
          <p v-if="!report" class="empty">执行同步后显示完整报告，也可从执行历史打开。</p>
          <template v-else>
            <h2>{{ label(report.status) }}</h2>
            <p><b>{{ report.target.name }}</b> / <code>{{ report.target.database }}</code> · {{ report.mode === 'repair' ? '注释修复' : '结构同步' }}</p>
            <p class="muted">{{ report.startedAt }} → {{ report.finishedAt || '尚无完成记录；如应用曾退出，请重新读取结构后规划' }}</p>
            <div class="summary">
              <div><strong>{{ report.applied }}</strong>成功</div>
              <div><strong>{{ report.failed }}</strong>失败</div>
              <div><strong>{{ (report.outcomes || []).filter((x: any) => ['pending', 'not_executed'].includes(x.status)).length }}</strong>未执行</div>
              <div><strong>{{ (report.verificationError || !report.finishedAt) ? '未知' : report.selectionComplete ? '已完成' : '未完成' }}</strong>本次所选变更</div>
            </div>
            <p v-if="report.error" class="error">{{ report.error }}</p>
            <p v-if="report.verificationError" class="error">复查失败：{{ report.verificationError }}。执行状态与复查状态应分别判断。</p>
            <div class="actions">
              <button :disabled="busy || disabled" @click="resume">重新读取结构，规划剩余项</button>
              <button @click="saveArtifact({ runId: report.id, kind: 'report' })">导出报告 JSON</button>
              <button @click="saveArtifact({ runId: report.id, kind: 'sql' })">导出原计划 SQL</button>
              <button @click="saveArtifact({ runId: report.id, kind: 'snapshot' })">导出同步前结构</button>
              <button @click="saveArtifact({ runId: report.id, kind: 'reverse' })">导出恢复参考 SQL</button>
            </div>
            <p class="muted">继续处理会重新对比并重新生成计划，不直接重放历史 SQL。结构快照不含数据，不能恢复已删除的数据。</p>
            <h3>本次选择 · {{ (report.selected || []).length }} 项</h3>
            <div class="chips"><span v-for="item in report.selected" :key="item.id"><code>{{ item.table }}</code> · {{ item.summary }}</span></div>
            <h3>逐条执行结果</h3>
            <details v-for="o in report.outcomes" :key="o.statement.id" class="outcome">
              <summary><b :class="{ error: o.status === 'failed' }">{{ label(o.status) }}</b> · {{ o.statement.table }} · {{ o.statement.comment }} · {{ o.durationMs }} ms</summary>
              <p v-if="o.error" class="error">{{ o.error }}</p>
              <p class="muted">关联差异：{{ o.statement.changeIds?.join('、') }}</p>
              <button @click="copyText(o.statement.sql)">复制 SQL</button><pre>{{ o.statement.sql }}</pre>
            </details>
            <h3>{{ report.mode === 'repair' ? '所选注释修复剩余项' : '全库剩余差异' }} · {{ (report.remaining || []).length }} 项</h3>
            <p v-if="!(report.remaining || []).length">{{ (report.verificationError || !report.finishedAt) ? '复查未完成，不能据此判断一致。' : '未发现剩余差异。' }}</p>
            <table v-else><thead><tr><th>分类</th><th>表</th><th>对象</th><th>差异</th></tr></thead><tbody>
              <tr v-for="(d, i) in report.remaining" :key="i"><td>{{ label(d.category) }}</td><td><code>{{ d.item.table }}</code></td><td>{{ d.item.objectName }}</td><td>{{ d.item.summary }}</td></tr>
            </tbody></table>
            <h3>同步前结构快照 · {{ (report.snapshots || []).length }} 张表</h3>
            <details v-for="sn in report.snapshots" :key="sn.table"><summary>{{ sn.table }} · {{ sn.exists ? '已保存原结构' : '同步前不存在' }}</summary><p class="muted">SHA256 {{ sn.sha256 }}</p><pre>{{ sn.ddl || '同步前无此表' }}</pre></details>
          </template>
        </section>
        <section v-if="tab === 'history'">
          <div class="actions"><button @click="refresh">刷新历史</button><button @click="loadLegacy">查看旧版逐条日志</button></div>
          <p class="muted">报告、执行状态及结构快照保存在本机，重启后仍可查看。未完成的历史需重新读取数据库确认状态。</p>
          <p v-if="!runs.length">暂无新版同步任务。</p>
          <button v-for="run in runs" :key="run.id" class="history-row" @click="openRun(run.id)"><span>{{ run.startedAt }} · {{ label(run.status) }}</span><b>{{ run.target.name }} / {{ run.target.database }}</b><span>成功 {{ run.applied }} / 失败 {{ run.failed }}</span></button>
          <details v-for="entry in legacy" :key="entry.id"><summary>{{ entry.at }} · {{ entry.ok ? '成功' : '失败' }} · {{ entry.durationMs }} ms</summary><p class="error">{{ entry.error }}</p><button @click="copyText(entry.sql)">复制 SQL</button><pre>{{ entry.sql }}</pre></details>
        </section>
        <section v-if="tab === 'encoding'">
          <p>检测字段和表注释中可逆的 UTF-8 / Latin1 编码错读，逐项预览中文恢复结果。</p>
          <p class="muted">仅覆盖可严格还原且增加中文字符的内容；未发现候选不代表所有注释都正常。无法还原或已经丢失的字符需人工核对。</p>
          <div class="actions">
            <label><input v-model="side" type="radio" value="source" :disabled="busy || disabled" />源库（期望结构）</label>
            <label><input v-model="side" type="radio" value="target" :disabled="busy || disabled" />目标库（将被修改）</label>
            <button :disabled="!sessionId || busy || disabled" @click="scan">扫描乱码注释</button>
          </div>
          <p v-if="!sessionId">请先开始一次结构对比，以确定源库和目标库。</p>
          <p v-if="side === 'source'" class="notice">当前选的是源库：生成的修复计划将修改源库注释，请核对 SQL 预览中的实际数据库。</p>
          <template v-if="scanned">
            <div class="actions"><input v-model="filter" placeholder="筛选表名或字段名" /><button :disabled="busy || disabled" @click="repairIds = displayedCandidates.map(x => x.id)">选择筛选结果</button><button :disabled="busy || disabled" @click="repairIds = []">清空选择</button></div>
            <p>检测到 {{ candidates.length }} 项，已选 {{ repairIds.length }} 项。</p>
            <table><thead><tr><th>选择 / 对象</th><th>原注释</th><th>建议恢复</th></tr></thead><tbody><tr v-for="c in displayedCandidates" :key="c.id"><td><n-checkbox :disabled="busy || disabled" :checked="repairIds.includes(c.id)" @update:checked="setRepair(c.id, $event)" /><code>{{ c.table }}.{{ c.column || '(表注释)' }}</code></td><td class="broken">{{ c.original }}</td><td>{{ c.recovered }}</td></tr></tbody></table>
            <button :disabled="!repairIds.length || busy || disabled" @click="planRepair()">生成所选修复 SQL（不执行）</button>
            <button :disabled="!repairIds.length || busy || disabled" @click="planRepair(true)">直接执行所选修复</button>
          </template>
        </section>
      </div>
    </n-drawer-content>
  </n-drawer>
</template>

<style scoped>
.workflow { color: #263d35; font-size: 13px; line-height: 1.6; }
nav,.actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin: 12px 0; }
nav { border-bottom: 1px solid #dbe5df; padding-bottom: 12px; }
button { border: 1px solid #c9d9d1; background: #fff; color: #246650; border-radius: 6px; padding: 7px 12px; cursor: pointer; }
button:disabled { opacity: .45; cursor: default; }
button.active { background: #d8eee5; font-weight: 600; }
h2 { font-size: 22px; margin: 14px 0; } h3 { font-size: 14px; margin-top: 24px; }
.summary { display: grid; grid-template-columns: repeat(4,1fr); gap: 10px; padding: 14px; background: #f0f6f3; border-radius: 8px; }
.summary div { display: flex; flex-direction: column; } strong { font-size: 20px; }
.muted { color: #6a7f75; font-size: 12px; overflow-wrap: anywhere; } .error { color: #a63627; overflow-wrap: anywhere; }
.notice { background: #fff3d6; padding: 10px; border-radius: 6px; }
table { width: 100%; border-collapse: collapse; margin: 12px 0; table-layout: fixed; }
th,td { text-align: left; padding: 9px; vertical-align: top; border-bottom: 1px solid #e0e8e3; overflow-wrap: anywhere; }
th { background: #f2f6f4; } td code { font-size: 11px; }
pre { background: #f2f6f4; padding: 12px; border-radius: 5px; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 380px; overflow-y: auto; font-size: 12px; }
details { padding: 10px 0; border-bottom: 1px solid #e1e9e5; } summary { cursor: pointer; overflow-wrap: anywhere; }
.history-row { display: flex; flex-direction: column; width: 100%; align-items: flex-start; margin: 10px 0; gap: 4px; text-align: left; }
.chips { display: flex; flex-wrap: wrap; gap: 5px; max-height: 160px; overflow-y: auto; }.chips span { padding: 4px 7px; background: #edf4f0; border-radius: 5px; }
.broken { color: #a15c2e; } input { padding: 6px; max-width: 100%; }.empty { margin: 40px 0; color: #6a7f75; }
</style>
