// The page's whole client: post a message, read the stream, put each HTML
// fragment where it says. The server renders everything; this file only
// knows slots, ids, and how to answer an ask() form.
(function () {
  'use strict';

  // The page names the session, not the server: a reply to ask() arrives
  // while the run is still streaming, before the run could hand one back.
  const sessionId = crypto.randomUUID();
  const turns = document.getElementById('turns');
  const composer = document.getElementById('composer');
  const input = composer.querySelector('textarea');
  const sendButton = composer.querySelector('button');
  let running = false;

  const nodes = (html) => {
    const t = document.createElement('template');
    t.innerHTML = html;
    return t.content;
  };

  // A chart lives on outside the DOM — ApexCharts keeps resize listeners
  // and its instance — so a block that is replaced has its charts torn down
  // first. tablerCharts.destroy also drops charts still queued to be drawn;
  // the fallback, for a tabler-templ before it, reaches for the instance the
  // script hangs on the element and leaves the queue alone.
  const dropCharts = (el) => {
    if (window.tablerCharts && window.tablerCharts.destroy) return window.tablerCharts.destroy(el);
    el.querySelectorAll('[data-tabler-chart]').forEach((c) => {
      try { c.tablerChart && c.tablerChart.destroy(); } catch (_) { /* already gone */ }
    });
  };

  const currentTurn = () => {
    const all = turns.querySelectorAll('.turn');
    return all[all.length - 1];
  };

  // apply places one fragment. An id the page already shows is replaced:
  // in place within the current turn (a step getting its result, a table
  // growing), and moved here from an earlier turn, so a revised chart shows
  // next to the question that revised it rather than up the page where
  // nobody is looking.
  function apply(f) {
    if (f.slot === 'turn') {
      document.getElementById('intro')?.remove();
      turns.append(nodes(f.html));
      currentTurn().scrollIntoView({ block: 'start', behavior: 'smooth' });
      return;
    }
    const turn = currentTurn();
    const slot = turn && turn.querySelector(`[data-slot="${f.slot}"]`);
    if (!slot) return;
    const frag = nodes(f.html);
    const added = [...frag.children];
    const old = f.id ? document.getElementById(f.id) : null;
    if (f.mode === 'set') {
      dropCharts(slot);
      slot.replaceChildren(frag);
    } else if (old && turn.contains(old)) {
      // A step the reader opened stays open when its result arrives.
      if (old.open && added[0]) added[0].open = true;
      dropCharts(old);
      old.replaceWith(frag);
    } else {
      if (old) { dropCharts(old); old.remove(); }
      slot.append(frag);
    }
    if (window.tablerCharts) added.forEach((el) => window.tablerCharts.init(el));
  }

  // EventSource cannot POST, and a message does not belong in a URL, so the
  // stream is read from fetch by hand. Only `data:` lines matter: the JSON
  // carries its own type.
  async function run(message) {
    const res = await fetch('/api/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message, sessionId }),
    });
    if (!res.ok || !res.body) throw new Error(`${res.status} ${await res.text()}`);
    const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += value;
      let i;
      while ((i = buf.indexOf('\n\n')) >= 0) {
        const frame = buf.slice(0, i);
        buf = buf.slice(i + 2);
        const data = frame.split('\n').filter((l) => l.startsWith('data: ')).map((l) => l.slice(6)).join('\n');
        if (!data) continue;
        const e = JSON.parse(data);
        if (e.type === 'html') apply(e.args);
      }
    }
  }

  async function send(message) {
    message = message.trim();
    if (!message || running) return;
    running = true;
    input.value = '';
    input.disabled = sendButton.disabled = true;
    try {
      await run(message);
    } catch (err) {
      const turn = currentTurn();
      const status = turn && turn.querySelector('[data-slot="status"]');
      if (status) status.textContent = String(err);
    } finally {
      running = false;
      input.disabled = sendButton.disabled = false;
      input.focus();
    }
  }

  composer.addEventListener('submit', (e) => {
    e.preventDefault();
    send(input.value);
  });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send(input.value);
    }
  });
  document.addEventListener('click', (e) => {
    const ex = e.target.closest('[data-example]');
    if (ex) send(ex.dataset.example);
  });

  // answerOf reads an ask() form into the value ask() expects, or returns
  // a message naming what is still missing. ask() checks the reply again
  // on the server; this check only saves the reader a round trip.
  function answerOf(form, submitter) {
    switch (form.dataset.inputType) {
      case 'confirm':
        return { value: submitter && submitter.value === 'true' };
      case 'choice':
        return { value: submitter ? submitter.value : '' };
      case 'questions': {
        const value = {};
        for (const fs of form.querySelectorAll('fieldset[data-question]')) {
          const id = fs.dataset.question;
          const multi = fs.dataset.multi === 'true';
          const other = (fs.querySelector('.ask-other')?.value ?? '').trim();
          const picked = [];
          for (const box of fs.querySelectorAll('input.form-selectgroup-input:checked')) {
            if (box.dataset.other) { if (other) picked.push(other); } else picked.push(box.value);
          }
          if (!picked.length) {
            const label = fs.dataset.label || id;
            return { missing: `Answer “${label}” first.` };
          }
          value[id] = multi ? picked : picked[0];
        }
        return { value };
      }
      default:
        return { value: form.elements.text.value };
    }
  }

  document.addEventListener('submit', async (e) => {
    const form = e.target.closest('form[data-ask]');
    if (!form) return;
    e.preventDefault();
    const { value, missing } = answerOf(form, e.submitter);
    const error = form.querySelector('.ask-error');
    if (missing) {
      if (error) error.textContent = missing;
      return;
    }
    form.querySelectorAll('button').forEach((b) => (b.disabled = true));
    const requestId = form.dataset.ask;
    const res = await fetch('/api/reply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId, requestId, value }),
    });
    if (!res.ok) {
      form.querySelectorAll('button').forEach((b) => (b.disabled = false));
      if (error) error.textContent = await res.text();
      return;
    }
    document.getElementById('ask-' + requestId)?.replaceWith(nodes(await res.text()));
  });
})();
