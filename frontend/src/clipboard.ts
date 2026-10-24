import { Clipboard } from '@wailsio/runtime'
import { message } from './ui'

// Keep every copy button on the same path, including desktop WebViews.
export async function copyText(text: string) {
  if (!text) { message.warning('暂无可复制的内容'); return }
  try {
    const host = window as any
    const desktop = host.webkit?.messageHandlers?.external || host.chrome?.webview || host._wails?.environment?.OS === 'linux'
    if (desktop) {
      try {
        await Clipboard.SetText(text)
        if (await Clipboard.Text() !== text) throw new Error('剪贴板写入未生效')
        message.success('已复制到剪贴板')
        return
      } catch { /* Fall through to browser APIs. */ }
    }
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      const active = document.activeElement as HTMLElement | null
      const selection = window.getSelection()
      const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, i) => selection.getRangeAt(i).cloneRange()) : []
      const field = document.createElement('textarea')
      field.value = text
      field.readOnly = true
      field.style.cssText = 'position:fixed;left:0;top:0;opacity:0;pointer-events:none'
      // Stay inside the active modal/drawer focus trap.
      ;(active?.closest('[role="dialog"]') || document.body).appendChild(field)
      try {
        field.focus(); field.select()
        if (!document.execCommand('copy')) throw new Error('剪贴板不可用')
      } finally {
        field.remove(); active?.focus({ preventScroll: true })
        if (selection) { selection.removeAllRanges(); ranges.forEach(r => selection.addRange(r)) }
      }
    }
    message.success('已复制到剪贴板')
  } catch {
    message.error('复制失败，请选中 SQL 手动复制，或导出 SQL 文件', { duration: 6000 })
  }
}
