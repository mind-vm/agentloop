package browser

// somJS defines window.__som and is prepended to every mark-related
// evaluation. The guard at the top makes that cheap and idempotent: a
// second injection into the same document returns immediately, and a
// document that has been replaced by a navigation gets a fresh install
// without the Go side having to track document identity.
//
// Two behaviours differ from the usual sketch of this technique, both
// so that the ids a model is given stay resolvable:
//
//   - install() clears the element map before rebuilding it, so a
//     re-mark of a page that lost elements cannot leave a higher id
//     from the previous pass pointing at a stale node.
//   - uninstall() removes only the visual overlay and keeps the map.
//     askMarks() has to strip the boxes before the next screenshot, and
//     it would be a trap if doing so silently invalidated the very ids
//     the model just answered with.
const somJS = `
(() => {
  if (window.__som) return;

  const SEL = [
    'a[href]', 'button', 'input:not([type=hidden])', 'select', 'textarea',
    '[role=button]', '[role=link]', '[role=tab]', '[role=menuitem]',
    '[role=checkbox]', '[role=radio]', '[role=switch]', '[role=textbox]',
    '[onclick]', '[contenteditable=""]', '[contenteditable=true]',
    'summary', 'label[for]',
  ].join(',');

  const STYLE_ID = '__som_style__';
  const LAYER_ID = '__som_layer__';

  function isVisible(el) {
    const r = el.getBoundingClientRect();
    if (r.width < 4 || r.height < 4) return false;
    const cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || cs.display === 'none' || cs.opacity === '0') return false;
    if (el.disabled) return false;
    const vw = innerWidth, vh = innerHeight;
    if (r.bottom < 0 || r.right < 0 || r.left > vw || r.top > vh) return false;
    return true;
  }

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const s = document.createElement('style');
    s.id = STYLE_ID;
    s.textContent =
      '#' + LAYER_ID + '{position:fixed;inset:0;pointer-events:none;z-index:2147483647;}' +
      '#' + LAYER_ID + ' .__som_box{position:absolute;outline:2px solid #ff2d2d;' +
      'background:rgba(255,45,45,0.06);box-sizing:border-box;border-radius:2px;}' +
      '#' + LAYER_ID + ' .__som_tag{position:absolute;top:-1px;left:-1px;' +
      'background:#ff2d2d;color:white;font:bold 11px/1 ui-monospace,Menlo,monospace;' +
      'padding:2px 5px;border-radius:2px 0 4px 0;}';
    document.documentElement.appendChild(s);
  }

  function rectOf(el) {
    const r = el.getBoundingClientRect();
    return { x: r.left, y: r.top, w: r.width, h: r.height,
             cx: r.left + r.width / 2, cy: r.top + r.height / 2 };
  }

  function describe(el) {
    const tag = el.tagName.toLowerCase();
    const role = el.getAttribute('role') || '';
    const name = (el.getAttribute('aria-label') ||
                  el.getAttribute('alt') ||
                  el.getAttribute('title') ||
                  el.getAttribute('placeholder') ||
                  el.value ||
                  el.innerText || '').trim().replace(/\s+/g, ' ').slice(0, 80);
    return { tag, role, name };
  }

  const api = {
    elements: [],
    removeLayer() {
      const layer = document.getElementById(LAYER_ID);
      if (layer) layer.remove();
    },
    install() {
      this.removeLayer();
      this.elements = [];
      ensureStyle();
      const layer = document.createElement('div');
      layer.id = LAYER_ID;
      document.documentElement.appendChild(layer);

      const seen = new Set();
      const marks = [];
      const all = document.querySelectorAll(SEL);
      let id = 0;
      for (const el of all) {
        if (seen.has(el) || !isVisible(el)) continue;
        seen.add(el);
        id++;
        this.elements[id] = el;
        const r = rectOf(el);
        const d = describe(el);

        const box = document.createElement('div');
        box.className = '__som_box';
        box.style.left = r.x + 'px';
        box.style.top = r.y + 'px';
        box.style.width = r.w + 'px';
        box.style.height = r.h + 'px';

        const tag = document.createElement('div');
        tag.className = '__som_tag';
        tag.textContent = id;
        box.appendChild(tag);
        layer.appendChild(box);

        marks.push({ id, tag: d.tag, role: d.role, name: d.name,
                     x: Math.round(r.cx), y: Math.round(r.cy),
                     w: Math.round(r.w), h: Math.round(r.h) });
      }
      return marks;
    },
    uninstall() { this.removeLayer(); },
    get(id) { return this.elements[id]; },
    click(id) {
      const el = this.elements[id];
      if (!el) throw new Error('no mark ' + id);
      el.scrollIntoView({ block: 'center', inline: 'center' });
      if (typeof el.click === 'function') el.click();
      else el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    },
    focus(id) {
      const el = this.elements[id];
      if (!el) throw new Error('no mark ' + id);
      el.scrollIntoView({ block: 'center', inline: 'center' });
      if (typeof el.focus === 'function') el.focus();
    },
  };

  Object.defineProperty(window, '__som', { value: api, configurable: true });
})();
`
