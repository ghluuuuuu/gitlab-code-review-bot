import { createApp } from 'vue'
import { VueQueryPlugin } from '@tanstack/vue-query'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import App from './App.vue'
import router from './router'

createApp(App).use(VueQueryPlugin).use(ElementPlus).use(router).mount('#app')
