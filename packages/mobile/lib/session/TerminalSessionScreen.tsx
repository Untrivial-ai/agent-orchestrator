import { Feather } from "@expo/vector-icons";
import { XtermJsWebView, type XtermWebViewHandle } from "@fressh/react-native-xtermjs-webview";
import { useLocalSearchParams, useNavigation, useRouter } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Alert, Keyboard, LayoutAnimation, Platform, Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { WebView } from "react-native-webview";
import { ApiError, getPreview, isTerminalStatus, killSession, sendMessage } from "../api";
import { authHeaders, isConfigured, loadConfig, type ServerConfig } from "../config";
import { terminalTheme, type Theme } from "../theme";
import { haptics } from "../haptics";
import { resetHeaderRightForSwap } from "../headerRightSwap";
import { MinimalBackButton } from "../MinimalBackButton";
import { MuxClient, type MuxStatus } from "../mux";
import { Composer } from "./Composer";
import { dockInset } from "./keyboardInset";
import { KeyRow } from "./KeyRow";
import {
	REROUTED_NOTICE,
	routeForSend,
	terminalPayload,
	TERMINAL_MODE_NOTICE,
	TERMINAL_UNAVAILABLE_NOTICE,
	type SendTarget,
} from "./sendRoute";
import { useApp } from "../store";
import { useVoiceInput } from "../voice/useVoiceInput";
import { useTheme, useThemedStyles, useThemeState } from "../ThemeProvider";
import { closeShellTerminal } from "../chat/api";
import {
	mobileInterfaceTransitionIsActive,
	mobileInterfaceTransitionIsCancellable,
	useInterfaceTransition,
} from "./useInterfaceTransition";
import { terminalInterfaceFailureRecovery } from "./terminalInterfaceRecovery";

const FONT_SIZE = 12;

// Injected into the xterm WebView after load. xterm has its own touch handlers
// that scroll by discrete lines (the janky "1 line per swipe"). We intercept in
// the CAPTURE phase and stopPropagation so those handlers never fire, then drive
// the viewport's scrollTop in proportion to finger movement (+ momentum). Taps
// (no significant movement) are left alone so tap-to-focus / keyboard still work.
const TERMINAL_ENHANCE_JS = `
(function () {
  // The text layer (xterm-screen canvas) captures touches for selection, which
  // blocks the smooth native scroll. Make it (and the hidden input) transparent
  // to touch so drags fall through to the viewport's native scroll, and so a tap
  // can't focus the input (no surprise keyboard).
  var s = document.createElement('style');
  s.textContent =
    '.xterm-screen{pointer-events:none !important;}' +
    '.xterm-helper-textarea{pointer-events:none !important;}' +
    '.xterm-viewport{pointer-events:auto !important;-webkit-overflow-scrolling:touch !important;}' +
    // We drive scrolling ourselves, so the WebView scrollbar is pure wasted width
    // on the right. Hide it (and give the viewport a thin overlay one) so the fit
    // reclaims those pixels as extra columns instead of a gap.
    '.xterm-viewport{scrollbar-width:none !important;}' +
    '.xterm-viewport::-webkit-scrollbar{width:0 !important;height:0 !important;display:none !important;}';
  document.head.appendChild(s);

  // Report xterm's REAL grid size (measured by the FitAddon from the actual
  // rendered cell) back to RN through fressh's own debug channel, so RN can tell
  // the PTY the exact cols/rows xterm is using — no font/DPR guessing.
  // Report the phone's NATURAL fit (what would fill the screen at the current
  // font) WITHOUT resizing the terminal. The render grid is driven by the daemon
  // (the shared PTY's authoritative size), which we scale to fit below; we report
  // this fit only so the daemon can size the PTY to the phone when it is the sole
  // viewer. proposeDimensions measures without applying, unlike fit().
  function reportFit() {
    try {
      var F = window.fitAddon; if (!F || !F.proposeDimensions) return;
      // xterm can't measure a real scrollbar on mobile (overlay scrollbars are
      // 0px, so its "offsetWidth - offsetWidth || 15" falls back to assuming a
      // 15px one). proposeDimensions subtracts that phantom width and under-reports
      // cols, leaving a dead strip on the right. We drive our own scroll and hide
      // the bar (see injected CSS above), so zero it before measuring to reclaim
      // those columns for the fit.
      try {
        var vp = window.terminal && window.terminal._core && window.terminal._core.viewport;
        if (vp) vp.scrollBarWidth = 0;
      } catch (_) {}
      var d = F.proposeDimensions();
      if (d && d.cols > 0 && d.rows > 0 && window.ReactNativeWebView) {
        window.ReactNativeWebView.postMessage(
          JSON.stringify({ type: 'debug', message: 'FRESSH_DIMS ' + d.cols + ' ' + d.rows }));
      }
    } catch (_) {}
  }

  // ---- Zoom & pan -----------------------------------------------------------
  // The daemon may hold the grid wider than the phone (a co-viewing desktop
  // drives the size). The resting view shrinks the whole grid uniformly to fit
  // the width (overview — may be tiny). Pinch zooms between that fit scale and
  // 1:1 (crisp, readable); while zoomed, one finger pans the viewport (vertical
  // overshoot spills into scrollback) and double-tap toggles overview <-> 1:1.
  // While zoomed we auto-pan to keep the cursor framed, so the prompt/output
  // stays in view without chasing it by hand.
  function term() { return window.terminal; }
  var Z = { s: 1, min: 1, tx: 0, ty: 0, zoomed: false, lastPan: 0 };
  function box() {
    var root = document.querySelector('.xterm');
    var screen = document.querySelector('.xterm-screen');
    var host = document.getElementById('terminal') || document.body;
    if (!root || !screen || !host) return null;
    return { root: root, natW: screen.offsetWidth, natH: screen.offsetHeight,
             contW: host.clientWidth || window.innerWidth,
             contH: host.clientHeight || window.innerHeight };
  }
  function clampT(b) {
    var minTx = Math.min(0, b.contW - b.natW * Z.s);
    var minTy = Math.min(0, b.contH - b.natH * Z.s);
    if (Z.tx < minTx) Z.tx = minTx; if (Z.tx > 0) Z.tx = 0;
    if (Z.ty < minTy) Z.ty = minTy; if (Z.ty > 0) Z.ty = 0;
  }
  function applyTransform(b) {
    b.root.style.transformOrigin = 'top left';
    b.root.style.transform = 'translate(' + Z.tx + 'px,' + Z.ty + 'px) scale(' + Z.s + ')';
  }
  // Fit-to-width baseline, re-run on grid/container changes. Tracks the fit
  // scale while at overview; preserves (re-clamps) the user's zoom otherwise.
  function applyScale() {
    try {
      var b = box(); if (!b || !b.natW || !b.contW) return;
      Z.min = Math.min(1, b.contW / b.natW);
      if (!Z.zoomed) { Z.s = Z.min; Z.tx = 0; Z.ty = 0; }
      else { if (Z.s < Z.min) Z.s = Z.min; clampT(b); }
      applyTransform(b);
    } catch (_) {}
  }
  // Zoom to scale s keeping the content under screen point (ax, ay) fixed.
  function setZoom(s, ax, ay) {
    var b = box(); if (!b) return;
    if (s < Z.min) s = Z.min; if (s > 1) s = 1;
    var px = (ax - Z.tx) / Z.s, py = (ay - Z.ty) / Z.s;
    Z.s = s; Z.tx = ax - px * s; Z.ty = ay - py * s;
    Z.zoomed = s > Z.min + 0.001;
    if (!Z.zoomed) { Z.s = Z.min; Z.tx = 0; Z.ty = 0; }
    clampT(b); applyTransform(b);
  }
  // Auto-pan so the cursor stays framed while zoomed in. Backs off for a few
  // seconds after a manual pan/pinch (never fight the finger) and only follows
  // the live screen — not while the user is reading scrollback.
  function followCursor() {
    try {
      if (!Z.zoomed || Date.now() - Z.lastPan < 4000) return;
      var T = term(); var b = box(); if (!T || !b || !T.cols || !T.rows) return;
      var buf = T.buffer && T.buffer.active; if (!buf) return;
      if (buf.viewportY !== buf.baseY) return;
      var cx = (buf.cursorX + 0.5) * (b.natW / T.cols) * Z.s;
      var cy = (buf.cursorY + 0.5) * (b.natH / T.rows) * Z.s;
      var mX = Math.min(48, b.contW / 4), mY = Math.min(48, b.contH / 4);
      if (Z.tx + cx < mX) Z.tx = mX - cx;
      else if (Z.tx + cx > b.contW - mX) Z.tx = b.contW - mX - cx;
      if (Z.ty + cy < mY) Z.ty = mY - cy;
      else if (Z.ty + cy > b.contH - mY) Z.ty = b.contH - mY - cy;
      clampT(b); applyTransform(b);
    } catch (_) {}
  }

  // When the grid changes, keep it pinned to the bottom (latest output).
  function pinBottom() { try { window.terminal.scrollToBottom(); } catch (_) {} }
  var cfTimer = 0;
  (function wire() {
    if (window.terminal && window.terminal.onResize && window.fitAddon) {
      // The grid changes only when the daemon tells RN the authoritative size and
      // RN calls resize(); re-fit-to-width and pin on every such change.
      window.terminal.onResize(function () { setTimeout(function () { applyScale(); pinBottom(); }, 0); });
      // Throttled cursor-follow: cursor moves fire in bursts while output streams.
      if (window.terminal.onCursorMove) {
        window.terminal.onCursorMove(function () {
          if (cfTimer) return;
          cfTimer = setTimeout(function () { cfTimer = 0; followCursor(); }, 120);
        });
      }
      // On box changes (keyboard/rotation) re-report the fit and re-scale, but do
      // NOT fit() — the daemon owns the grid; fitting would fight it.
      try {
        var host = document.getElementById('terminal') || document.body;
        var ro = new ResizeObserver(function () { reportFit(); applyScale(); });
        ro.observe(host);
      } catch (_) {}
      reportFit(); applyScale();
      // Android's first measure often runs before layout/fonts settle, so the grid
      // comes out narrower than the WebView until some later resize nudges it. Since
      // nothing changes the host box in between (the ResizeObserver never fires until
      // e.g. the keyboard opens), re-measure a few times as things settle so the fit
      // reaches full width on its own.
      [60, 200, 500, 1000].forEach(function (t) {
        setTimeout(function () { reportFit(); applyScale(); }, t);
      });
    } else {
      setTimeout(wire, 200);
    }
  })();

  // Keyboard is handled by a React-Native TextInput, NOT the WebView. We disable
  // the WebView's hidden textarea (see harden) so it can never raise a keyboard
  // or steal first-responder. The keyboard button shows/hides the keyboard.

  // Gesture routing (canvas is pointer-events:none, so we read touches here):
  //  • quick drag -> scrollback scroll (overview) / viewport pan (zoomed)
  //  • pinch -> zoom between fit-to-width and 1:1
  //  • long-press -> select the line; drag extends by lines; release copies
  //  • single tap -> nothing   • double-tap -> toggle overview <-> 1:1
  function lineAt(clientY) {
    var T = term(), screen = document.querySelector('.xterm-screen');
    if (!T || !screen) return 0;
    var r = screen.getBoundingClientRect();
    var ch = r.height / T.rows;
    var vis = Math.floor((clientY - r.top) / ch);
    if (vis < 0) vis = 0; if (vis > T.rows - 1) vis = T.rows - 1;
    var top = (T.buffer && T.buffer.active) ? T.buffer.active.viewportY : 0;
    return top + vis;
  }
  function copySel() {
    var T = term(); if (!T) return; var txt = '';
    try { txt = T.getSelection(); } catch (_) {}
    if (!txt) return;
    try { if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(txt); } catch (_) {}
  }

  // ---- App-driven scrolling (harness-agnostic) ------------------------------
  // Full-screen TUIs (Claude Code, Codex, Gemini, aider, vim, less, ...) run in
  // the terminal's ALTERNATE screen buffer, which by design keeps NO xterm
  // scrollback — so .xterm-viewport has nothing to scroll and a drag "does
  // nothing". Rather than hand-encode scroll bytes per harness, we synthesize the
  // same 'wheel' event a desktop mouse produces and let xterm's own handler
  // translate it for WHATEVER the app negotiated: proper mouse-wheel bytes when
  // the app tracks the mouse (X10/UTF-8/SGR — xterm picks the right encoding),
  // else cursor-key presses (honoring application-cursor mode) in the alt buffer.
  // This means it works for every harness, not just one. The normal buffer (plain
  // shell scrollback) keeps its local viewport scroll below.
  function isAltScreen() {
    try { var b = term().buffer.active; return !!(b && b.type === 'alternate'); }
    catch (_) { return false; }
  }
  function mouseActive() {
    try { var m = term().modes; return !!(m && m.mouseTrackingMode && m.mouseTrackingMode !== 'none'); }
    catch (_) { return false; }
  }
  // Let xterm own the scroll only where a local viewport scroll wouldn't reach the
  // app: the alt buffer (no scrollback) or any buffer where the app tracks mouse.
  function appDrivesScroll() { return isAltScreen() || mouseActive(); }
  // Dispatch one wheel notch to xterm (up = toward older output). Coordinates are
  // the finger position so mouse-reporting apps get an accurate cell.
  function wheelTick(up, cx, cy) {
    var el = document.querySelector('.xterm'); if (!el) return;
    var ev;
    try {
      ev = new WheelEvent('wheel', { bubbles: true, cancelable: true,
        deltaX: 0, deltaY: up ? -1 : 1, deltaZ: 0,
        deltaMode: 1 /* DOM_DELTA_LINE */, clientX: cx, clientY: cy });
    } catch (_) {
      ev = document.createEvent('Event'); ev.initEvent('wheel', true, true);
      ev.deltaY = up ? -1 : 1; ev.deltaMode = 1; ev.clientX = cx; ev.clientY = cy;
    }
    el.dispatchEvent(ev);
  }

  var sX = 0, sY = 0, mode = 'idle', anchor = 0, lpTimer = 0;
  var MOVE = 10, LONGPRESS = 350, DBLTAP = 300;
  var altLines = 0;                        // wheel notches emitted to the app this gesture
  var SCROLL_STEP_PX = 24;                 // finger px per wheel notch (scale-independent)
  // Android: we drive the viewport's scrollTop directly off finger movement —
  // its native overflow-scroll doesn't respond to touch reliably in the WebView,
  // which is why the terminal felt unscrollable there. iOS keeps native momentum.
  var _vp = null, startScroll = 0;
  var lX = 0, lY = 0;                       // last touch point (zoomed pan deltas)
  var pinch0 = null;                        // pinch anchor {d, s, mx, my}
  var lastTap = 0, ltX = 0, ltY = 0;        // double-tap detection
  function clearLP() { if (lpTimer) { clearTimeout(lpTimer); lpTimer = 0; } }
  function touchDist(e) {
    var a = e.touches[0], b = e.touches[1];
    var dx = a.clientX - b.clientX, dy = a.clientY - b.clientY;
    return Math.sqrt(dx * dx + dy * dy);
  }

  document.addEventListener('touchstart', function (e) {
    // Taps on the scroll-to-top button are handled by the button itself; don't
    // let this capture-phase listener consume them as a terminal tap/long-press.
    var _tt = e.target;
    if (_tt && _tt.closest && _tt.closest('#ao-scrolltop')) return;
    if (e.touches && e.touches.length >= 2) {
      // Second finger down -> pinch. Cancel any pending tap/long-press/scroll.
      clearLP(); mode = 'pinch';
      pinch0 = { d: touchDist(e) || 1, s: Z.s,
                 mx: (e.touches[0].clientX + e.touches[1].clientX) / 2,
                 my: (e.touches[0].clientY + e.touches[1].clientY) / 2 };
      try { term() && term().clearSelection(); } catch (_) {}
      return;
    }
    var t = e.touches ? e.touches[0] : e;
    sX = t.clientX; sY = t.clientY; lX = sX; lY = sY; mode = 'pending';
    altLines = 0;
    _vp = document.querySelector('.xterm-viewport');
    startScroll = _vp ? _vp.scrollTop : 0;
    try { term() && term().clearSelection(); } catch (_) {}
    clearLP();
    lpTimer = setTimeout(function () {
      if (mode !== 'pending') return;
      mode = 'select'; anchor = lineAt(sY);
      try { term().selectLines(anchor, anchor); } catch (_) {}
    }, LONGPRESS);
  }, { capture: true, passive: true });

  document.addEventListener('touchmove', function (e) {
    if (mode === 'pinch') {
      if (!e.touches || e.touches.length < 2 || !pinch0) return;
      if (e.cancelable) e.preventDefault();  // keep the page/viewport from moving
      var mx = (e.touches[0].clientX + e.touches[1].clientX) / 2;
      var my = (e.touches[0].clientY + e.touches[1].clientY) / 2;
      // Two-finger drag pans while pinching; the scale keeps the content under
      // the midpoint anchored so the zoom feels centered on the fingers.
      Z.tx += mx - pinch0.mx; Z.ty += my - pinch0.my;
      setZoom(pinch0.s * (touchDist(e) / pinch0.d), mx, my);
      pinch0.mx = mx; pinch0.my = my;
      Z.lastPan = Date.now();
      return;
    }
    var t = e.touches ? e.touches[0] : e;
    if (mode === 'pending') {
      if (Math.abs(t.clientX - sX) > MOVE || Math.abs(t.clientY - sY) > MOVE) {
        mode = 'scroll'; clearLP(); lX = t.clientX; lY = t.clientY;
      }
      return;
    }
    if (mode === 'scroll') {
      if (appDrivesScroll()) {
        // The app owns scrolling here (alt buffer / mouse tracking): feed it wheel
        // notches instead of moving the (empty) xterm viewport. Content follows the
        // finger (drag down -> older), matching the normal buffer's direction below.
        // One notch per SCROLL_STEP_PX of travel; emit only on each boundary cross.
        if (e.cancelable) e.preventDefault();
        var moved = (t.clientY - sY) / SCROLL_STEP_PX;   // + = finger down = older
        var want = moved > 0 ? Math.floor(moved) : Math.ceil(moved);
        var diff = want - altLines;
        if (diff !== 0) {
          var up = diff > 0;                             // more "older" notches -> wheel up
          for (var i = 0; i < Math.abs(diff); i++) wheelTick(up, t.clientX, t.clientY);
          altLines = want;
        }
        // While zoomed, the horizontal component still pans the magnified grid.
        if (Z.zoomed) {
          var bz = box();
          if (bz) { Z.tx += t.clientX - lX; clampT(bz); applyTransform(bz); Z.lastPan = Date.now(); }
        }
        lX = t.clientX; lY = t.clientY;
        return;
      }
      if (Z.zoomed) {
        // Zoomed in: one finger pans the viewport over the big grid. Vertical
        // overshoot past the grid edge spills into scrollback scrolling (divide
        // by scale: scrollTop is in unscaled content px, the finger in screen px).
        if (e.cancelable) e.preventDefault();
        var b = box();
        if (b) {
          Z.tx += t.clientX - lX;
          var wantTy = Z.ty + (t.clientY - lY);
          Z.ty = wantTy;
          clampT(b);
          var spill = wantTy - Z.ty;
          if (spill !== 0 && _vp) _vp.scrollTop -= spill / Z.s;
          applyTransform(b);
        }
        Z.lastPan = Date.now();
        lX = t.clientX; lY = t.clientY;
        return;
      }
      // Overview: scrollback scroll. Android: move the viewport ourselves, 1:1
      // with the finger. iOS: leave it to native momentum (don't preventDefault).
      if (IS_ANDROID && _vp) {
        _vp.scrollTop = startScroll - (t.clientY - sY);
        if (e.cancelable) e.preventDefault();
      }
      return;
    }
    if (mode === 'select') {
      if (e.cancelable) e.preventDefault();  // stop native scroll while selecting
      var cur = lineAt(t.clientY);
      try { term().selectLines(Math.min(anchor, cur), Math.max(anchor, cur)); } catch (_) {}
    }
  }, { capture: true, passive: false });

  document.addEventListener('touchend', function (e) {
    clearLP();
    if (mode === 'pinch') {
      // Stay in pinch while two fingers remain; otherwise done (the leftover
      // finger must lift and re-touch to start a new gesture).
      if (!e.touches || e.touches.length < 2) { mode = 'idle'; pinch0 = null; }
      return;
    }
    if (mode === 'select') copySel();
    if (mode === 'pending') {
      // A tap. Two taps close together toggle overview <-> 1:1 at the tap point.
      var now = Date.now();
      if (now - lastTap < DBLTAP && Math.abs(sX - ltX) < 40 && Math.abs(sY - ltY) < 40) {
        lastTap = 0;
        if (Z.zoomed) setZoom(Z.min, 0, 0);
        else { setZoom(1, sX, sY); Z.lastPan = Date.now(); }
      } else { lastTap = now; ltX = sX; ltY = sY; }
    }
    mode = 'idle';
  }, { capture: true, passive: true });

  // ---- Scroll-to-top button ------------------------------------------------
  // A floating button that scrolls back to the oldest output. It lives in the
  // terminal DOM (not RN) because the WebView package exposes no inject hook.
  // Two regimes, mirroring the drag handler above:
  //  • normal buffer  -> xterm owns the scrollback; scrollToTop() is a true jump,
  //    and viewportY tells us whether there's anything above (hide at the top).
  //  • alt buffer / mouse-tracking (Claude Code, Codex, aider, vim, less, ...) ->
  //    the APP owns its scrollback, so scrollToTop() can't reach it. Send the same
  //    wheel notches a drag produces. We can't query the app's scroll position, so
  //    the button always shows there and the jump is a generous burst.
  (function scrollTopBtn() {
    var btn = document.createElement('div');
    btn.id = 'ao-scrolltop';
    btn.setAttribute('aria-label', 'Scroll to top');
    btn.innerHTML = '↑'; // up arrow
    var s = btn.style;
    s.position = 'fixed'; s.right = '12px'; s.bottom = '12px';
    s.width = '36px'; s.height = '36px'; s.lineHeight = '36px';
    s.textAlign = 'center'; s.borderRadius = '18px';
    s.background = 'rgba(20,28,40,0.72)'; s.color = '#dbe4f0';
    s.fontSize = '18px'; s.fontWeight = '600';
    s.border = '1px solid rgba(120,140,170,0.35)';
    s.boxShadow = '0 2px 8px rgba(0,0,0,0.4)';
    s.webkitBackdropFilter = 'blur(6px)'; s.backdropFilter = 'blur(6px)';
    s.zIndex = '2147483647'; s.display = 'none'; s.cursor = 'pointer';
    s.pointerEvents = 'auto'; s.userSelect = 'none'; s.webkitUserSelect = 'none';
    (document.getElementById('terminal') || document.body).appendChild(btn);

    function atTop() {
      try { var b = term().buffer.active; return !b || b.viewportY <= 0; }
      catch (_) { return true; }
    }
    function update() {
      // When the app drives scrolling we can't read its position, so always offer
      // the button; otherwise hide it once xterm's viewport is at the top.
      var show = appDrivesScroll() ? true : !atTop();
      btn.style.display = show ? 'block' : 'none';
    }
    // Walk the app's scrollback up with the same wheel notches a drag produces,
    // chunked across frames so a few hundred events don't block the renderer.
    var TOP_BURST = 400, BURST_CHUNK = 25;
    function burstUp() {
      var el = document.querySelector('.xterm');
      var r = el ? el.getBoundingClientRect() : null;
      var cx = r ? r.left + r.width / 2 : 0;
      var cy = r ? r.top + r.height / 2 : 0;
      var left = TOP_BURST;
      (function step() {
        for (var i = 0; i < BURST_CHUNK && left > 0; i++, left--) wheelTick(true, cx, cy);
        if (left > 0) requestAnimationFrame(step);
      })();
    }
    // Tap -> back to the oldest output. Swallow the event so nothing else reacts.
    function go(e) {
      if (e.stopPropagation) e.stopPropagation();
      if (e.cancelable && e.preventDefault) e.preventDefault();
      if (appDrivesScroll()) burstUp();
      else { try { term().scrollToTop(); } catch (_) {} }
      update();
    }
    btn.addEventListener('touchstart', go, { capture: true });
    btn.addEventListener('click', go, { capture: true });

    // Re-evaluate on xterm scroll and buffer switches; poll as a cheap fallback
    // for transitions those miss (our drag handler moves the viewport directly).
    (function wire() {
      var T = term();
      if (T && T.onScroll) {
        T.onScroll(update);
        try { if (T.buffer && T.buffer.onBufferChange) T.buffer.onBufferChange(update); } catch (_) {}
        update();
      } else { setTimeout(wire, 200); }
    })();
    setInterval(update, 500);
  })();

  // Disable the WebView's hidden textarea so it can NEVER show a keyboard or
  // steal first-responder from the RN input. RN handles all keyboard I/O.
  function harden() {
    var t = document.querySelector('.xterm-helper-textarea');
    if (t) {
      t.disabled = true;
      t.setAttribute('inputmode', 'none');
      t.setAttribute('readonly', 'readonly');
      t.setAttribute('autocorrect', 'off');
      t.setAttribute('autocapitalize', 'off');
      t.setAttribute('autocomplete', 'off');
      t.setAttribute('spellcheck', 'false');
    }
  }
  harden(); setTimeout(harden, 400); setTimeout(harden, 1500);
  setInterval(harden, 3000); // keep it disabled if xterm recreates it
  true;
})();
true;
`;

const statusLabel: Record<MuxStatus, string> = {
	connecting: "connecting...",
	open: "live",
	closed: "disconnected",
	error: "error",
};
const statusColorFor = (t: Theme): Record<MuxStatus, string> => ({
	connecting: t.attention,
	open: t.green,
	closed: t.textTertiary,
	error: t.red,
});

function terminalInterfacePhaseLabel(phase?: string): string {
	switch (phase) {
		case "draining":
			return "Waiting for the current terminal turn to finish. New AO messages are queued safely.";
		case "source_stopping":
			return "Stopping the terminal controller before Chat starts.";
		case "source_stopped":
			return "Terminal controller stopped. The worktree and native conversation are unchanged.";
		case "target_starting":
			return "Resuming the same native conversation in Chat.";
		case "activating":
			return "Opening the Chat interface.";
		default:
			return "Checking that Chat can resume this agent's native conversation.";
	}
}

export default function TerminalScreen() {
	const t = useTheme();
	const { scheme } = useThemeState();
	const styles = useThemedStyles(makeStyles);
	const params = useLocalSearchParams<{ id?: string; handleId?: string; projectId?: string; sessionId?: string; title?: string }>();
	const shellOnly = Boolean(params.handleId);
	const id = String(params.handleId ?? params.id ?? "");
	const sessionId = shellOnly ? String(params.sessionId ?? "") : id;
	const projectId = params.projectId ? String(params.projectId) : undefined;
	const router = useRouter();
	const navigation = useNavigation();
	const [headerRightReady, setHeaderRightReady] = useState(false);
	useLayoutEffect(
		() => resetHeaderRightForSwap(
			() => navigation.setOptions({ headerRight: undefined }),
			() => setHeaderRightReady(true),
		),
		[navigation],
	);
	const insets = useSafeAreaInsets();

	// Leaving the screen: pop when there's history, otherwise go to the board.
	// Guards against a missing/broken back button when this route was cold-started
	// with no back-stack - e.g. a reload while on the terminal, or a deep link.
	const leave = useCallback(() => {
		if (router.canGoBack()) router.back();
		else router.replace("/");
	}, [router]);

	const xtermRef = useRef<XtermWebViewHandle | null>(null);
	// The PTY remains attached while xterm remounts for a font/theme change. Bytes
	// arriving between the old WebView unmount and the new onInitialized used to
	// disappear; retain them in wire order until the replacement can accept writes.
	const xtermReadyRef = useRef(false);
	const pendingOutputRef = useRef<Uint8Array[]>([]);
	const muxRef = useRef<MuxClient | null>(null);
	const openedRef = useRef(false);
	// Last grid size reported by the WebView's FitAddon, so we can send it to the
	// PTY the moment the terminal opens (dims may arrive before or after open).
	const lastDimsRef = useRef<{ cols: number; rows: number } | null>(null);
	// The authoritative grid the daemon told us the shared PTY is actually using
	// (driven by the largest/primary client — e.g. a co-viewing desktop). We render
	// THIS grid (scaled to fit), not the phone's own fit, so the display matches the
	// PTY and a full-screen TUI doesn't mis-render. Null until the daemon reports it.
	const authRef = useRef<{ cols: number; rows: number } | null>(null);

	const [cfg, setCfg] = useState<ServerConfig | null>(null);
	const [status, setStatus] = useState<MuxStatus>("connecting");
	const [size, setSize] = useState<{ cols: number; rows: number } | null>(null);
	const [banner, setBanner] = useState<string | null>(null);
	const [kbHeight, setKbHeight] = useState(0); // iOS: space to reserve for keyboard
	const [kbVisible, setKbVisible] = useState(false); // both platforms
	const [msg, setMsg] = useState("");
	const [sending, setSending] = useState(false);
	const [sendTarget, setSendTarget] = useState<SendTarget>(shellOnly ? "terminal" : "agent");
	// Terminal font size. Smaller font = more rows/cols, which is the only way to
	// see more of a full-screen TUI (alt-screen apps have no scrollback). Changing
	// it remounts the xterm; the PTY persists and re-attaches at the denser grid.
	const [fontSize, setFontSize] = useState(FONT_SIZE);
	// A terminated session has no live PTY (the mux answers "Session not found").
	// Track that + the known status so we can offer Restore instead of a dead term.
	const [notFound, setNotFound] = useState(false);
	const [restoring, setRestoring] = useState(false);
	// In-app browser: shows the static preview file the agent generated (an
	// index.html). We poll the daemon's on-demand detector while the terminal is
	// open, but we deliberately DO NOT auto-open the overlay: the detector falls back
	// to any previewable file (e.g. a repo's README.md), so auto-popping would steal
	// the screen with an unbuilt/blank page. Instead the globe button lights up with a
	// green dot when the agent has produced something to view (any previewable file
	// except the repo README); the user taps it to open.
	const [browserOpen, setBrowserOpen] = useState(false);
	const [preview, setPreview] = useState<{ entry: string; url: string } | null>(null);
	const previewWebRef = useRef<WebView>(null);

	const { sessions, orchestrators, restore, refresh } = useApp();
	const known = sessions.find((s) => s.id === sessionId) ?? orchestrators.find((o) => o.id === sessionId) ?? null;
	const interfaceSwitch = useInterfaceTransition(cfg, shellOnly ? "" : sessionId, refresh);
	const interfaceTransitionActive = mobileInterfaceTransitionIsActive(interfaceSwitch.transition);
	const interfaceTransitionNotice =
		!interfaceTransitionActive &&
		!interfaceSwitch.transition?.noticeAcknowledgedAt &&
		(interfaceSwitch.transition?.phase === "failed" ||
			interfaceSwitch.transition?.phase === "recovery_required")
			? interfaceSwitch.transition
			: undefined;
	const interfaceTransitionRecovered =
		interfaceTransitionNotice?.phase === "recovery_required" &&
		interfaceTransitionNotice.errorCode === "DAEMON_RESTARTED";
	const interfaceTransitionNoticeText =
		interfaceTransitionNotice?.errorDetail ||
		(interfaceTransitionRecovered
			? "AO restored the session in its last committed interface."
			: interfaceTransitionNotice?.phase === "recovery_required"
				? "The interface switch needs recovery before more work is sent."
				: "The interface switch failed; Terminal UI remains available.");
	const interfaceFailureRecovery = useMemo(
		() => terminalInterfaceFailureRecovery(interfaceSwitch.transition),
		[interfaceSwitch.transition?.errorCode],
	);
	const interfaceBusy = Boolean(
		known &&
		(known.status === "working" ||
			known.status === "needs_input" ||
			known.activity === "active" ||
			known.activity === "waiting_input" ||
			known.activity === "blocked"),
	);
	const dead = notFound || (!shellOnly && known ? isTerminalStatus(known.status) : false);
	// What counts as a live preview: any file the daemon surfaces (an .html build, or
	// a generated doc like plan.md / a report) EXCEPT a repo's README, which the
	// detector's markdown fallback always matches on a fresh checkout. Filtering the
	// README out keeps the globe's green dot meaningful — it means "there's something
	// the agent produced to view", not just "this repo has a README".
	const previewBase = (preview?.entry ?? "").split("/").pop() ?? "";
	const isReadme = /^readme\.(md|markdown)$/i.test(previewBase);
	const hasPreview = !!preview && !isReadme;

	// Neither platform shrinks the layout for the keyboard: iOS never has, and on
	// Android edge-to-edge (edgeToEdgeEnabled) defeats windowSoftInputMode=adjustResize
	// so the window no longer resizes - the keyboard just draws over our content.
	// So reserve kbHeight on BOTH platforms and let the screen pad itself above the
	// keyboard, else the input dock (and its send button) hide behind it. This is
	// the ONLY place the keyboard height is applied — the dock adds nothing on top
	// of it (see dockInset), because doing both is what made the bar kick.
	useEffect(() => {
		const isIOS = Platform.OS === "ios";
		const showEvt = isIOS ? "keyboardWillShow" : "keyboardDidShow";
		const hideEvt = isIOS ? "keyboardWillHide" : "keyboardDidHide";
		// Ride the system keyboard curve. Without this the relayout lands as an
		// instant jump while the keyboard is still sliding, which is most of why
		// the dock looked like it kicked.
		const animate = (duration?: number) =>
			LayoutAnimation.configureNext({
				duration: duration || 250,
				update: { type: LayoutAnimation.Types.keyboard },
			});
		const show = Keyboard.addListener(showEvt, (e) => {
			animate(e.duration);
			setKbVisible(true);
			setKbHeight(e.endCoordinates.height);
		});
		const hide = Keyboard.addListener(hideEvt, (e) => {
			animate(e?.duration);
			setKbVisible(false);
			setKbHeight(0);
		});
		// willShow can report a height that still includes the accessory bar we hid,
		// leaving a gap. didShow reports the actual final frame - use it to correct.
		// Guarded so the common case where the two agree is a no-op instead of a
		// second visible nudge.
		const didShow = isIOS
			? Keyboard.addListener("keyboardDidShow", (e) => {
					const next = e.endCoordinates.height;
					setKbHeight((h) => {
						if (h === next) return h;
						animate(e.duration);
						return next;
					});
				})
			: null;
		// Backup: guarantee the reserved space collapses even if willHide is missed.
		// iOS-only, like didShow above — Android's hide listener is already bound to
		// keyboardDidHide, so registering this there subscribed the same handler to
		// the same event twice and collapsed the dock on both.
		const didHide = isIOS
			? Keyboard.addListener("keyboardDidHide", () => {
					setKbVisible(false);
					setKbHeight(0);
				})
			: null;
		return () => {
			show.remove();
			hide.remove();
			didShow?.remove();
			didHide?.remove();
		};
	}, []);

	// Header shows just the short id; Kill lives in our own status bar below so we
	// fully control its shape/alignment (iOS draws its own box behind header
	// buttons, which fights any custom background).
	useLayoutEffect(() => {
		navigation.setOptions({
			title: shellOnly ? (params.title || "Worktree shell") : id.length > 22 ? `${id.slice(0, 20)}...` : id,
			// Always render our own Back control so it works even when the app was
			// cold-started directly on this route (reload/deep link) and the stack
			// has no history for the default back button to use.
			headerLeft: () => <MinimalBackButton onPress={leave} />,
		});
	}, [navigation, id, leave, params.title, shellOnly]);

	// Load config, then connect the mux socket.
	useEffect(() => {
		let disposed = false;
		(async () => {
			const config = await loadConfig();
			if (disposed) return;
			setCfg(config);
			if (!isConfigured(config)) return;

			const mux = new MuxClient(config, {
				onStatus: (s) => setStatus(s),
				onTerminalData: (tid, bytes) => {
					if (tid !== id) return;
					if (!xtermReadyRef.current || !xtermRef.current) {
						pendingOutputRef.current.push(bytes.slice());
						return;
					}
					xtermRef.current.write(bytes);
				},
				onTerminalExited: (tid, code) => {
					if (tid === id) {
						setBanner(`Session exited (code ${code})`);
						setNotFound(true);
					}
				},
				onTerminalError: (tid, msg) => {
					if (tid !== id) return;
					// A missing PTY means the session is terminated - offer Restore
					// instead of surfacing it as a raw error banner.
					if (/not found/i.test(msg)) setNotFound(true);
					else setBanner(msg);
				},
				onTerminalResize: (tid, cols, rows) => {
					if (tid !== id) return;
					// The daemon's authoritative grid: render exactly this (the webview
					// scales it to fit), so the phone mirrors the shared PTY instead of
					// fitting to its own screen and mis-drawing a wider grid.
					authRef.current = { cols, rows };
					setSize({ cols, rows });
					xtermRef.current?.resize({ cols, rows });
				},
			});
			muxRef.current = mux;
			mux.connect();
		})();
		return () => {
			disposed = true;
			muxRef.current?.disconnect();
			muxRef.current = null;
			xtermReadyRef.current = false;
			pendingOutputRef.current = [];
		};
	}, [id]);

	useLayoutEffect(() => {
		xtermReadyRef.current = false;
	}, [fontSize, scheme]);

	// Poll the daemon's on-demand preview detector while the terminal is open, just
	// to keep `preview` current for the globe button. We never auto-open the overlay
	// (see the note by the state above): the detector's markdown fallback matches a
	// repo README, so auto-popping would surface a blank/unbuilt page.
	useEffect(() => {
		if (shellOnly) return;
		if (!cfg || !isConfigured(cfg)) return;
		let cancelled = false;
		let timer: ReturnType<typeof setTimeout> | null = null;
		const tick = async () => {
			try {
				const p = await getPreview(cfg, id);
				if (cancelled) return;
				setPreview(p);
			} catch {
				/* transient - keep polling */
			}
			if (!cancelled) timer = setTimeout(tick, 5000);
		};
		tick();
		return () => {
			cancelled = true;
			if (timer) clearTimeout(timer);
		};
	}, [cfg, id, shellOnly]);

	// The WebView reports the phone's NATURAL fit (proposeDimensions, measure-only).
	// We forward it to the daemon as this client's requested size — used only when
	// the phone is the sole viewer (a co-viewing desktop, being primary, wins). The
	// render grid comes back via onTerminalResize; until it does, render the fit so
	// the terminal isn't blank.
	const applyDims = useCallback(
		(cols: number, rows: number) => {
			lastDimsRef.current = { cols, rows };
			if (openedRef.current) muxRef.current?.resize(id, cols, rows, projectId);
			if (!authRef.current) {
				setSize({ cols, rows });
				xtermRef.current?.resize({ cols, rows });
			}
		},
		[id, projectId],
	);

	// fressh routes WebView {type:'debug'} messages to logger.log(prefix, message).
	// We piggyback on it for the FRESSH_DIMS report (using a custom onMessage would
	// clobber fressh's own bridge).
	const logger = useMemo(
		() => ({
			log: (...args: unknown[]) => {
				const m = args[args.length - 1];
				if (typeof m === "string" && m.startsWith("FRESSH_DIMS ")) {
					const parts = m.split(" ");
					const cols = parseInt(parts[1], 10);
					const rows = parseInt(parts[2], 10);
					if (cols > 0 && rows > 0) applyDims(cols, rows);
				}
			},
		}),
		[applyDims],
	);

	const onInitialized = useCallback(() => {
		xtermReadyRef.current = true;
		const pending = pendingOutputRef.current;
		pendingOutputRef.current = [];
		for (const bytes of pending) xtermRef.current?.write(bytes);
		// A fresh xterm (first mount, or a remount after a font-zoom) starts at its
		// default grid — restore the daemon's authoritative grid onto it so it keeps
		// mirroring the shared PTY rather than snapping to the default.
		if (authRef.current) xtermRef.current?.resize(authRef.current);
		// Guard against a second open if the WebView re-fires onInitialized (e.g.
		// remount on orientation change) - that would attach the PTY twice.
		if (openedRef.current) return;
		openedRef.current = true;
		muxRef.current?.openTerminal(id, projectId);
		// If the FitAddon already reported dims before open, send them to the PTY now.
		const d = lastDimsRef.current;
		if (d) muxRef.current?.resize(id, d.cols, d.rows, projectId);
	}, [id, projectId]);

	const onData = useCallback(
		(data: string) => {
			muxRef.current?.sendInput(id, data, projectId);
		},
		[id, projectId],
	);

	const sendKey = useCallback(
		(seq: string) => {
			muxRef.current?.sendInput(id, seq, projectId);
		},
		[id, projectId],
	);

	// Send the composed text to the selected route. The agent route can still
	// auto-engage the terminal route when the daemon reports a blocked prompt.
	//
	// AO's /send is the right route for a message: the daemon hands it to the
	// harness and submits it. But it sanitises control characters and refuses
	// outright while a session is paused on a permission prompt — answering 409
	// SESSION_AWAITING_DECISION with the advice "answer it in the session
	// terminal first". So on exactly that code we do what it says and write the
	// line to the PTY instead. `shouldRetryOnTerminal` keeps that narrow: a
	// terminated or exited session has no PTY, and rerouting there would swallow
	// the user's text and report success.
	//
	// The reroute is announced in the banner rather than done silently, and the
	// text stays in the field on any failure we did NOT handle, so nothing typed
	// is ever lost.
	const sendPrompt = useCallback(async () => {
		const text = msg.trim();
		if (!text) return;
		if (routeForSend(sendTarget) === "terminal") {
			if (muxRef.current && status === "open") {
				muxRef.current.sendInput(id, terminalPayload(text), projectId);
				haptics.success();
				setMsg("");
				setBanner(TERMINAL_MODE_NOTICE);
			} else {
				haptics.error();
				setBanner(TERMINAL_UNAVAILABLE_NOTICE);
			}
			return;
		}
		setSending(true);
		try {
			const config = cfg ?? (await loadConfig());
			await sendMessage(config, id, text);
			haptics.success();
			setMsg("");
		} catch (e) {
			const failure = e instanceof ApiError ? e : null;
			// Only reroute onto a mux we actually hold open — otherwise the write is
			// a no-op and we would clear the field having sent nothing.
			if (routeForSend(sendTarget, failure) === "terminal" && muxRef.current && status === "open") {
				muxRef.current.sendInput(id, terminalPayload(text), projectId);
				haptics.success();
				setMsg("");
				setSendTarget("terminal");
				setBanner(REROUTED_NOTICE);
			} else {
				haptics.error();
				setBanner(`Send failed: ${e instanceof Error ? e.message : String(e)}`);
			}
		} finally {
			setSending(false);
		}
	}, [msg, sendTarget, cfg, id, projectId, status]);

	// Push-to-talk dictation, captured on the PHONE rather than by the harness.
	//
	// Driving a harness's own voice mode from here (Claude Code's /voice) looks
	// easy — the key row already sends arbitrary bytes to the PTY — but it records
	// through the *daemon host's* microphone, which is a machine the user is not
	// sitting at. It would also only ever work for the harnesses that ship a voice
	// mode at all.
	//
	// Capturing here inverts both: the transcript is just text, so it leaves
	// through sendPrompt like anything typed, and every harness gets it for free.
	const voice = useVoiceInput({
		onTranscript: useCallback((text: string) => {
			// Land in the composer as an editable draft rather than sending. This
			// text reaches an agent with tool access and speech-to-text mishears code
			// vocabulary often enough that silent submission isn't acceptable.
			//
			// The composer is always mounted now, so there is nothing to open — and
			// nothing focuses the field either, which would pop the keyboard over the
			// terminal mid-phrase.
			// Append, so several held phrases build one prompt (the same way holding
			// the key again appends in Claude Code's own dictation).
			setMsg((m) => (m ? `${m} ${text}` : text));
			haptics.success();
		}, []),
	});

	// Recognition failures surface in the existing banner rather than new UI.
	useEffect(() => {
		if (voice.error) setBanner(voice.error);
	}, [voice.error]);

	// Toggle the in-app browser. The poll above keeps `preview` current, so a tap
	// just shows/hides the overlay. A bare README (the detector's markdown fallback)
	// reports "no preview yet" instead of surfacing an unbuilt repo doc.
	const toggleBrowser = useCallback(() => {
		haptics.tap();
		if (browserOpen) {
			setBrowserOpen(false);
			return;
		}
		if (!hasPreview) {
			setBanner("No preview yet - waiting for the agent to generate a page or document...");
			return;
		}
		setBrowserOpen(true);
	}, [browserOpen, hasPreview]);

	const startInterfaceSwitch = useCallback(
		async (policy: "drain" | "interrupt") => {
			try {
				await interfaceSwitch.start("chat", policy);
				setBanner(null);
			} catch (cause) {
				setBanner(`Switch failed: ${cause instanceof Error ? cause.message : String(cause)}`);
			}
		},
		[interfaceSwitch],
	);

	const requestInterfaceSwitch = useCallback(() => {
		haptics.tap();
		if (!interfaceSwitch.status?.supported) {
			Alert.alert(
				"Chat unavailable",
				interfaceSwitch.status?.reason ||
					interfaceSwitch.error ||
					"This agent has not declared a compatible native conversation handoff.",
			);
			return;
		}
		if (!interfaceBusy) {
			void startInterfaceSwitch("drain");
			return;
		}
		Alert.alert(
			"Switch to Chat?",
			known?.activity === "waiting_input" || known?.activity === "blocked"
				? "This turn is waiting for your input. Finish waits for your answer; stop cancels it and switches now."
				: "Keep the same AO session, worktree, and native agent conversation.",
			[
				{ text: "Keep Terminal UI", style: "cancel" },
				{ text: "Finish, then switch", onPress: () => void startInterfaceSwitch("drain") },
				{ text: "Stop and switch", style: "destructive", onPress: () => void startInterfaceSwitch("interrupt") },
			],
		);
	}, [interfaceBusy, interfaceSwitch, known?.activity, startInterfaceSwitch]);

	const requestInterfaceFailureRecovery = useCallback(() => {
		if (!interfaceFailureRecovery) return;
		interfaceFailureRecovery.confirm((policy) => void startInterfaceSwitch(policy));
	}, [interfaceFailureRecovery, startInterfaceSwitch]);

	// The browser toggle lives in the nav bar, beside the session name, to keep the
	// status row uncluttered. Separate from the headerLeft effect above because
	// `toggleBrowser` is declared here — referencing it in that effect's dep array
	// would read it before initialisation.
	useLayoutEffect(() => {
		if (shellOnly || !headerRightReady) {
			navigation.setOptions({ headerRight: undefined });
			return;
		}
		navigation.setOptions({
			headerRight: () => (
				<View style={styles.headerActions}>
					<Pressable
						hitSlop={10}
						accessibilityLabel="Open Chat interface"
						accessibilityState={{ busy: interfaceTransitionActive || interfaceSwitch.starting }}
						onPress={requestInterfaceSwitch}
						style={({ pressed }) => [styles.headerBrowserBtn, pressed && { opacity: 0.6 }]}
					>
						<Feather
							name={interfaceTransitionActive ? "repeat" : "message-square"}
							size={18}
							color={interfaceSwitch.status?.supported ? t.blue : t.textFaint}
						/>
					</Pressable>
					<Pressable
						hitSlop={12}
						accessibilityLabel={browserOpen ? "Close preview" : "Open preview"}
						onPress={toggleBrowser}
						style={({ pressed }) => [styles.headerBrowserBtn, pressed && { opacity: 0.6 }]}
					>
						<Feather
							name="globe"
							size={19}
							color={browserOpen ? t.blue : hasPreview ? t.green : t.textSecondary}
						/>
						{hasPreview && !browserOpen && <View style={styles.browserReadyDot} />}
					</Pressable>
				</View>
			),
		});
	}, [headerRightReady, navigation, browserOpen, hasPreview, toggleBrowser, styles, t, shellOnly, interfaceTransitionActive, interfaceSwitch.starting, interfaceSwitch.status?.supported, requestInterfaceSwitch]);

	const confirmKill = useCallback(() => {
		const doKill = async () => {
			try {
				const config = cfg ?? (await loadConfig());
				if (shellOnly) await closeShellTerminal(config, id);
				else await killSession(config, id);
				haptics.success();
				leave();
			} catch (e) {
				haptics.error();
				setBanner(`Kill failed: ${e instanceof Error ? e.message : String(e)}`);
			}
		};
		if (Platform.OS === "web") {
			doKill();
			return;
		}
		// Cautionary buzz as the destructive confirmation dialog is raised.
		haptics.warning();
		Alert.alert(shellOnly ? "Close shell?" : "Kill session?", shellOnly ? "This stops the worktree shell." : `This stops ${id}.`, [
			{ text: "Cancel", style: "cancel" },
			{ text: shellOnly ? "Close" : "Kill", style: "destructive", onPress: doKill },
		]);
	}, [cfg, id, leave, shellOnly]);

	// Restore a terminated session: the daemon re-attaches its worktree agent and
	// its PTY comes back, so we re-open the terminal once restore succeeds.
	const onRestore = useCallback(async () => {
		setRestoring(true);
		try {
			await restore(id);
			setBanner(null);
			setNotFound(false);
			openedRef.current = false;
			// Give the daemon a moment to bring the PTY up, then re-attach.
			setTimeout(() => {
				if (openedRef.current) return;
				openedRef.current = true;
				muxRef.current?.openTerminal(id, projectId);
				const d = lastDimsRef.current;
				if (d) muxRef.current?.resize(id, d.cols, d.rows, projectId);
			}, 1200);
		} catch (e) {
			setBanner(`Restore failed: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			setRestoring(false);
		}
	}, [restore, id, projectId]);

	const xtermOptions = useMemo(
		() => ({
			fontSize,
			cursorBlink: true,
			scrollback: 5000,
			// Move more rows per swipe so touch scrolling feels responsive.
			scrollSensitivity: 3,
			fastScrollSensitivity: 8,
			// The terminal's own palette, not the product one: agent TUIs own the
			// meaning of the ANSI slots.
			theme: terminalTheme(scheme),
			// Agent TUIs pick their colours assuming a dark canvas, so on the light
			// theme they can land white-on-white — the prompt row is exactly that
			// once its background band is collapsed away. xterm nudges any pair that
			// falls below this ratio until it is readable, which is cheaper and far
			// more reliable than trying to anticipate every colour a TUI might emit.
			minimumContrastRatio: 4.5,
		}),
		// `scheme` matters: without it the terminal keeps the palette it was built
		// with and stays dark after a theme switch.
		[fontSize, scheme],
	);

	// Zoom re-mounts the terminal at a new font size (see fontSize note above).
	// Reset open/size so the fresh mount re-attaches the PTY and re-reports dims.
	const zoom = useCallback((delta: number) => {
		setFontSize((f) => Math.min(20, Math.max(7, f + delta)));
		openedRef.current = false;
		setSize(null);
	}, []);

	const webViewOptions = useMemo(
		() => ({
			// Removes the extra "< > Done" / autofill bar iOS shows above the keyboard.
			hideKeyboardAccessoryView: true,
			// Custom drag/momentum scroll + input hardening (see TERMINAL_ENHANCE_JS).
			// Prepend the platform flag the enhance script branches on for scrolling.
			injectedJavaScript: `var IS_ANDROID=${Platform.OS === "android"};\n${TERMINAL_ENHANCE_JS}`,
			// NOTE: do NOT force androidLayerType:"hardware" here. xterm renders into a
			// <canvas>, and a hardware layer makes the Android WebView's render process
			// composite/crash blank on many devices (black terminal, no dims ever
			// reported). Leaving it at the default keeps the canvas visible.
			nestedScrollEnabled: true,
			// Surface an Android WebView render-process crash instead of a silent black
			// screen, so the user can tell the terminal died vs. never loaded.
			onRenderProcessGone: () => setBanner("Terminal renderer crashed - reopen the session (Back, then tap it again)."),
		}),
		[],
	);

	if (cfg && !isConfigured(cfg)) {
		return (
			<View style={styles.center}>
				<Text style={styles.bannerText}>No server configured.</Text>
			</View>
		);
	}

	// One inset for the whole dock. The root already reserves kbHeight, so the
	// dock owes nothing more while the keyboard is up — see dockInset.
	const bottomPad = dockInset(kbHeight, insets.bottom);

	return (
		<View style={[styles.screen, kbHeight > 0 && { paddingBottom: kbHeight }]}>
			<View style={styles.statusBar}>
				<View style={[styles.statusDot, { backgroundColor: statusColorFor(t)[status] }]} />
				<Text style={styles.statusText}>{statusLabel[status]}</Text>
				{size && !dead && (
					<Text style={styles.dims}>
						{size.cols}x{size.rows}
					</Text>
				)}
				{/* Zoom the terminal font: smaller = more rows/cols (see more of a TUI).
				    Grid geometry belongs beside the grid readout, not in the input dock. */}
				{!dead && (
					<View style={styles.zoomGroup}>
						<Pressable
							hitSlop={6}
							accessibilityLabel="Smaller text"
						onPress={() => { haptics.tap(); zoom(-1); }}
							style={({ pressed }) => [styles.zoomBtn, pressed && { opacity: 0.6 }]}
						>
							<Feather name="minus" size={13} color={t.textSecondary} />
						</Pressable>
						<View style={styles.zoomDivider} />
						<Pressable
							hitSlop={6}
							accessibilityLabel="Larger text"
						onPress={() => { haptics.tap(); zoom(1); }}
							style={({ pressed }) => [styles.zoomBtn, pressed && { opacity: 0.6 }]}
						>
							<Feather name="plus" size={13} color={t.textSecondary} />
						</Pressable>
					</View>
				)}
				{dead && !shellOnly ? (
					<Pressable
						hitSlop={8}
						onPress={() => { haptics.tap(); void onRestore(); }}
						disabled={restoring}
						style={({ pressed }) => [styles.restoreBtn, (pressed || restoring) && { opacity: 0.7 }]}
					>
						<Feather name="rotate-ccw" size={12} color={t.blue} />
						<Text style={styles.restoreText}>{restoring ? "Restoring..." : "Restore"}</Text>
					</Pressable>
				) : (
					// Icon-only: the trash glyph reads as "destroy this session" on its
					// own, so the label would only cost width. Hence accessibilityLabel.
					<Pressable
						hitSlop={8}
						accessibilityLabel={shellOnly ? "Close shell" : "Kill session"}
						onPress={confirmKill}
						style={({ pressed }) => [styles.killBtn, pressed && { opacity: 0.7 }]}
					>
						<Feather name={shellOnly ? "x" : "trash-2"} size={14} color={t.red} />
					</Pressable>
				)}
			</View>

			{banner && (
				<Pressable onPress={() => { haptics.tap(); setBanner(null); }} style={styles.banner}>
					<Text style={styles.bannerText}>{banner} (tap to dismiss)</Text>
				</Pressable>
			)}
			{interfaceTransitionNotice ? (
				<View style={styles.banner}>
					<Text style={styles.bannerText}>
						{interfaceTransitionNoticeText}
						{interfaceSwitch.acknowledgeNoticeError
							? ` Could not dismiss: ${interfaceSwitch.acknowledgeNoticeError}`
							: ""}
					</Text>
					<View style={styles.interfaceFailureActions}>
						{interfaceFailureRecovery ? (
							<Pressable
								accessibilityRole="button"
								onPress={() => {
									haptics.tap();
									requestInterfaceFailureRecovery();
								}}
							>
								<Text style={styles.interfaceFailureActionText}>{interfaceFailureRecovery.actionLabel}</Text>
							</Pressable>
						) : null}
						<Pressable
							accessibilityRole="button"
							accessibilityLabel="Dismiss interface switch error"
							disabled={interfaceSwitch.acknowledgingNotice}
							onPress={() => {
								haptics.tap();
								void interfaceSwitch
									.acknowledgeNotice(interfaceTransitionNotice.id)
									.catch(() => {});
							}}
						>
							<Text style={styles.interfaceFailureDismissText}>
								{interfaceSwitch.acknowledgingNotice ? "Dismissing…" : "Dismiss"}
							</Text>
						</Pressable>
					</View>
				</View>
			) : null}

			<View style={styles.termWrap}>
				<XtermJsWebView
					// Remount on a theme change as well as a font change: xterm applies
					// its palette at construction, so a live options swap leaves the
					// already-painted rows in the old colours.
					key={`term-${fontSize}-${scheme}`}
					ref={xtermRef}
					autoFit={false}
					xtermOptions={xtermOptions}
					webViewOptions={webViewOptions}
					logger={logger}
					onInitialized={onInitialized}
					onData={onData}
					style={{ flex: 1, backgroundColor: t.bgBase }}
				/>
				{interfaceTransitionActive ? (
					<View style={styles.interfaceOverlay}>
						<View style={styles.interfaceCard}>
							<Feather name="repeat" size={22} color={t.blue} />
							<Text style={styles.interfaceTitle}>Switching to Chat</Text>
							<Text style={styles.interfaceCopy}>{terminalInterfacePhaseLabel(interfaceSwitch.transition?.phase)}</Text>
							{mobileInterfaceTransitionIsCancellable(interfaceSwitch.transition) ? (
								<Pressable
									disabled={interfaceSwitch.cancelling}
									onPress={() => { haptics.tap(); void interfaceSwitch.cancel().catch(() => {}); }}
									style={styles.interfaceCancel}
								>
									<Text style={styles.interfaceCancelText}>{interfaceSwitch.cancelling ? "Cancelling…" : "Cancel switch"}</Text>
								</Pressable>
							) : null}
							{interfaceSwitch.error ? <Text style={styles.interfaceError}>{interfaceSwitch.error}</Text> : null}
						</View>
					</View>
				) : null}
				{dead && (
					<View style={styles.deadOverlay}>
						<View style={styles.deadIcon}>
							<Feather name="power" size={24} color={t.textTertiary} />
						</View>
						<Text style={styles.deadTitle}>{shellOnly ? "Shell closed" : "Session terminated"}</Text>
						<Text style={styles.deadMsg}>{shellOnly ? "This worktree shell is no longer running." : "This session has no live terminal. Restore it to bring the agent back."}</Text>
						{!shellOnly ? <Pressable
								onPress={() => { haptics.tap(); void onRestore(); }}
							disabled={restoring}
							style={({ pressed }) => [styles.restoreCta, (pressed || restoring) && { opacity: 0.8 }]}
						>
							<Feather name="rotate-ccw" size={16} color={t.onAccent} />
							<Text style={styles.restoreCtaText}>{restoring ? "Restoring..." : "Restore session"}</Text>
						</Pressable> : null}
					</View>
				)}

				{/* In-app browser overlay: the agent's generated preview file. Sits over
				    the terminal (which keeps running underneath) with its own bar. */}
				{browserOpen && preview && (
					<View style={styles.browserOverlay}>
						<View style={styles.browserBar}>
							<Feather name="globe" size={13} color={t.textTertiary} />
							<Text style={styles.browserPath} numberOfLines={1}>
								{preview.entry}
							</Text>
							<Pressable hitSlop={8} onPress={() => { haptics.tap(); previewWebRef.current?.reload(); }} style={styles.browserAction}>
								<Feather name="rotate-cw" size={15} color={t.blue} />
							</Pressable>
							<Pressable hitSlop={8} onPress={() => { haptics.tap(); setBrowserOpen(false); }} style={styles.browserAction}>
								<Feather name="x" size={17} color={t.textSecondary} />
							</Pressable>
						</View>
						<WebView
							ref={previewWebRef}
							// The preview route lives behind the daemon's connection-password
							// auth (Bearer). Without this header the WebView's request 401s and
							// renders the JSON error body instead of the page. cfg carries the
							// password we paired with; authHeaders() turns it into the Bearer.
							source={{ uri: preview.url, headers: cfg ? authHeaders(cfg) : undefined }}
							originWhitelist={["*"]}
							style={styles.browserWeb}
							onError={() => setBanner("Preview failed to load.")}
						/>
					</View>
				)}
			</View>

			{/* The input dock. One container, one bottom inset, fixed slots — every
			    control sits in the same place in every state the screen can reach. */}
			<View style={[styles.dock, { paddingBottom: bottomPad }]}>
				{/* Live dictation readout. Deliberately not inside the field: the
				    partial transcript changes on every syllable, and rewriting the
				    input under the user's caret is hostile. */}
				{(voice.state === "starting" || voice.state === "recording" || voice.state === "transcribing") && (
					<View style={[styles.voiceStrip, voice.state === "starting" && styles.voiceStripWarmup]}>
						<Feather name="mic" size={13} color={voice.state === "starting" ? t.textTertiary : t.blue} />
						<Text style={styles.voiceText} numberOfLines={2}>
							{voice.partial ||
								(voice.state === "starting"
									? // The mic is not capturing yet. Saying "Listening" here would
										// invite speech that gets dropped during warm-up.
										"Keep holding..."
									: voice.state === "transcribing"
										? "Transcribing..."
										: voice.mode === "latched"
											? "Recording hands-free - tap the mic to finish"
											: "Speak now - release to insert")}
						</Text>
					</View>
				)}

				<KeyRow onKey={sendKey} />

				<Composer
					value={msg}
					onChangeText={setMsg}
					onSend={sendPrompt}
					sending={sending || interfaceTransitionActive}
					target={sendTarget}
					onTargetChange={setSendTarget}
					voice={voice}
					keyboardVisible={kbVisible}
					onDismissKeyboard={Keyboard.dismiss}
					targetLocked={shellOnly}
				/>
			</View>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	center: {
		flex: 1,
		alignItems: "center",
		justifyContent: "center",
		backgroundColor: t.bgBase,
	},
	statusBar: {
		flexDirection: "row",
		alignItems: "center",
		paddingHorizontal: 14,
		paddingVertical: 6,
		borderBottomWidth: 1,
		borderBottomColor: t.borderSubtle,
	},
	statusDot: { width: 8, height: 8, borderRadius: 4, marginRight: 8 },
	statusText: { color: t.textSecondary, fontSize: 12, flex: 1 },
	dims: { color: t.textTertiary, fontSize: 11, fontFamily: t.fontMono },
	zoomGroup: {
		flexDirection: "row",
		alignItems: "center",
		marginLeft: 10,
		borderWidth: 1,
		borderColor: t.borderDefault,
		borderRadius: 7,
		backgroundColor: t.bgElevated,
		overflow: "hidden",
	},
	zoomBtn: { width: 28, height: 24, alignItems: "center", justifyContent: "center" },
	zoomDivider: { width: 1, height: 24, backgroundColor: t.borderDefault },
	banner: {
		backgroundColor: t.bgElevated,
		paddingHorizontal: 14,
		paddingVertical: 8,
		borderBottomWidth: 1,
		borderBottomColor: t.borderDefault,
	},
	bannerText: { color: t.attention, fontSize: 12 },
	interfaceFailureActions: {
		marginTop: 8,
		flexDirection: "row",
		alignItems: "center",
		justifyContent: "flex-end",
		gap: 16,
	},
	interfaceFailureActionText: { color: t.red, fontSize: 12, fontWeight: "700" },
	interfaceFailureDismissText: { color: t.textSecondary, fontSize: 12, fontWeight: "600" },
	termWrap: { flex: 1, backgroundColor: t.bgBase },
	dock: {
		borderTopWidth: 1,
		borderTopColor: t.borderSubtle,
		backgroundColor: t.bgSurface,
	},
	// Square-ish now that it holds a glyph and no label.
	killBtn: {
		alignItems: "center",
		justifyContent: "center",
		backgroundColor: t.tintRed,
		borderRadius: 12,
		paddingHorizontal: 8,
		paddingVertical: 5,
		marginLeft: 12,
	},
	// Bare glyph in the nav bar: iOS draws its own round box behind header buttons,
	// so a tinted pill here would fight it. Colour carries the state instead.
	// A fixed square with centred content, not padding — asymmetric padding leaves
	// the glyph visibly off-centre inside the circle iOS draws around it.
	headerBrowserBtn: {
		width: 35,
		height: 30,
		alignItems: "center",
		justifyContent: "center",
	},
	headerActions: { flexDirection: "row", alignItems: "center" },
	interfaceOverlay: {
		...StyleSheet.absoluteFillObject,
		alignItems: "center",
		justifyContent: "center",
		paddingHorizontal: 24,
		backgroundColor: t.scrim,
	},
	interfaceCard: {
		width: "100%",
		maxWidth: 340,
		alignItems: "center",
		gap: 10,
		paddingHorizontal: 22,
		paddingVertical: 24,
		borderRadius: 16,
		borderWidth: 1,
		borderColor: t.borderDefault,
		backgroundColor: t.bgSurface,
	},
	interfaceTitle: { color: t.textPrimary, fontSize: 16, fontWeight: "700" },
	interfaceCopy: { color: t.textSecondary, fontSize: 12, lineHeight: 18, textAlign: "center" },
	interfaceCancel: {
		marginTop: 4,
		borderRadius: 9,
		borderWidth: 1,
		borderColor: t.borderDefault,
		paddingHorizontal: 13,
		paddingVertical: 8,
	},
	interfaceCancelText: { color: t.textPrimary, fontSize: 12, fontWeight: "600" },
	interfaceError: { color: t.red, fontSize: 11, lineHeight: 16, textAlign: "center" },
	// Small green badge on the globe when a real preview is available. Offsets are
	// from the square's centre, so it rides the glyph rather than the button frame.
	browserReadyDot: {
		position: "absolute",
		top: 4,
		right: 3,
		width: 8,
		height: 8,
		borderRadius: 4,
		backgroundColor: t.green,
		borderWidth: 1,
		borderColor: t.bgSurface,
	},
	browserOverlay: { ...StyleSheet.absoluteFillObject, backgroundColor: t.bgBase },
	browserBar: {
		flexDirection: "row",
		alignItems: "center",
		gap: 10,
		paddingHorizontal: 12,
		paddingVertical: 8,
		backgroundColor: t.bgSurface,
		borderBottomWidth: 1,
		borderBottomColor: t.borderSubtle,
	},
	browserPath: { flex: 1, color: t.textSecondary, fontFamily: t.fontMono, fontSize: 12 },
	browserAction: { paddingHorizontal: 4, paddingVertical: 2 },
	browserWeb: { flex: 1, backgroundColor: "#ffffff" },
	restoreBtn: {
		flexDirection: "row",
		alignItems: "center",
		gap: 4,
		backgroundColor: t.tintBlue,
		borderRadius: 12,
		paddingHorizontal: 11,
		paddingVertical: 4,
		marginLeft: 12,
	},
	restoreText: { color: t.blue, fontWeight: "700", fontSize: 12 },
	deadOverlay: {
		...StyleSheet.absoluteFillObject,
		alignItems: "center",
		justifyContent: "center",
		padding: 32,
		gap: 10,
		backgroundColor: t.bgBase,
	},
	deadIcon: {
		width: 64,
		height: 64,
		borderRadius: 18,
		backgroundColor: t.bgElevated,
		borderWidth: 1,
		borderColor: t.borderSubtle,
		alignItems: "center",
		justifyContent: "center",
		marginBottom: 6,
	},
	deadTitle: { color: t.textPrimary, fontSize: 17, fontWeight: "700", textAlign: "center" },
	deadMsg: { color: t.textSecondary, fontSize: 13, lineHeight: 20, textAlign: "center", maxWidth: 300 },
	restoreCta: {
		flexDirection: "row",
		alignItems: "center",
		gap: 8,
		backgroundColor: t.blue,
		borderRadius: 10,
		paddingVertical: 12,
		paddingHorizontal: 20,
		marginTop: 10,
	},
	restoreCtaText: { color: t.onAccent, fontSize: 15, fontWeight: "700" },
	// Accent-blue like the rest of the chrome. Red is reserved for the mic button
	// alone: one small saturated element reads as "recording", a whole red panel
	// reads as an error. It sits at the top of the dock, so it divides itself from
	// the keys below rather than redrawing the dock's own top border.
	voiceStrip: {
		flexDirection: "row",
		alignItems: "center",
		gap: 8,
		paddingHorizontal: 12,
		paddingVertical: 7,
		backgroundColor: t.tintBlue,
		borderBottomWidth: 1,
		borderBottomColor: t.blue,
	},
	// Muted while the mic warms up, so "ready to speak" is a visible state change
	// and not just a wording difference.
	voiceStripWarmup: { backgroundColor: t.bgElevated, borderBottomColor: t.borderDefault },
	voiceText: { flex: 1, color: t.textPrimary, fontSize: 13 },
});
