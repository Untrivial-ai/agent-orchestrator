import type { MobileBrowserCommand, MobileBrowserCommandResult } from "./mobileBrowserRuntime";

const MESSAGE_KEY = "__aoMobileBrowser";
const APPEARANCE_KEY = "__aoMobileBrowserAppearance";

export type BrowserContentAppearance = "light" | "dark";

export type BrowserBridgeMessage = {
	__aoMobileBrowser: true;
	requestId: string;
	ok: boolean;
	result?: Record<string, unknown>;
	error?: { code: string; message: string };
};

// Samples the rendered page rather than the AO theme so native glass can use
// dark ink over bright pages and light ink over dark pages without a fixed tint.
export const MOBILE_BROWSER_APPEARANCE_SCRIPT = `
(function () {
  function rgba(value) {
    var match = String(value || '').match(/rgba?\\(([^)]+)\\)/i);
    if (!match) return null;
    var parts = match[1].split(',').map(Number);
    if (parts.length < 3 || parts.some(function (part) { return !Number.isFinite(part); })) return null;
    return { r: parts[0], g: parts[1], b: parts[2], a: parts.length > 3 ? parts[3] : 1 };
  }
  function report() {
    var node = document.body, color = null;
    while (node && !color) {
      var candidate = rgba(window.getComputedStyle(node).backgroundColor);
      if (candidate && candidate.a > 0.05) color = candidate;
      node = node.parentElement;
    }
    var appearance = 'light';
    if (color) {
      function channel(value) { value /= 255; return value <= 0.04045 ? value / 12.92 : Math.pow((value + 0.055) / 1.055, 2.4); }
      appearance = 0.2126 * channel(color.r) + 0.7152 * channel(color.g) + 0.0722 * channel(color.b) < 0.42 ? 'dark' : 'light';
    } else if (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) appearance = 'dark';
    window.ReactNativeWebView.postMessage(JSON.stringify({ ${JSON.stringify(APPEARANCE_KEY)}: appearance }));
  }
  report();
  clearTimeout(window.__aoAppearanceTimer);
  window.__aoAppearanceTimer = setTimeout(report, 250);
  return true;
})(); true;
`;

export function parseBrowserContentAppearance(raw: string): BrowserContentAppearance | undefined {
	try {
		const value = (JSON.parse(raw) as Record<string, unknown>)[APPEARANCE_KEY];
		return value === "light" || value === "dark" ? value : undefined;
	} catch {
		return undefined;
	}
}

// Installed in every document before its content loads. It intentionally uses
// no eval and exposes only the bounded command vocabulary implemented below.
export const MOBILE_BROWSER_BOOTSTRAP = `
(function () {
  if (window.__aoMobileBrowserBridge) return true;
  var refs = Object.create(null), refInfo = Object.create(null);
  var generation = 0;
  function visible(el) {
    if (!el || !el.getBoundingClientRect) return false;
    var r = el.getBoundingClientRect(), s = window.getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
  }
  function normalizedText(value, limit) { return String(value || '').replace(/\\s+/g, ' ').trim().slice(0, limit); }
  function clean(value) { return normalizedText(value, 240); }
  function readableText(value) { return normalizedText(value, 20000); }
  function role(el) {
    return el.getAttribute('role') || ({A:'link',BUTTON:'button',INPUT:el.type === 'checkbox' ? 'checkbox' : 'textbox',TEXTAREA:'textbox',SELECT:'combobox'}[el.tagName] || el.tagName.toLowerCase());
  }
  function name(el) { return clean(el.getAttribute('aria-label') || el.getAttribute('alt') || el.getAttribute('placeholder') || el.innerText || el.value || el.title); }
  function snapshot(interactive) {
    generation += 1; refs = Object.create(null); refInfo = Object.create(null);
    var selector = interactive ? 'a,button,input,textarea,select,[role],[tabindex]' : 'a,button,input,textarea,select,[role],[tabindex],h1,h2,h3,p,li';
    var nodes = Array.prototype.slice.call(document.querySelectorAll(selector));
    var lines = [], count = 0;
    nodes.forEach(function (el) {
      if (lines.length >= 500 || !visible(el)) return;
      var interactiveEl = el.matches('a,button,input,textarea,select,[role],[tabindex]');
      var label = name(el); if (!label) return;
      var ref = '';
      if (interactiveEl) { ref = 'e' + (++count); refs[ref] = el; refInfo[ref] = { role: role(el), name: label }; }
      lines.push('- ' + role(el) + ' "' + label.replace(/"/g, '\\"') + '"' + (ref ? ' [ref=' + ref + ']' : ''));
    });
    return { text: lines.join('\\n'), refs: refInfo, generation: generation, url: location.href, title: document.title };
  }
  function target(ref) {
    var el = refs[String(ref || '')];
    if (!el || !el.isConnected) { var e = new Error('Element reference is stale; take another snapshot.'); e.code = 'STALE_REFERENCE'; throw e; }
    return el;
  }
  function inputValue(el, value) {
    var proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    var setter = Object.getOwnPropertyDescriptor(proto, 'value');
    if (setter && setter.set) setter.set.call(el, value); else el.value = value;
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
  }
  function typeAtSelection(el, value) {
    if ((el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA') ||
        typeof el.selectionStart !== 'number' || typeof el.selectionEnd !== 'number' ||
        typeof el.setSelectionRange !== 'function') {
      var unsupported = new Error('Typing at the current cursor position is not available for this element.');
      unsupported.code = 'BROWSER_ACTION_UNSUPPORTED';
      throw unsupported;
    }
    var start = el.selectionStart, end = el.selectionEnd, current = String(el.value || '');
    inputValue(el, current.slice(0, start) + value + current.slice(end));
    var caret = start + value.length;
    el.setSelectionRange(caret, caret);
  }
  function wait(args) {
    var timeout = Math.min(Number(args.timeoutMs || 10000), 55000), started = Date.now();
    var stableFor = Math.max(Number(args.stableMs || 0), 0), lastMutation = started, observer = null;
    return new Promise(function (resolve, reject) {
      function finish(value, error) {
        if (observer) observer.disconnect();
        if (error) reject(error); else resolve(value);
      }
      if (stableFor > 0) {
        observer = new MutationObserver(function () { lastMutation = Date.now(); });
        observer.observe(document.documentElement || document.body, {
          subtree: true,
          childList: true,
          attributes: true,
          characterData: true
        });
      }
      function check() {
        var body = document.body ? document.body.innerText : '';
        var ok = args.ms ? Date.now() - started >= Number(args.ms) :
          args.text ? body.indexOf(String(args.text)) >= 0 :
          args.textGone ? body.indexOf(String(args.textGone)) < 0 :
          args.selector ? !!document.querySelector(String(args.selector)) :
          args.selectorGone ? !document.querySelector(String(args.selectorGone)) :
          args.url ? location.href.indexOf(String(args.url)) >= 0 :
          args.load ? document.readyState === 'complete' :
          stableFor > 0 ? Date.now() - lastMutation >= stableFor : false;
        if (ok) return finish({ matched: true, url: location.href });
        if (Date.now() - started >= timeout) { var e = new Error('Timed out waiting for page condition.'); e.code = 'WAIT_TIMEOUT'; return finish(null, e); }
        setTimeout(check, 100);
      }
      check();
    });
  }
  window.__aoMobileBrowserBridge = {
    run: function (command) {
      var a = command.args || {}, result;
      try {
        switch (command.action) {
          case 'snapshot': result = snapshot(!!a.interactive); break;
          case 'click': target(a.ref).click(); result = { clicked: a.ref }; break;
          case 'dblclick': target(a.ref).dispatchEvent(new MouseEvent('dblclick', { bubbles:true, cancelable:true, view:window })); result = { clicked: a.ref }; break;
          case 'focus': target(a.ref).focus(); result = { focused: a.ref }; break;
          case 'hover': target(a.ref).dispatchEvent(new MouseEvent('mouseover', { bubbles:true, cancelable:true, view:window })); result = { hovered:a.ref }; break;
          case 'fill': var f=target(a.ref); f.focus(); inputValue(f, String(a.text || a.value || '')); result={ filled:a.ref }; break;
          case 'type': var t=target(a.ref); t.focus(); typeAtSelection(t, String(a.text || a.value || '')); result={ typed:a.ref }; break;
          case 'check': var c=target(a.ref); if(!c.checked)c.click(); result={ checked:a.ref }; break;
          case 'uncheck': var u=target(a.ref); if(u.checked)u.click(); result={ unchecked:a.ref }; break;
          case 'press':
            var unsupportedKey = new Error('Native key presses are not available on the mobile browser surface.');
            unsupportedKey.code = 'BROWSER_ACTION_UNSUPPORTED';
            throw unsupportedKey;
          case 'scroll': var amount=Number(a.amount || 500), x=0,y=0; if(a.direction==='up')y=-amount;else if(a.direction==='left')x=-amount;else if(a.direction==='right')x=amount;else y=amount; window.scrollBy({left:x,top:y,behavior:'smooth'}); result={ scrolled:true }; break;
          case 'scrollintoview': target(a.ref).scrollIntoView({block:'center',behavior:'smooth'}); result={ scrolled:a.ref }; break;
          case 'get': {
            var property = String(a.property || '').toLowerCase(), value;
            switch (property) {
              case 'url':
                if (a.ref) { var urlRef = new Error('url does not accept an element ref'); urlRef.code = 'INVALID_ARGUMENT'; throw urlRef; }
                value = location.href;
                break;
              case 'title':
                if (a.ref) { var titleRef = new Error('title does not accept an element ref'); titleRef.code = 'INVALID_ARGUMENT'; throw titleRef; }
                value = document.title;
                break;
              case 'text': {
                var textTarget = a.ref ? target(a.ref) : document.body;
                value = textTarget ? readableText(textTarget.innerText || textTarget.textContent) : '';
                break;
              }
              case 'value':
                if (!a.ref) { var valueRef = new Error('value requires an element ref'); valueRef.code = 'REFERENCE_REQUIRED'; throw valueRef; }
                value = target(a.ref).value;
                break;
              case 'checked':
                if (!a.ref) { var checkedRef = new Error('checked requires an element ref'); checkedRef.code = 'REFERENCE_REQUIRED'; throw checkedRef; }
                value = !!target(a.ref).checked;
                break;
              default:
                var invalidProperty = new Error('Unsupported browser property: ' + property);
                invalidProperty.code = 'INVALID_ARGUMENT';
                throw invalidProperty;
            }
            result = { value: value };
            break;
          }
          case 'wait': return wait(a).then(function(v){ post(command.requestId,true,v); },function(e){ post(command.requestId,false,null,e); });
          default: var unsupported=new Error('This command is not available on the mobile browser yet.'); unsupported.code='BROWSER_ACTION_UNSUPPORTED'; throw unsupported;
        }
        post(command.requestId, true, result || {});
      } catch (error) { post(command.requestId, false, null, error); }
    }
  };
  function post(requestId, ok, result, error) {
    window.ReactNativeWebView.postMessage(JSON.stringify({ ${JSON.stringify(MESSAGE_KEY)}: true, requestId: requestId, ok: ok, result: result, error: error ? { code:error.code || 'MOBILE_BROWSER_COMMAND_FAILED', message:error.message || String(error) } : undefined }));
  }
  return true;
})(); true;
`;

export function browserCommandScript(command: MobileBrowserCommand): string {
	return `${MOBILE_BROWSER_BOOTSTRAP}\nwindow.__aoMobileBrowserBridge && window.__aoMobileBrowserBridge.run(${JSON.stringify(command)}); true;`;
}

export function parseBrowserBridgeMessage(raw: string): BrowserBridgeMessage | undefined {
	try {
		const message = JSON.parse(raw) as Partial<BrowserBridgeMessage>;
		if (message[MESSAGE_KEY] !== true || typeof message.requestId !== "string" || typeof message.ok !== "boolean") return undefined;
		return message as BrowserBridgeMessage;
	} catch {
		return undefined;
	}
}

export function bridgeResult(message: BrowserBridgeMessage): MobileBrowserCommandResult {
	return message.ok
		? { ok: true, result: message.result ?? {} }
		: { ok: false, error: message.error ?? { code: "MOBILE_BROWSER_COMMAND_FAILED", message: "Mobile browser command failed" } };
}
