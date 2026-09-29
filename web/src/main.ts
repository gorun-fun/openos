import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './sdk' // 注入 window.OpenOS 第三方应用 SDK
import './styles/base.css'

const app = createApp(App)
app.use(createPinia())
app.mount('#app')
