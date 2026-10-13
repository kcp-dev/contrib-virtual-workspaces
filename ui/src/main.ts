import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import '@fontsource-variable/instrument-sans'
import '@fontsource-variable/archivo'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/600.css'
import './style.css'

createApp(App).use(createPinia()).use(router).mount('#app')
