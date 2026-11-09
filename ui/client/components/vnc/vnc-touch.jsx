/**
 * Touch control for a VNC session, modelled on RealVNC Viewer:
 *
 *   1 finger drag        moves the remote cursor relatively (touchpad)
 *   tap                  left click at the cursor
 *   hold, then drag      left button drag
 *   2 finger tap         right click
 *   2 finger drag        wheel scroll
 *   pinch                zoom, the zoomed desktop pans when the cursor is pushed past an edge
 *   3 finger tap         toggle the soft keyboard
 *
 * Touches are taken in the capture phase on the mount, so noVNC's own
 * direct-touch GestureHandler on the canvas never sees them; the result is
 * replayed to noVNC as synthetic mouse/wheel events on its canvas, which also
 * drives noVNC's drawn cursor.
 *
 * The soft keyboard types into a hidden textarea (diffed, so IME composition
 * and autocorrect work), shows a row of extra keys with sticky modifiers above
 * it, and shifts the desktop up by the space the keyboard takes.
 */
import { PureComponent, createRef } from 'react'
import './vnc-touch.styl'

// ponytail: fixed tuning, make these settings if users ask
const SPEED = 1.5 // cursor px per finger px
const TAP_MS = 300
const HOLD_MS = 500
const DOUBLE_MS = 250 // after a tap, wait this long for a second finger (drag / double click)
const MOVE_SLOP = 8
const PINCH_SLOP = 30
const SCROLL_SLOP = 12
const MAX_ZOOM = 5
const BAR_H = 44
const PAD = '  ' // pre-filled text, so backspace in an "empty" field still reaches us

const K = {
  Esc: 0xff1b,
  Tab: 0xff09,
  BackSpace: 0xff08,
  Return: 0xff0d,
  Left: 0xff51,
  Up: 0xff52,
  Right: 0xff53,
  Down: 0xff54,
  Home: 0xff50,
  End: 0xff57,
  PgUp: 0xff55,
  PgDn: 0xff56,
  Ins: 0xff63,
  Del: 0xffff
}
const MODS = [
  ['Ctrl', 0xffe3],
  ['Alt', 0xffe9],
  ['Shift', 0xffe1],
  ['Win', 0xffeb]
]
const KEYS = [
  ['Esc', K.Esc], ['Tab', K.Tab],
  ['←', K.Left], ['↑', K.Up], ['↓', K.Down], ['→', K.Right],
  ['Home', K.Home], ['End', K.End], ['PgUp', K.PgUp], ['PgDn', K.PgDn],
  ['Ins', K.Ins], ['Del', K.Del],
  ...Array.from({ length: 12 }, (_, i) => ['F' + (i + 1), 0xffbe + i])
]
// hardware keyboard keys that never reach the textarea as text
const HW_KEYS = {
  Escape: K.Esc,
  Tab: K.Tab,
  ArrowLeft: K.Left,
  ArrowUp: K.Up,
  ArrowRight: K.Right,
  ArrowDown: K.Down,
  Home: K.Home,
  End: K.End,
  PageUp: K.PgUp,
  PageDown: K.PgDn,
  Insert: K.Ins,
  Delete: K.Del
}

// X11 keysym for a character: Latin-1 maps directly, the rest uses the Unicode range
export function charKeysym (ch) {
  const c = ch.codePointAt(0)
  if (ch === '\n') return K.Return
  if ((c >= 0x20 && c <= 0x7e) || (c >= 0xa0 && c <= 0xff)) return c
  return 0x01000000 | c
}

// Is this cursor image a text (I-beam) cursor? VNC has no word on what has
// focus, but over a text field the server shows an I-beam. Looks at the opaque
// silhouette: narrow, tall, and already near full width on its first row (the
// serif), where a vertical resize arrow starts from a thin tip.
export function isTextCursor (rgba, w, h) {
  const rows = []
  for (let y = 0; y < h; y++) {
    let l = w
    let r = -1
    for (let x = 0; x < w; x++) {
      if (rgba[(y * w + x) * 4 + 3] > 0) {
        if (x < l) l = x
        r = x
      }
    }
    if (r >= 0) rows.push(r - l + 1)
  }
  if (rows.length < 12) return false
  const max = Math.max(...rows)
  return max <= 11 && rows.length >= 2 * max && rows[0] >= 0.6 * max
}

// what turns `prev` into `now`: `back` backspaces, then type `add` (code points)
export function textDiff (prev, now) {
  const a = Array.from(prev)
  const b = Array.from(now)
  let i = 0
  while (i < a.length && i < b.length && a[i] === b[i]) i++
  return { back: a.length - i, add: b.slice(i) }
}

const clamp =(v, lo, hi) => Math.min(Math.max(v, lo), hi)
const dist = (a, b) => Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY)
const mid = (a, b) => ({ x: (a.clientX + b.clientX) / 2, y: (a.clientY + b.clientY) / 2 })

export const isTouchDevice = () => navigator.maxTouchPoints > 0

export default class VncTouch extends PureComponent {
  state = {
    kb: false,
    barTop: 0,
    mods: []
  }

  taRef = createRef()
  cx = null
  cy = null
  buttons = 0
  g = null // current gesture
  shift = 0 // how far the desktop is lifted for the keyboard
  maxShift = 0

  componentDidMount () {
    const el = this.mount()
    const opts = { capture: true, passive: false }
    for (const t of ['touchstart', 'touchmove', 'touchend', 'touchcancel']) {
      el.addEventListener(t, this.onTouch, opts)
    }
    window.visualViewport?.addEventListener('resize', this.onViewport)
    window.visualViewport?.addEventListener('scroll', this.onViewport)
  }

  componentWillUnmount () {
    const el = this.mount()
    const opts = { capture: true, passive: false }
    for (const t of ['touchstart', 'touchmove', 'touchend', 'touchcancel']) {
      el?.removeEventListener(t, this.onTouch, opts)
    }
    window.visualViewport?.removeEventListener('resize', this.onViewport)
    window.visualViewport?.removeEventListener('scroll', this.onViewport)
    clearTimeout(this.holdTimer)
    clearTimeout(this.pendingTap)
  }

  rfb = () => this.props.session.rfb
  mount = () => this.props.session.domRef.current
  screen = () => this.mount()?.firstElementChild

  // ---- mouse ----

  // part of the desktop the user can actually see: noVNC's screen box, clipped
  // by the session viewport and by the key bar when the keyboard is up
  visibleRect () {
    const r = this.screen().getBoundingClientRect()
    const p = this.mount().parentElement.getBoundingClientRect()
    const bottom = this.state.kb ? this.state.barTop : window.innerHeight
    return {
      left: Math.max(r.left, p.left),
      right: Math.min(r.right, p.right),
      top: Math.max(r.top, p.top),
      bottom: Math.min(r.bottom, p.bottom, bottom)
    }
  }

  clampCursor () {
    const v = this.visibleRect()
    const c = this.rfb()._canvas.getBoundingClientRect()
    if (this.cx === null) {
      this.cx = (Math.max(v.left, c.left) + Math.min(v.right, c.right)) / 2
      this.cy = (Math.max(v.top, c.top) + Math.min(v.bottom, c.bottom)) / 2
    }
    this.cx = clamp(this.cx, Math.max(v.left, c.left), Math.min(v.right, c.right) - 1)
    this.cy = clamp(this.cy, Math.max(v.top, c.top), Math.min(v.bottom, c.bottom) - 1)
  }

  moveCursor (dx, dy) {
    this.clampCursor()
    const x = this.cx + dx
    let y = this.cy + dy
    if (this.state.kb) {
      // with the keyboard up the desktop slides up and down after the cursor
      const v = this.visibleRect()
      const over = y > v.bottom ? y - v.bottom : y < v.top ? y - v.top : 0
      const shift = clamp(this.shift + over, 0, this.maxShift)
      y -= shift - this.shift
      this.setShift(shift)
    }
    const s = this.screen()
    const v = this.visibleRect()
    // pushing past an edge pans a zoomed desktop instead
    if (x > v.right) s.scrollLeft += x - v.right
    else if (x < v.left) s.scrollLeft -= v.left - x
    if (y > v.bottom) s.scrollTop += y - v.bottom
    else if (y < v.top) s.scrollTop -= v.top - y
    this.cx = x
    this.cy = y
    this.clampCursor()
    this.mouse('mousemove')
  }

  mouse (type, buttons = this.buttons) {
    this.buttons = buttons
    // noVNC covers the page with a capture layer on mousedown and drops it only
    // when a mouseup reaches window, yet it stops the one sent to its canvas.
    // A left-over layer swallowed the next touch as a direct (touchscreen) click.
    const target = type === 'mouseup' && document.captureElement ? window : this.rfb()._canvas
    target.dispatchEvent(new MouseEvent(type, {
      clientX: this.cx,
      clientY: this.cy,
      buttons,
      button: buttons === 2 ? 2 : 0,
      bubbles: true,
      cancelable: true,
      view: window
    }))
  }

  click (buttons) {
    this.clampCursor()
    this.mouse('mousedown', buttons)
    this.mouse('mouseup', 0)
    this.releaseMods()
  }

  wheel (dx, dy) {
    this.clampCursor()
    this.rfb()._canvas.dispatchEvent(new WheelEvent('wheel', {
      clientX: this.cx,
      clientY: this.cy,
      deltaX: dx,
      deltaY: dy,
      deltaMode: 0,
      bubbles: true,
      cancelable: true
    }))
  }

  zoom (z, center) {
    const { session } = this.props
    const rfb = this.rfb()
    const canvas = rfb._canvas
    const s = this.screen()
    const before = canvas.getBoundingClientRect()
    const fx = (center.x - before.left) / before.width
    const fy = (center.y - before.top) / before.height
    session.zoom = clamp(z, 1, MAX_ZOOM)
    if (session.getFit()) {
      rfb._display.autoscale(s.clientWidth, s.clientHeight)
    } else {
      rfb._display.scale = session.zoom
    }
    // keep the point under the fingers where it was
    const after = canvas.getBoundingClientRect()
    s.scrollLeft += after.left + fx * after.width - center.x
    s.scrollTop += after.top + fy * after.height - center.y
  }

  // ---- gestures ----

  onTouch = (ev) => {
    const rfb = this.rfb()
    if (!rfb || rfb._rfbConnectionState !== 'connected' || this.props.session.vncViewOnly) {
      return
    }
    ev.preventDefault()
    ev.stopPropagation()
    // touchpad mode only: noVNC's own direct-touch gestures (its pinch sends
    // Ctrl+wheel, which resizes icons in Explorer) must never run
    if (!rfb._touchpadOnly) {
      rfb._gestures.detach()
      rfb._touchpadOnly = true
    }
    const t = ev.touches
    if (ev.type === 'touchstart') {
      if (!this.g) {
        this.g = { n: 0, t0: Date.now(), moved: false, drag: false, mode: null }
        // a finger right after a tap: that tap becomes a double click or a drag
        if (this.pendingTap) {
          clearTimeout(this.pendingTap)
          this.pendingTap = null
          this.g.afterTap = true
        }
      }
      if (this.g.afterTap && t.length > 1) {
        this.g.afterTap = false
        this.click(1) // it was a plain tap after all
      }
      const g = this.g
      g.n = Math.max(g.n, t.length)
      if (t.length === 1) {
        g.last = { x: t[0].clientX, y: t[0].clientY }
        g.start = g.last
        this.holdTimer = setTimeout(this.onHold, HOLD_MS)
      } else {
        clearTimeout(this.holdTimer)
      }
      if (t.length === 2) {
        g.d0 = dist(t[0], t[1])
        g.c0 = mid(t[0], t[1])
        g.c = g.c0
        g.z0 = this.props.session.zoom || 1
      }
      return
    }
    const g = this.g
    if (!g) return
    if (ev.type === 'touchmove') {
      if (t.length === 1 && g.n === 1) {
        const p = { x: t[0].clientX, y: t[0].clientY }
        if (!g.moved && Math.hypot(p.x - g.start.x, p.y - g.start.y) > MOVE_SLOP) {
          g.moved = true
          clearTimeout(this.holdTimer)
          // tap, then swipe: drag with the left button down (selects text)
          if (g.afterTap && !g.drag) {
            g.drag = true
            this.clampCursor()
            this.mouse('mousedown', 1)
          }
        }
        if (g.moved || g.drag) {
          this.moveCursor((p.x - g.last.x) * SPEED, (p.y - g.last.y) * SPEED)
        }
        g.last = p
      } else if (t.length === 2) {
        const d = dist(t[0], t[1])
        const c = mid(t[0], t[1])
        if (!g.mode) {
          if (Math.abs(d - g.d0) > PINCH_SLOP) g.mode = 'pinch'
          else if (Math.hypot(c.x - g.c0.x, c.y - g.c0.y) > SCROLL_SLOP) g.mode = 'scroll'
        }
        if (g.mode === 'pinch') {
          this.zoom(g.z0 * d / g.d0, c)
        } else if (g.mode === 'scroll') {
          // natural scrolling: content follows the fingers
          this.wheel((g.c.x - c.x) * 2, (g.c.y - c.y) * 2)
        }
        g.c = c
      }
      return
    }
    // touchend / touchcancel: act once the last finger is up
    if (t.length) return
    clearTimeout(this.holdTimer)
    this.g = null
    const quick = Date.now() - g.t0 < TAP_MS
    if (g.drag) {
      this.mouse('mouseup', 0)
      this.releaseMods()
    } else if (ev.type === 'touchcancel') {
      // nothing
    } else if (g.n === 1 && !g.moved && g.afterTap) {
      // tap, tap: double click (a slow second press is just one more click)
      this.click(1)
      if (quick) this.click(1)
    } else if (g.n === 1 && !g.moved && quick) {
      // held back a moment, a finger may follow to drag or double click
      this.pendingTap = setTimeout(() => {
        this.pendingTap = null
        this.click(1)
      }, DOUBLE_MS)
      // tapped into a text field: bring the keyboard up (still inside the gesture)
      if (this.props.session.textCursor && !this.state.kb) {
        this.openKb()
      }
    } else if (g.n === 2 && !g.mode && quick) {
      this.click(2)
    } else if (g.n === 3 && quick) {
      this.toggleKb()
    }
  }

  onHold = () => {
    const g = this.g
    if (!g || g.n !== 1 || g.moved) return
    g.drag = true
    navigator.vibrate?.(15)
    this.clampCursor()
    this.mouse('mousedown', 1)
  }

  // ---- keyboard ----

  key (sym) {
    this.rfb()?.sendKey(sym, null)
  }

  // tap = latch until the next key or click, tap again = release
  tapMod = (sym) => {
    const { mods } = this.state
    const on = mods.includes(sym)
    this.rfb()?.sendKey(sym, null, !on)
    this.setState({ mods: on ? mods.filter(m => m !== sym) : [...mods, sym] })
  }

  releaseMods () {
    const { mods } = this.state
    if (!mods.length) return
    for (const m of mods) this.rfb()?.sendKey(m, null, false)
    this.setState({ mods: [] })
  }

  pressKey = (sym) => {
    this.key(sym)
    this.releaseMods()
  }

  resetText () {
    const ta = this.taRef.current
    ta.value = PAD
    ta.setSelectionRange(PAD.length, PAD.length)
    this.sent = PAD
  }

  // send the difference between what the field holds and what was sent, which
  // covers plain typing, IME composition and autocorrect replacing a word
  onInput = () => {
    const ta = this.taRef.current
    const { back, add } = textDiff(this.sent, ta.value)
    for (let n = back; n > 0; n--) this.key(K.BackSpace)
    for (const ch of add) this.key(charKeysym(ch))
    this.sent = ta.value
    if (back || add.length) this.releaseMods()
    if (!this.composing && (ta.value.length < PAD.length || ta.value.includes('\n') || ta.value.length > 64)) {
      this.resetText()
    }
  }

  onKeyDown = (ev) => {
    const sym = HW_KEYS[ev.key]
    if (sym) {
      ev.preventDefault()
      this.pressKey(sym)
    }
  }

  toggleKb = () => (this.state.kb ? this.closeKb() : this.openKb())

  // must run inside the user gesture, or the soft keyboard will not open
  openKb = () => {
    const m = this.mount()
    const rfb = this.rfb()
    if (!m || !rfb) return
    const vv = window.visualViewport
    this.savedH = vv ? vv.height : window.innerHeight
    this.canvasBottom = Math.min(
      rfb._canvas.getBoundingClientRect().bottom,
      this.screen().getBoundingClientRect().bottom
    )
    this.shown = false
    // freeze the desktop size, so a resizing webview does not rescale it
    m.style.height = m.clientHeight + 'px'
    m.parentElement.style.overflow = 'hidden'
    rfb.focusOnClick = false
    this.resetText()
    this.taRef.current.focus({ preventScroll: true })
    this.setState({ kb: true, barTop: this.savedH - BAR_H })
  }

  closeKb = () => {
    if (!this.state.kb) return
    const m = this.mount()
    if (m) {
      m.style.height = ''
      m.style.transform = ''
      m.parentElement.style.overflow = ''
    }
    this.moveShift(0)
    const rfb = this.rfb()
    if (rfb) rfb.focusOnClick = true
    this.releaseMods()
    this.taRef.current?.blur()
    this.setState({ kb: false })
  }

  // the keyboard has no event of its own, the visual viewport shrinking is it
  onViewport = () => {
    if (!this.state.kb) return
    const vv = window.visualViewport
    const kbH = this.savedH - vv.height
    if (kbH > 100) this.shown = true
    // closed with the system back button, the field kept focus
    if (this.shown && kbH < 50) {
      this.closeKb()
      return
    }
    const barTop = vv.offsetTop + vv.height - BAR_H
    // like RealVNC the keyboard just covers the bottom and the view stays put;
    // pushing the cursor under it slides the desktop up, at most until its
    // bottom edge sits above the key bar
    this.maxShift = Math.max(0, this.canvasBottom - barTop)
    this.setShift(clamp(this.shift, 0, this.maxShift))
    this.setState({ barTop })
  }

  setShift (shift) {
    this.moveShift(shift)
    this.mount().style.transform = shift ? `translateY(${-shift}px)` : ''
  }

  // the cursor rides along with the desktop
  moveShift (shift) {
    if (this.cy !== null) this.cy -= shift - this.shift
    this.shift = shift
  }

  onBlur = () => {
    // a tap on the key bar keeps focus (mousedown is cancelled), anything else closes
    setTimeout(() => {
      if (document.activeElement !== this.taRef.current) this.closeKb()
    }, 0)
  }

  // ---- render ----

  renderBar () {
    const { mods, barTop } = this.state
    const btn = (label, onClick, active) => (
      <button
        key={label}
        tabIndex={-1}
        className={'vnc-touch-key' + (active ? ' active' : '')}
        onMouseDown={e => e.preventDefault()}
        onClick={onClick}
      >
        {label}
      </button>
    )
    return (
      <div className='vnc-touch-bar' style={{ top: barTop, height: BAR_H }}>
        {btn('⌨✕', this.closeKb)}
        {MODS.map(([label, sym]) => btn(label, () => this.tapMod(sym), mods.includes(sym)))}
        {KEYS.map(([label, sym]) => btn(label, () => this.pressKey(sym)))}
      </div>
    )
  }

  render () {
    const { kb } = this.state
    return (
      <>
        <textarea
          ref={this.taRef}
          className='vnc-touch-input'
          autoCapitalize='off'
          autoComplete='off'
          autoCorrect='off'
          spellCheck={false}
          onInput={this.onInput}
          onKeyDown={this.onKeyDown}
          onCompositionStart={() => { this.composing = true }}
          onCompositionEnd={() => { this.composing = false }}
          onBlur={this.onBlur}
        />
        {kb
          ? this.renderBar()
          : (
            <button
              className='vnc-touch-kb-btn'
              onClick={this.openKb}
              title='Keyboard'
            >
              ⌨
            </button>
            )}
      </>
    )
  }
}
