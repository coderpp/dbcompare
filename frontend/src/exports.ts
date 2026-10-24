import * as SyncService from '../bindings/github.com/pp/db-compare/internal/service/syncservice'
import { message } from './ui'

export async function saveArtifact(request: any) {
  try {
    const path = await SyncService.SaveExport(request)
    if (path) message.success(`已保存：${path}`)
  } catch (error: any) {
    // Server/browser environments download the same source-backed artifact.
    if (!String(error).includes('不支持保存对话框')) { message.error(String(error)); return }
    try {
      const file = await SyncService.Export(request)
      if (!file) throw new Error('导出内容为空')
      const url = URL.createObjectURL(new Blob([file.content], { type: 'text/plain;charset=utf-8' }))
      const link = document.createElement('a')
      link.href = url; link.download = file.filename; link.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (error) { message.error(String(error)) }
  }
}
