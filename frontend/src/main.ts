import { createApp, h } from 'vue'
import {
  NConfigProvider,
  NMessageProvider,
  NDialogProvider,
  lightTheme,
} from 'naive-ui'
import { themeOverrides } from './ui'
import './style.css'
import App from './App.vue'

const app = createApp({
  render() {
    return h(
      NConfigProvider,
      { theme: lightTheme, themeOverrides },
      {
        default: () =>
          h(NMessageProvider, null, {
            default: () =>
              h(NDialogProvider, null, {
                default: () => h(App),
              }),
          }),
      },
    )
  },
})

app.mount('#app')
