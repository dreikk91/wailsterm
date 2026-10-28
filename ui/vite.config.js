// Adapted from electerm src/build/vite/conf.js: same entries and output layout,
// plus the wailsterm shim entry that stands in for Electron's preload (window.api).
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { cpSync, readFileSync } from 'fs'
import { resolve } from 'path'

const root = import.meta.dirname
const { version } = JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8'))
const out = resolve(root, 'dist')

// electerm's build hardcodes the desktop (non web-app) code paths the same way.
// Exception: web tabs. WebView2 has no Electron <webview> tag, so web-session.jsx keeps its
// web-app branch, which renders the page in an <iframe>.
function replaceWebAppPlugin () {
  return {
    name: 'replace-webapp',
    transform (code, id) {
      if (id.replace(/\\/g, '/').endsWith('/components/web/web-session.jsx')) {
        return { code: code.replace(/window\.et\.isWebApp/g, 'true'), map: null }
      }
    },
    renderChunk (code) {
      const newCode = code.replace(/window\.et\.isWebApp/g, 'false')
      return newCode !== code ? { code: newCode, map: null } : null
    }
  }
}

// electerm serves these folders statically from node_modules; we copy them next to the bundle.
function copyStatic () {
  return {
    name: 'copy-static',
    closeBundle () {
      const nm = resolve(root, 'node_modules')
      cpSync(resolve(nm, 'electerm-icons/icons'), resolve(out, 'icons'), { recursive: true })
      cpSync(resolve(nm, '@electerm/electerm-resource/res/imgs'), resolve(out, 'images'), { recursive: true })
    }
  }
}

export default defineConfig({
  plugins: [
    react({ include: /\.(mdx|js|jsx|ts|tsx|mjs)$/ }),
    replaceWebAppPlugin(),
    copyStatic()
  ],
  resolve: {
    alias: {
      'ironrdp-wasm': resolve(root, 'node_modules/ironrdp-wasm/pkg/rdp_client.js'),
      'node:diagnostics_channel': resolve(root, 'diagnostics-channel-stub.js'),
      diagnostics_channel: resolve(root, 'diagnostics-channel-stub.js')
    }
  },
  optimizeDeps: { exclude: ['ironrdp-wasm'] },
  define: {
    'process.env.VER': JSON.stringify(version),
    __DEFINES__: JSON.stringify('some value')
  },
  publicDir: false,
  legacy: { inconsistentCjsInterop: true },
  root,
  build: {
    target: 'esnext',
    cssCodeSplit: false,
    codeSplitting: false,
    emptyOutDir: true,
    outDir: out,
    chunkSizeWarningLimit: 100000,
    rollupOptions: {
      input: {
        shim: resolve(root, 'shim/api.js'),
        electerm: resolve(root, 'client/entry/electerm.jsx'),
        basic: resolve(root, 'client/entry/basic.js'),
        worker: resolve(root, 'client/entry/worker.js')
      },
      output: {
        format: 'esm',
        entryFileNames: `js/[name]-${version}.js`,
        chunkFileNames: `chunk/[name]-${version}-[hash].js`,
        assetFileNames: ({ name = '' }) => {
          if (/\.(png|jpe?g|gif|svg|webp|ico|bmp)$/i.test(name)) return 'images/[name][extname]'
          if (name.endsWith('.css')) return `css/style-${version}[extname]`
          return 'assets/[name]-[hash][extname]'
        }
      }
    }
  }
})
