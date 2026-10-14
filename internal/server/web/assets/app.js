// NetPulse dashboard.
//
// Polls /api/v1/status every few seconds and re-renders. Every piece of text
// is inserted with textContent (never innerHTML) because summaries and error
// messages originate from remote servers and must be treated as untrusted.
'use strict';

const REFRESH_MS = 5000;
const STALE_MS = 3 * REFRESH_MS;

const state = {
  data: null,
  filter: 'all',
  group: null,
  query: '',
  open: new Set(),
  lastOk: 0,
  first: true,
};

const $ = (id) => document.getElementById(id);

// h builds an element: h('div', {class: 'x', text: 'hi', onclick}, ...children)
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

function s(tag, attrs = {}) {
  const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  return el;
}

// ---------- formatting ----------

function fmtMs(ms) {
  if (!ms || ms <= 0) return '–';
  if (ms < 1) return `${Math.round(ms * 1000)}µs`;
  if (ms < 10) return `${ms.toFixed(1)}ms`;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

function ago(iso) {
  const t = new Date(iso).getTime();
  if (!t || t < 0) return 'never';
  const sec = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
}

const statusClass = (st) => (st === 'ok' || st === 'warn' || st === 'fail' ? st : 'pending');
const statusLabel = (st) => ({ ok: 'Healthy', warn: 'Degraded', fail: 'Failing' }[st] || 'Pending');

function median(xs) {
  if (!xs.length) return 0;
  const a = [...xs].sort((x, y) => x - y);
  const m = Math.floor(a.length / 2);
  return a.length % 2 ? a[m] : (a[m - 1] + a[m]) / 2;
}

// ---------- components ----------

function pill(st) {
  return h('span', { class: `pill ${statusClass(st)}`, text: statusLabel(st) });
}

// sparkline draws latency history as an area chart. Failed and slow runs
// are marked with coloured dots so incidents stand out at a glance.
function sparkline(history) {
  const W = 88, H = 26, P = 3;
  const svg = s('svg', { class: 'spark', viewBox: `0 0 ${W} ${H}`, 'aria-hidden': 'true' });
  const pts = (history || []).slice(-40);
  if (pts.length === 0) return svg;
  if (pts.length === 1) {
    svg.append(s('circle', { class: 'last', cx: W - P, cy: H / 2, r: 2.4 }));
    return svg;
  }
  const maxMs = Math.max(...pts.map((p) => p.ms), 1) * 1.15;
  const x = (i) => P + (i * (W - 2 * P)) / (pts.length - 1);
  const y = (ms) => H - P - (ms / maxMs) * (H - 2 * P);

  const line = pts.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(p.ms).toFixed(1)}`).join(' ');
  svg.append(s('path', { class: 'area', d: `${line} L${x(pts.length - 1).toFixed(1)},${H} L${x(0).toFixed(1)},${H} Z` }));
  svg.append(s('path', { class: 'line', d: line }));
  pts.forEach((p, i) => {
    if (p.s === 'warn' || p.s === 'fail') {
      svg.append(s('circle', { class: p.s, cx: x(i), cy: y(p.ms), r: 2.2 }));
    }
  });
  const last = pts.length - 1;
  svg.append(s('circle', { class: 'last', cx: x(last), cy: y(pts[last].ms), r: 2.4 }));
  return svg;
}

// waterfall renders phases (dns → connect → tls → server → transfer) as a
// proportional stacked bar, like a browser devtools network timing view.
function waterfall(phases) {
  if (!phases || !phases.length) return null;
  const total = phases.reduce((acc, p) => acc + p.ms, 0) || 1;
  const bar = h('div', { class: 'wf-bar' });
  const legend = h('div', { class: 'wf-legend' });
  for (const p of phases) {
    const seg = h('div', { class: `wf-seg ph-${p.name}`, title: `${p.name}: ${fmtMs(p.ms)}` });
    seg.style.width = `${(100 * p.ms) / total}%`;
    bar.append(seg);
    legend.append(h('span', { class: `ph-${p.name}`, text: `${p.name} ${fmtMs(p.ms)}` }));
  }
  return h('div', { class: 'waterfall' }, bar, legend);
}

// certMeter shows how much of the certificate's lifetime remains.
function certMeter(tls, st) {
  if (!tls || !tls.not_after) return null;
  const from = new Date(tls.not_before).getTime();
  const to = new Date(tls.earliest_expiry || tls.not_after).getTime();
  const frac = Math.min(1, Math.max(0, (to - Date.now()) / Math.max(1, to - from)));
  const fill = h('div');
  fill.style.width = `${(frac * 100).toFixed(1)}%`;
  return h('div', { class: `meter ${statusClass(st)}`, title: `${Math.round(tls.days_left)} days left` }, fill);
}

function checkItem(target, c) {
  const id = `${target.name}/${c.kind}`;
  const latest = c.latest;
  const st = statusClass(c.status);
  const li = h('li', { class: `check ${state.open.has(id) ? 'open' : ''}` });

  const row = h('button', {
    class: 'check-row', type: 'button', 'aria-expanded': String(state.open.has(id)),
    onclick: () => {
      if (state.open.has(id)) state.open.delete(id); else state.open.add(id);
      li.classList.toggle('open');
      row.setAttribute('aria-expanded', String(state.open.has(id)));
    },
  },
    h('span', { class: 'kind', text: c.kind }),
    h('span', { class: `dot ${st}`, title: st }),
    h('span', { class: 'summary', text: latest ? latest.summary : 'pending…', title: latest ? latest.summary : '' }),
    h('span', { class: 'latency', text: latest ? fmtMs(latest.latency_ms) : '' }),
    sparkline(c.history),
  );

  const detail = h('div', { class: 'check-detail' });
  if (latest) {
    if (latest.error) detail.append(h('div', { class: 'error-box', text: latest.error }));
    if (c.kind === 'tls') detail.append(certMeter(latest.tls, latest.status) || '');
    const wf = waterfall(latest.phases);
    if (wf) detail.append(wf);
    const kv = h('dl', { class: 'kv' });
    for (const d of latest.details || []) kv.append(h('dt', { text: d.key }), h('dd', { text: d.value }));
    kv.append(
      h('dt', { text: 'availability' }), h('dd', { text: `${c.availability.toFixed(2)}% of ${c.runs} runs` }),
      h('dt', { text: 'last run' }), h('dd', { text: `${ago(latest.started_at)} (${new Date(latest.started_at).toLocaleTimeString()})` }),
    );
    detail.append(kv);
  } else {
    detail.append(h('p', { class: 'muted', text: 'Waiting for the first run of this check…' }));
  }
  li.append(row, detail);
  return li;
}

function card(t) {
  const runs = t.checks.filter((c) => c.runs > 0);
  const avail = runs.length ? runs.reduce((a, c) => a + c.availability, 0) / runs.length : null;
  const lastRun = t.checks
    .map((c) => (c.latest ? new Date(c.latest.started_at).getTime() : 0))
    .reduce((a, b) => Math.max(a, b), 0);

  return h('article', { class: `card ${statusClass(t.status)}` },
    h('div', { class: 'card-head' },
      h('div', { class: 'card-title' },
        h('h2', { text: t.name, title: t.name }),
        h('div', { class: 'host', text: t.host }),
      ),
      t.group ? h('span', { class: 'tag', text: t.group }) : null,
      pill(t.status),
    ),
    h('ul', { class: 'checks' }, t.checks.map((c) => checkItem(t, c))),
    h('div', { class: 'card-foot' },
      h('span', { text: avail == null ? 'no data yet' : `${avail.toFixed(2)}% available` }),
      h('span', { text: lastRun ? `checked ${ago(new Date(lastRun).toISOString())}` : 'pending' }),
    ),
  );
}

// ---------- page ----------

function renderBanner(sum, targets) {
  const banner = $('banner');
  let cls = 'pending', title = 'Waiting for the first results…', sub = '';
  const failing = targets.filter((t) => t.status === 'fail').map((t) => t.name);
  const degraded = targets.filter((t) => t.status === 'warn').map((t) => t.name);
  const list = (xs) => xs.slice(0, 4).join(', ') + (xs.length > 4 ? ` +${xs.length - 4} more` : '');

  if (sum.fail > 0) {
    cls = 'fail';
    title = `${sum.fail} check${sum.fail > 1 ? 's' : ''} failing`;
    sub = `Affected: ${list(failing)}`;
  } else if (sum.warn > 0) {
    cls = 'warn';
    title = `Degraded performance — ${sum.warn} warning${sum.warn > 1 ? 's' : ''}`;
    sub = `Affected: ${list(degraded)}`;
  } else if (sum.ok > 0) {
    cls = 'ok';
    title = 'All systems operational';
    sub = `${sum.ok} checks across ${sum.targets} targets are healthy`;
  }
  if (sum.pending > 0 && cls !== 'pending') sub += ` · ${sum.pending} pending`;
  banner.className = `banner ${cls}`;
  $('banner-title').textContent = title;
  $('banner-sub').textContent = sub;
}

function renderStats(d) {
  const sum = d.summary;
  $('s-targets').textContent = sum.targets;
  $('s-checks').textContent = `${sum.checks} checks`;
  $('s-ok').textContent = sum.ok;
  const done = sum.checks - sum.pending;
  $('s-ok-pct').textContent = done ? `${((100 * sum.ok) / done).toFixed(0)}% of reporting checks` : '–';
  $('s-warn').textContent = sum.warn;
  $('s-fail').textContent = sum.fail;

  const all = d.targets.flatMap((t) => t.checks.map((c) => ({ t, c })));
  const http = all.filter(({ c }) => c.kind === 'http' && c.latest && c.latest.http).map(({ c }) => c.latest.latency_ms);
  $('s-http').textContent = http.length ? fmtMs(median(http)) : '–';

  const certs = all.filter(({ c }) => c.kind === 'tls' && c.latest && c.latest.tls)
    .sort((a, b) => a.c.latest.tls.days_left - b.c.latest.tls.days_left);
  if (certs.length) {
    const { t, c } = certs[0];
    const days = c.latest.tls.days_left;
    const el = $('s-cert');
    el.textContent = days < 0 ? 'expired' : `${Math.floor(days)}d`;
    el.className = `stat-value ${days < 7 ? 'fail' : days < 30 ? 'warn' : ''}`;
    $('s-cert-host').textContent = t.name;
  }
}

function renderGroups(targets) {
  const groups = [...new Set(targets.map((t) => t.group).filter(Boolean))];
  const box = $('groups');
  box.replaceChildren();
  if (groups.length < 2) return;
  for (const g of groups) {
    box.append(h('button', {
      class: `chip ${state.group === g ? 'active' : ''}`, type: 'button', text: g,
      onclick: () => { state.group = state.group === g ? null : g; render(); },
    }));
  }
}

function visible(t) {
  if (state.group && t.group !== state.group) return false;
  if (state.filter === 'issues' && !(t.status === 'warn' || t.status === 'fail')) return false;
  if (state.filter === 'ok' && t.status !== 'ok') return false;
  if (state.query) {
    const q = state.query.toLowerCase();
    if (!t.name.toLowerCase().includes(q) && !t.host.toLowerCase().includes(q) && !(t.group || '').toLowerCase().includes(q)) return false;
  }
  return true;
}

function render() {
  const d = state.data;
  if (!d) return;
  $('title').textContent = d.title;
  document.title = `${d.title} · Network Health`;
  $('version').textContent = d.version;
  $('since').textContent = new Date(d.started_at).toLocaleString();

  renderBanner(d.summary, d.targets);
  renderStats(d);
  renderGroups(d.targets);

  const grid = $('grid');
  const shown = d.targets.filter(visible);
  grid.replaceChildren(...shown.map(card));
  $('empty').hidden = shown.length > 0;
  if (state.first) {
    grid.classList.add('intro');
    setTimeout(() => grid.classList.remove('intro'), 600);
    state.first = false;
  }
}

function tickClock() {
  const live = $('live');
  if (!state.lastOk) return;
  const age = Date.now() - state.lastOk;
  live.classList.toggle('stale', age > STALE_MS);
  $('updated').textContent = age > STALE_MS
    ? `connection lost · ${ago(new Date(state.lastOk).toISOString())}`
    : `live · updated ${Math.round(age / 1000)}s ago`;
}

async function refresh() {
  try {
    const res = await fetch('api/v1/status', { cache: 'no-store' });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    state.data = await res.json();
    state.lastOk = Date.now();
    render();
  } catch (err) {
    console.warn('netpulse: refresh failed', err);
  }
  tickClock();
}

function init() {
  document.querySelectorAll('.seg').forEach((btn) => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.seg').forEach((b) => b.classList.toggle('active', b === btn));
      state.filter = btn.dataset.filter;
      render();
    });
  });
  const search = $('search');
  search.addEventListener('input', () => { state.query = search.value.trim(); render(); });
  document.addEventListener('keydown', (e) => {
    if (e.key === '/' && document.activeElement !== search) { e.preventDefault(); search.focus(); }
    if (e.key === 'Escape' && document.activeElement === search) { search.value = ''; state.query = ''; render(); search.blur(); }
  });

  refresh();
  setInterval(refresh, REFRESH_MS);
  setInterval(tickClock, 1000);
}

init();
