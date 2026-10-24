import { h, ref } from 'vue'
import {
  createDiscreteApi,
  lightTheme,
  NInput,
  type GlobalThemeOverrides,
} from 'naive-ui'

export const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#0f6b5c',
    primaryColorHover: '#1a8573',
    primaryColorPressed: '#0b5649',
    primaryColorSuppl: '#2a9d84',
    successColor: '#1f7a4d',
    warningColor: '#b45309',
    errorColor: '#a33',
    borderRadius: '4px',
    fontFamily: '-apple-system, BlinkMacSystemFont, "PingFang SC", "Microsoft YaHei", sans-serif',
    fontFamilyMono: '"IBM Plex Mono", ui-monospace, Menlo, monospace',
  },
  Button: {
    borderRadiusMedium: '4px',
    heightMedium: '34px',
  },
  Input: {
    borderRadius: '4px',
    heightMedium: '34px',
  },
  Select: {
    peers: {
      InternalSelection: {
        borderRadius: '4px',
        heightMedium: '34px',
      },
    },
  },
  Checkbox: {
    borderRadius: '4px',
  },
  Dialog: {
    borderRadius: '14px',
  },
  Drawer: {
    borderRadius: '0',
  },
  Progress: {
    fillColor: '#0f6b5c',
  },
}

const { message, dialog } = createDiscreteApi(['message', 'dialog'], {
  configProviderProps: {
    theme: lightTheme,
    themeOverrides,
  },
})

export { message, dialog }

export function confirmBox(content: string, title = '确认'): Promise<boolean> {
  return new Promise((resolve) => {
    dialog.warning({
      title,
      content,
      positiveText: '确认',
      negativeText: '取消',
      onPositiveClick: () => resolve(true),
      onNegativeClick: () => resolve(false),
      onClose: () => resolve(false),
    })
  })
}

export function promptBox(title: string, initial = '', placeholder = ''): Promise<string | null> {
  const value = ref(initial)
  return new Promise((resolve) => {
    const d = dialog.create({
      title,
      content: () =>
        h(NInput, {
          value: value.value,
          placeholder,
          maxlength: 80,
          autofocus: true,
          onUpdateValue: (v: string) => {
            value.value = v
          },
          onKeyup: (e: KeyboardEvent) => {
            if (e.key === 'Enter') {
              const name = value.value.trim()
              if (!name) {
                message.warning('名称不能为空')
                return
              }
              d.destroy()
              resolve(name)
            }
          },
        }),
      positiveText: '确定',
      negativeText: '取消',
      onPositiveClick: () => {
        const name = value.value.trim()
        if (!name) {
          message.warning('名称不能为空')
          return false
        }
        resolve(name)
      },
      onNegativeClick: () => resolve(null),
      onClose: () => resolve(null),
    })
  })
}
