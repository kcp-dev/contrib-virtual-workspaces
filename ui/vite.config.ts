import { fileURLToPath, URL } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// The UI talks to kcp's front-proxy and nothing else: every API it needs
// — the virtual workspaces under /services/, the workspaces themselves
// under /clusters/ — answers on one address.
//
// In development that address is a different origin than the dev server,
// and kcp sends no CORS headers, so the dev server proxies those two
// prefixes instead. The browser therefore only ever talks to its own
// origin, which is also how the UI is served in a deployment: behind the
// same front-proxy, where the question does not arise.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const target = env.VITE_KCP_SERVER || 'https://kcp.localhost:8443'

  const proxy = {
    target,
    changeOrigin: true,
    // A development front-proxy serves a certificate signed by the
    // cluster's own CA, which node does not trust.
    secure: env.VITE_KCP_INSECURE === 'false',
  }

  return {
    plugins: [vue(), tailwindcss()],
    resolve: {
      alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
    },
    server: {
      port: 5173,
      proxy: {
        '/services': proxy,
        '/clusters': proxy,
      },
    },
  }
})
