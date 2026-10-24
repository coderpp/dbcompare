import { ref } from 'vue'
import * as SettingsService from '../bindings/github.com/pp/db-compare/internal/service/settingsservice'
import { message } from './ui'

export const settingsExporting = ref(false)
type Selection = { all?: boolean; connectionIds?: string[]; profileIds?: string[] }
export const pendingSettingsExport = ref<Selection | null>(null)
export function exportSettings(request: Selection) {
  if (!settingsExporting.value) pendingSettingsExport.value = request
}
export async function saveSettingsExport(includePasswords: boolean) {
  const request = pendingSettingsExport.value
  if (!request) return
  if (settingsExporting.value) return
  settingsExporting.value = true
  try {
    let file: any
    try {
      file = await SettingsService.SaveExport({ all: false, connectionIds: [], profileIds: [], ...request, includePasswords } as any)
      if (!file?.path) return // Native save dialog canceled.
      message.success(`已保存：${file.path}`)
    } catch (error) {
      if (!String(error).includes('不支持保存对话框')) throw error
      file = await SettingsService.Export({ all: false, connectionIds: [], profileIds: [], ...request, includePasswords } as any)
      if (!file) throw new Error('导出内容为空')
      const url = URL.createObjectURL(new Blob([file.content], { type: 'application/json;charset=utf-8' }))
      const link = document.createElement('a')
      link.href = url; link.download = file.filename; link.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      message.success(includePasswords ? '配置包已导出，含明文密码，请妥善保管' : '配置包已导出，不含密码')
    }
    if (file.warnings?.length) message.warning(file.warnings.join('；'), { duration: 8000 })
  } catch (error) { message.error(String(error)) } finally { settingsExporting.value = false; pendingSettingsExport.value = null }
}
