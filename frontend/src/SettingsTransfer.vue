<script setup lang="ts">
import { ref, watch } from 'vue'
import { NModal, NButton, NSpace, NCheckbox } from 'naive-ui'
import * as SettingsService from '../bindings/github.com/pp/db-compare/internal/service/settingsservice'
import { exportSettings, settingsExporting, pendingSettingsExport, saveSettingsExport } from './settingsTransfer'
import { message } from './ui'

defineProps<{ disabled?: boolean; empty?: boolean }>()
const emit = defineEmits(['imported'])
const includePasswords = ref(false)
watch(pendingSettingsExport, () => { includePasswords.value = false })
function cancelExport() { if (!settingsExporting.value) pendingSettingsExport.value = null }
const input = ref<HTMLInputElement | null>(null)
const busy = ref(false)
const show = ref(false)
const content = ref('')
const preview = ref<any>(null)
const filename = ref('配置文件')
function reset() { if (!busy.value) { show.value = false; preview.value = null; content.value = '' } }
async function previewContent(raw: string, name = '配置文件') {
  if (!raw) return
  preview.value = await SettingsService.PreviewImport(raw)
  content.value = raw; filename.value = name; show.value = true
}
async function chooseFile() {
  if (busy.value) return
  busy.value = true
  try { await previewContent(await SettingsService.ReadImportFile()) }
  catch (error) {
    if (String(error).includes('不支持打开对话框')) input.value?.click()
    else message.error(String(error))
  } finally { busy.value = false }
}
async function upload(event: Event) {
  const element = event.target as HTMLInputElement
  const file = element.files?.[0]
  element.value = ''
  if (!file) return
  if (!file.size) { message.error('配置文件为空'); return }
  if (file.size > 8 * 1024 * 1024) { message.error('配置文件不能超过 8 MB'); return }
  busy.value = true
  try { await previewContent(await file.text(), file.name) }
  catch (error) { message.error(String(error)) } finally { busy.value = false }
}
async function confirmImport() {
  if (busy.value || !preview.value) return
  busy.value = true
  try {
    const result = await SettingsService.Import(content.value)
    if (!result) throw new Error('导入结果为空')
    show.value = false; content.value = ''; preview.value = null
    emit('imported')
    const count = result.connections?.length || 0
    const credentials = result.passwordsImported || 0
    message.success(`已新增 ${count} 个连接、${result.profiles?.length || 0} 个方案；${credentials ? `已写入 ${credentials} 个密码` : '请编辑连接补充密码'}`, { duration: 6000 })
    if (result.warnings?.length) message.warning(result.warnings.join('；'), { duration: 10000 })
  } catch (error) { message.error(String(error)) } finally { busy.value = false }
}
function connectionName(id: string) { return preview.value?.bundle.connections.find((c: any) => c.id === id)?.name || '缺少连接' }
</script>

<template>
  <div class="settings-transfer">
    <div class="transfer-buttons">
      <button type="button" class="transfer-button" :disabled="disabled || busy || settingsExporting" @click="chooseFile">{{ busy ? '读取中…' : '导入配置' }}</button>
      <button type="button" class="transfer-button" :disabled="disabled || empty || busy || settingsExporting" @click="exportSettings({ all: true })">{{ settingsExporting ? '导出中…' : '导出全部' }}</button>
    </div>
    <p>连接与方案一起迁移 · 导出时可选是否包含密码</p>
    <input ref="input" type="file" accept=".json,application/json" hidden aria-label="选择连接与方案配置包" @change="upload" />
  </div>
  <n-modal :show="!!pendingSettingsExport" preset="card" title="导出连接与方案" style="width: 480px; max-width: 92vw" :mask-closable="!settingsExporting" :close-on-esc="!settingsExporting" :closable="!settingsExporting" @update:show="cancelExport">
    <p class="import-summary">{{ pendingSettingsExport?.all ? '导出全部连接与方案。' : pendingSettingsExport?.profileIds?.length ? '导出所选方案及关联连接。' : '导出所选连接。' }}</p>
    <n-checkbox v-model:checked="includePasswords" :disabled="settingsExporting">包含已保存的连接密码</n-checkbox>
    <p v-if="includePasswords" class="password-warning">导出的 JSON 文件将包含明文密码，任何拿到文件的人都可以读取。请仅通过可信方式传递，使用后妥善处理。</p>
    <p v-else class="import-summary export-hint">默认不导出密码，目标机需重新填写。</p>
    <template #footer><n-space justify="end"><n-button :disabled="settingsExporting" @click="cancelExport">取消</n-button><n-button type="primary" :loading="settingsExporting" :disabled="disabled" @click="saveSettingsExport(includePasswords)">{{ includePasswords ? '导出（含密码）' : '导出' }}</n-button></n-space></template>
  </n-modal>
  <n-modal :show="show" preset="card" title="导入连接与方案" style="width: 640px; max-width: 92vw" :mask-closable="!busy" :close-on-esc="!busy" :closable="!busy" @update:show="reset">
    <template v-if="preview">
      <p class="import-summary">{{ filename }} · {{ preview.bundle.connections.length }} 个连接 / {{ preview.bundle.profiles.length }} 个方案</p>
      <p class="import-notice">全部作为新配置导入，不覆盖现有内容。同名项自动添加“导入”后缀；方案会关联到本次导入的新连接。未携带密码的连接需重新填写密码。</p>
      <p v-if="preview.passwordConnectionIds?.length" class="password-warning">此文件携带 {{ preview.passwordConnectionIds.length }} 个连接密码，确认后写入本机系统凭据存储。预览不显示密码内容。</p>
      <div class="import-list">
        <h4>连接</h4>
        <div v-for="c in preview.bundle.connections" :key="c.id" class="import-row">
          <div><b>{{ c.name }}</b><span v-if="preview.passwordConnectionIds?.includes(c.id)" class="conflict-label">含密码</span><span v-if="preview.conflictingConnections.includes(c.id)" class="conflict-label">同名，自动另存</span></div>
          <code>{{ c.user }}@{{ c.host }}:{{ c.port }}<template v-if="c.database"> / {{ c.database }}</template></code>
        </div>
        <h4 v-if="preview.bundle.profiles.length">方案</h4>
        <div v-for="p in preview.bundle.profiles" :key="p.id" class="import-row">
          <div><b>{{ p.name }}</b><span v-if="preview.conflictingProfiles.includes(p.id)" class="conflict-label">同名，自动另存</span></div>
          <span>{{ connectionName(p.sourceId) }} / {{ p.sourceDatabase }} → {{ connectionName(p.targetId) }} / {{ p.targetDatabase }}</span>
        </div>
      </div>
      <p v-for="(warning, index) in preview.warnings" :key="index" class="import-warning">{{ warning }}</p>
    </template>
    <template #footer><n-space justify="end"><n-button :disabled="busy" @click="reset">取消</n-button><n-button type="primary" :loading="busy" :disabled="disabled || !preview" @click="confirmImport">确认导入</n-button></n-space></template>
  </n-modal>
</template>

<style scoped>
.settings-transfer { padding: 0 16px 14px; border-bottom: 1px solid #dfe7e3; background: #fff; }
.transfer-buttons { display: flex; gap: 8px; }
.transfer-button { padding: 7px 12px; border: 1px solid #cfded8; border-radius: 4px; background: #fff; color: #247267; cursor: pointer; font: inherit; font-size: 12px; }
.transfer-button:disabled { opacity: .45; cursor: not-allowed; }
.settings-transfer p { font-size: 11px; color: #85928d; margin: 8px 0 0; }
.import-summary { color: #65768a; font-size: 12px; margin: 0 0 10px; }
.import-notice { padding: 10px 12px; color: #476c60; background: #f0f8f4; border-radius: 4px; font-size: 12px; line-height: 1.7; }
.import-list { max-height: 42vh; overflow-y: auto; }
h4 { margin: 12px 0 4px; font-size: 12px; color: #526575; }
.import-row { padding: 9px 0; border-bottom: 1px solid #edf0f2; font-size: 12px; overflow-wrap: anywhere; }
.import-row b { font-weight: 500; }
.import-row code, .import-row > span { display: block; margin-top: 4px; font-size: 11px; color: #7a8898; }
.conflict-label { margin-left: 8px; color: #a47b3a; font-size: 10px; }
.password-warning { margin: 12px 0; padding: 10px 12px; border-radius: 4px; background: #fff4e8; color: #a35c28; font-size: 12px; line-height: 1.7; }
.export-hint { margin-top: 12px; }
.import-warning { font-size: 12px; color: #a47b3a; }
</style>
