/**
 * electerm's lib/ai.js for the browser: same functions and return shapes, using
 * electerm's own ai-format.js / ai-response.js for the provider formats. HTTP goes
 * through the Go backend (/api/http), which adds proxy support and avoids CORS.
 */
import aiFormat from './electerm/ai-format.js'
import aiResponse from './electerm/ai-response.js'

const {
  detectFormat, headersForFormat, buildRequest, parseResponse, parseUsage, createStreamParser
} = aiFormat
const { errorMessageFromData } = aiResponse

const streamingSessions = new Map()
const pendingToolRequests = new Map()

function authHeaders (apiKey, authHeaderName, extra) {
  const parts = (authHeaderName || 'Authorization: Bearer').split(': ')
  const value = parts.length > 1 && parts[1] ? `${parts[1]} ${apiKey}` : apiKey
  return { 'Content-Type': 'application/json', [parts[0]]: value, ...(extra || {}) }
}

// axios combineURLs: a relative path is appended to baseURL
function joinURL (baseURL, path) {
  if (/^https?:\/\//i.test(path || '')) return path
  return String(baseURL || '').replace(/\/+$/, '') + '/' + String(path || '').replace(/^\/+/, '')
}

async function send ({ url, method = 'POST', headers, body, proxy, signal }) {
  const r = await window.fetch('/api/http', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Token': window.et.tokenElecterm },
    body: JSON.stringify({ url, method, headers, body: body === undefined ? '' : JSON.stringify(body), proxy }),
    signal
  })
  if (!r.ok) {
    throw new Error(await r.text())
  }
  return { status: Number(r.headers.get('X-Upstream-Status')), res: r }
}

async function readJSON (res) {
  const text = await res.text()
  try {
    return JSON.parse(text)
  } catch (e) {
    return text
  }
}

// like axios' rejection plus ai.js formatError: "Request failed with status code N: detail"
async function failure (status, res) {
  const detail = errorMessageFromData(await readJSON(res))
  const msg = `Request failed with status code ${status}`
  return detail ? `${msg}: ${detail}` : msg
}

function extractModelList (data) {
  const arr = Array.isArray(data) ? data : (data && (data.data || data.models)) || []
  const seen = new Set()
  const models = []
  for (const item of Array.isArray(arr) ? arr : []) {
    const id = typeof item === 'string' ? item : item && (item.id || item.name || item.model)
    if (id && !seen.has(id)) {
      seen.add(id)
      models.push(id)
    }
  }
  return models
}

export async function AIlistModels (baseURL, apiKey, authHeaderName, proxy) {
  try {
    const extra = /x-api-key/i.test(authHeaderName || '') ? { 'anthropic-version': '2023-06-01' } : null
    const { status, res } = await send({
      url: String(baseURL).replace(/\/+$/, '') + '/models',
      method: 'GET',
      headers: authHeaders(apiKey, authHeaderName, extra),
      proxy
    })
    if (status >= 400) return { error: await failure(status, res) }
    const models = extractModelList(await readJSON(res))
    return models.length ? { models } : { error: 'No models found in response' }
  } catch (e) {
    return { error: e.message }
  }
}

export async function AIchatWithTools (messages, model, baseURL, path, apiKey, proxy, tools, authHeaderName, format, requestId) {
  const controller = new AbortController()
  if (requestId) pendingToolRequests.set(requestId, controller)
  try {
    const fmt = detectFormat(path, format)
    const { status, res } = await send({
      url: joinURL(baseURL, path),
      headers: authHeaders(apiKey, authHeaderName, headersForFormat(fmt)),
      body: buildRequest(fmt, { model, messages, tools, stream: false }),
      proxy,
      signal: controller.signal
    })
    if (status >= 400) return { error: await failure(status, res) }
    const data = await readJSON(res)
    const { message, error } = parseResponse(fmt, data)
    if (error) return { error }
    return { message, usage: parseUsage(fmt, data) }
  } catch (e) {
    if (controller.signal.aborted) return { aborted: true }
    return { error: e.message }
  } finally {
    if (requestId) pendingToolRequests.delete(requestId)
  }
}

export async function AIchat (prompt, model, role, baseURL, path, apiKey, proxy, stream = true, authHeaderName, messages = null, format) {
  try {
    const fmt = detectFormat(path, format)
    const useStream = stream && !prompt.includes('give me max 5 command suggestions')
    const requestMessages = messages || [
      { role: 'system', content: role },
      { role: 'user', content: prompt }
    ]
    const controller = new AbortController()
    const { status, res } = await send({
      url: joinURL(baseURL, path),
      headers: authHeaders(apiKey, authHeaderName, headersForFormat(fmt)),
      body: buildRequest(fmt, { model, messages: requestMessages, stream: useStream }),
      proxy,
      signal: controller.signal
    })
    if (status >= 400) return { error: await failure(status, res) }
    if (!useStream) {
      const data = await readJSON(res)
      const { message, error } = parseResponse(fmt, data)
      if (error) return { error }
      return { response: message.content === undefined ? '' : message.content, isStream: false, usage: parseUsage(fmt, data) }
    }
    const sessionId = Date.now().toString() + Math.random().toString(36).slice(2, 11)
    const session = { content: '', completed: false, error: null, controller }
    streamingSessions.set(sessionId, session)
    pumpStream(res.body, session, createStreamParser(fmt))
    return { sessionId, isStream: true, hasMore: true, content: '' }
  } catch (e) {
    return { error: e.message, stack: e.stack }
  }
}

async function pumpStream (body, session, parser) {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  const sync = () => {
    session.content = parser.content
    if (parser.error) {
      session.error = parser.error
      session.completed = true
    } else if (parser.completed) {
      session.completed = true
    }
  }
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      parser.feed(decoder.decode(value, { stream: true }))
      sync()
    }
    parser.feed(decoder.decode())
    parser.flush()
    session.content = parser.content
    if (parser.error) session.error = parser.error
  } catch (e) {
    if (!session.stopped) session.error = e.message
  }
  session.completed = true
}

export function getStreamContent (sessionId) {
  const session = streamingSessions.get(sessionId)
  if (!session) return { error: 'Session not found' }
  const result = { content: session.content, hasMore: !session.completed, isStream: true }
  if (session.error) result.error = session.error
  if (session.completed || session.error) streamingSessions.delete(sessionId)
  return result
}

export function stopStream (sessionId) {
  const session = streamingSessions.get(sessionId)
  if (!session) return { error: 'Session not found' }
  session.stopped = true
  session.completed = true
  session.controller.abort()
  streamingSessions.delete(sessionId)
  return { stopped: true }
}

export function abortAIRequest (requestId) {
  const controller = pendingToolRequests.get(requestId)
  if (!controller) return { error: 'Request not found' }
  pendingToolRequests.delete(requestId)
  controller.abort()
  return { aborted: true }
}
