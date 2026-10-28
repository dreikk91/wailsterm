/**
 * window.api for the wailsterm Go backend.
 *
 * electerm's client talks to Electron through the preload bridge in
 * src/app/preload/preload.js. This module provides the same surface on top of
 * HTTP endpoints served by the Go process on the same origin:
 *   runSync        -> synchronous XHR  POST /api/sync
 *   runGlobalAsync -> fetch            POST /api/async
 *   ipcOnEvent     -> EventSource          /api/events
 * Locales ship with the UI bundle, so they are merged in here instead of in Go.
 */
import * as locales from '@electerm/electerm-locales'

const token = window.et.tokenElecterm
const headers = { 'Content-Type': 'application/json', 'X-Token': token }

const langs = Object.entries(locales)
  .filter(([id, l]) => id !== 'default' && l && l.lang)
  .map(([id, l]) => ({ id, name: l.name, reg: l.match, lang: l.lang }))
const langMap = Object.fromEntries(langs.map(l => [l.id, l]))

function runSyncRemote (name, args) {
  const xhr = new window.XMLHttpRequest()
  xhr.open('POST', '/api/sync', false)
  for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v)
  xhr.send(JSON.stringify({ name, args }))
  const res = JSON.parse(xhr.responseText || '{}')
  if (res.error) {
    console.error('runSync', name, res.error)
    return null
  }
  return res.data
}

const localSync = {
  getInitLocale: () => ({
    language: runSyncRemote('getLanguage', []) || 'en_us',
    langMap
  })
}

function runSync (name, ...args) {
  return localSync[name] ? localSync[name](...args) : runSyncRemote(name, args)
}

async function runGlobalAsync (name, ...args) {
  const r = await window.fetch('/api/async', {
    method: 'POST',
    headers,
    body: JSON.stringify({ name, args })
  })
  const res = await r.json()
  if (res.error) {
    throw new Error(res.error)
  }
  if (name === 'init') {
    return { ...res.data, langs, langMap }
  }
  return res.data
}

// ---- events: Go pushes {event, args} over SSE; focus/blur come from the page itself ----

const listeners = {}

function emit (event, args = []) {
  for (const cb of listeners[event] || []) {
    try {
      cb({ sender: null }, ...args)
    } catch (e) {
      console.error('ipc event handler', event, e)
    }
  }
}

const es = new window.EventSource('/api/events?token=' + encodeURIComponent(token))
es.onmessage = (e) => {
  const { event, args } = JSON.parse(e.data)
  emit(event, args)
}
window.addEventListener('focus', () => emit('focused'))
window.addEventListener('blur', () => emit('blur'))

let zoom = 1

window.api = {
  getZoomFactor: () => zoom,
  setZoomFactor: (z) => {
    zoom = z
    document.documentElement.style.zoom = String(z)
  },
  // A browser File carries no filesystem path, unlike Electron's webUtils.
  getPathForFile: () => null,
  openDialog: (opts) => runGlobalAsync('openDialog', opts),
  saveDialog: (opts) => runGlobalAsync('saveDialog', opts),
  ipcOnEvent: (event, cb) => {
    (listeners[event] = listeners[event] || []).push(cb)
  },
  ipcOffEvent: (event, cb) => {
    listeners[event] = (listeners[event] || []).filter(f => f !== cb)
  },
  runGlobalAsync,
  runSync,
  sendMcpResponse: () => {},
  onWebviewAuthRequest: () => () => {},
  sendWebviewAuthResponse: () => {}
}
