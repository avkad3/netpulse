"use strict";

const WX = {
  sunny: { icon: "☀️", text: "sunny" },
  cloudy: { icon: "⛅", text: "cloudy" },
  rainy: { icon: "🌧️", text: "rainy" },
  stormy: { icon: "⛈️", text: "stormy" },
  unknown: { icon: "·", text: "waiting for data" },
};
const MAX_SAMPLES = 120;
const MAX_LOG = 60;

const state = {
  targets: new Map(), // name -> { status, samples: [{t, ms, ok}], el }
  selected: null,
  window: "1h",
};

const $ = (sel, root = document) => root.querySelector(sel);
const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
const fmtMs = (v) => (v == null || Number.isNaN(v) ? "–" : v < 10 ? v.toFixed(1) : Math.round(v).toString());

function address(t) {
  if (t.type === "dns") return t.host + (t.server ? ` @ ${t.server}` : "");
  if (t.type === "http") return t.url;
  return t.address;
}

function ago(iso) {
  if (!iso) return "never probed";
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  return `${Math.floor(s / 3600)}h ago`;
}

async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
  return res.json();
}

// ---------- cards ----------

function ensureCard(status) {
  const name = status.target.name;
  let entry = state.targets.get(name);
  if (entry) {
    entry.status = status;
    return entry;
  }
  const el = $("#card-tpl").content.firstElementChild.cloneNode(true);
  $(".name", el).textContent = name;
  $(".type", el).textContent = status.target.type;
  $(".addr", el).textContent = address(status.target);
  el.addEventListener("click", (e) => {
    if (!e.target.closest(".probe")) select(name);
  });
  el.addEventListener("keydown", (e) => {
    if (e.key === "Enter") select(name);
  });
  $(".probe", el).addEventListener("click", (e) => probeNow(name, e.currentTarget));
  $("#cards").appendChild(el);
  entry = { status, samples: [], el };
  state.targets.set(name, entry);
  seedSamples(name);
  return entry;
}

async function seedSamples(name) {
  try {
    const results = await api(`/api/targets/${encodeURIComponent(name)}/results?window=1h&limit=${MAX_SAMPLES}`);
    const entry = state.targets.get(name);
    const seeded = results.reverse().map((r) => ({ t: r.time, ms: r.latency_ms, ok: r.success }));
    // Keep any live samples that arrived while we were fetching.
    const newest = seeded.length ? seeded[seeded.length - 1].t : "";
    entry.samples = seeded.concat(entry.samples.filter((s) => s.t > newest)).slice(-MAX_SAMPLES);
    renderCard(entry);
  } catch (err) {
    console.warn("seed", name, err);
  }
}

function renderCard(entry) {
  const { status, el } = entry;
  const wx = status.weather?.condition || "unknown";
  const latest = status.latest;
  el.dataset.wx = wx;
  $(".wx", el).textContent = WX[wx].icon;
  $(".wx", el).title = wx;
  $(".cond", el).textContent = status.weather?.summary || WX[wx].text;
  const big = $(".big", el);
  big.classList.toggle("failed", latest && !latest.success);
  $(".latency", el).textContent = latest ? (latest.success ? fmtMs(latest.latency_ms) : "fail") : "–";
  const st = status.stats || {};
  $(".p50", el).textContent = st.count ? fmtMs(st.p50_ms) : "–";
  $(".p95", el).textContent = st.count ? fmtMs(st.p95_ms) : "–";
  $(".jitter", el).textContent = st.count ? fmtMs(st.jitter_ms) : "–";
  $(".uptime", el).textContent = st.count ? `${st.uptime_pct}%` : "–";
  const err = $(".err", el);
  err.hidden = !(latest && !latest.success);
  err.textContent = latest && !latest.success ? latest.error : "";
  $(".ago", el).textContent = ago(latest?.time);
  drawSpark($(".spark", el), entry.samples);
}

function drawSpark(canvas, samples) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth;
  const h = canvas.clientHeight;
  if (!w) return;
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext("2d");
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, w, h);
  if (samples.length < 2) return;

  const ok = samples.filter((s) => s.ok).map((s) => s.ms);
  const top = Math.max(1, ...ok) * 1.15;
  // Fill the width early on, then settle into a fixed scrolling scale.
  const step = w / (Math.min(MAX_SAMPLES, Math.max(samples.length, 30)) - 1);
  const x0 = w - (samples.length - 1) * step;
  const y = (ms) => h - 2 - (ms / top) * (h - 4);

  ctx.strokeStyle = css("--chart-avg");
  ctx.lineWidth = 1.5;
  ctx.lineJoin = "round";
  ctx.beginPath();
  let pen = false;
  samples.forEach((s, i) => {
    const x = x0 + i * step;
    if (!s.ok) { pen = false; return; }
    pen ? ctx.lineTo(x, y(s.ms)) : ctx.moveTo(x, y(s.ms));
    pen = true;
  });
  ctx.stroke();

  ctx.fillStyle = css("--bad");
  samples.forEach((s, i) => {
    if (!s.ok) ctx.fillRect(x0 + i * step - 1, 0, 2, h);
  });
}

// ---------- overall + log ----------

function renderOverall(cond) {
  const wx = WX[cond] || WX.unknown;
  $("#overall-icon").textContent = wx.icon;
  $("#overall-text").textContent = cond === "unknown" ? "Waiting for data" : wx.text;
  document.title = cond === "unknown" ? "NetPulse" : `${wx.icon} NetPulse — ${wx.text}`;
}

function logResult(r) {
  const tr = document.createElement("tr");
  const cells = [
    new Date(r.time).toLocaleTimeString(),
    r.target,
    r.type,
    `${fmtMs(r.latency_ms)} ms`,
    r.success ? (r.status_code ? `OK (${r.status_code})` : "OK") : r.error,
  ];
  cells.forEach((text, i) => {
    const td = document.createElement("td");
    td.textContent = text;
    if (i === 4) td.className = r.success ? "ok" : "bad";
    tr.appendChild(td);
  });
  const log = $("#log");
  log.prepend(tr);
  while (log.children.length > MAX_LOG) log.lastChild.remove();
}

// ---------- detail panel ----------

function select(name) {
  state.selected = name;
  for (const [n, e] of state.targets) e.el.classList.toggle("selected", n === name);
  $("#detail").hidden = false;
  $("#detail-title").textContent = name;
  loadHistory();
  renderPhases();
}

async function loadHistory() {
  const name = state.selected;
  if (!name) return;
  try {
    const h = await api(`/api/targets/${encodeURIComponent(name)}/history?window=${state.window}`);
    if (name === state.selected) drawHistory($("#history"), h.buckets);
  } catch (err) {
    console.warn("history", err);
  }
}

function drawHistory(canvas, buckets) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth;
  const h = canvas.clientHeight;
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext("2d");
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, w, h);
  const pad = { l: 44, r: 8, t: 8, b: 20 };
  const pw = w - pad.l - pad.r;
  const ph = h - pad.t - pad.b;
  ctx.font = "11px system-ui, sans-serif";
  ctx.fillStyle = css("--muted");
  if (!buckets.length) {
    ctx.fillText("No data in this window yet", pad.l, pad.t + 16);
    return;
  }
  const top = Math.max(1, ...buckets.map((b) => b.max_ms)) * 1.1;
  const y = (v) => pad.t + ph - (v / top) * ph;
  const bw = pw / buckets.length;

  ctx.strokeStyle = css("--chart-grid");
  ctx.lineWidth = 1;
  for (let i = 0; i <= 4; i++) {
    const v = (top / 4) * i;
    ctx.beginPath();
    ctx.moveTo(pad.l, y(v));
    ctx.lineTo(w - pad.r, y(v));
    ctx.stroke();
    ctx.fillText(`${Math.round(v)} ms`, 2, y(v) + 4);
  }

  buckets.forEach((b, i) => {
    const x = pad.l + i * bw;
    if (b.count === b.failures) {
      ctx.fillStyle = css("--bad");
      ctx.fillRect(x, pad.t, Math.max(1, bw - 1), ph);
      return;
    }
    ctx.fillStyle = css("--chart-max");
    ctx.fillRect(x, y(b.max_ms), Math.max(1, bw - 1), pad.t + ph - y(b.max_ms));
    if (b.failures) {
      ctx.fillStyle = css("--bad");
      ctx.fillRect(x, pad.t, Math.max(1, bw - 1), 3);
    }
  });

  ctx.strokeStyle = css("--chart-avg");
  ctx.lineWidth = 2;
  ctx.beginPath();
  let pen = false;
  buckets.forEach((b, i) => {
    if (b.count === b.failures) { pen = false; return; }
    const x = pad.l + i * bw + bw / 2;
    pen ? ctx.lineTo(x, y(b.avg_ms)) : ctx.moveTo(x, y(b.avg_ms));
    pen = true;
  });
  ctx.stroke();

  ctx.fillStyle = css("--muted");
  const fmt = (iso) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  ctx.fillText(fmt(buckets[0].start), pad.l, h - 4);
  const last = fmt(buckets[buckets.length - 1].start);
  ctx.fillText(last, w - pad.r - ctx.measureText(last).width, h - 4);
}

function renderPhases() {
  const box = $("#phases");
  box.replaceChildren();
  const entry = state.targets.get(state.selected);
  const r = entry?.status.latest;
  if (!r || r.type !== "http" || !r.success) return;
  const ttfb = r.ttfb_ms || r.latency_ms;
  const phases = [
    ["DNS", r.dns_ms, "#8e6cd8"],
    ["Connect", r.connect_ms, "#2f9e8f"],
    ["TLS", r.tls_ms, "#d98c2b"],
    ["Server", Math.max(0, ttfb - (r.dns_ms || 0) - (r.connect_ms || 0) - (r.tls_ms || 0)), "#2f6fdb"],
    ["Download", Math.max(0, r.latency_ms - ttfb), "#9aa4b5"],
  ].filter(([, v]) => v > 0);
  const title = document.createElement("div");
  title.className = "phase-legend";
  title.textContent = `Latest request breakdown — ${fmtMs(r.latency_ms)} ms total`;
  const bar = document.createElement("div");
  bar.className = "phasebar";
  const legend = document.createElement("div");
  legend.className = "phase-legend";
  for (const [label, v, color] of phases) {
    const seg = document.createElement("span");
    seg.style.width = `${(100 * v) / r.latency_ms}%`;
    seg.style.background = color;
    seg.title = `${label}: ${fmtMs(v)} ms`;
    bar.appendChild(seg);
    const item = document.createElement("span");
    item.innerHTML = `<span class="sw" style="background:${color}"></span>`;
    item.append(`${label} ${fmtMs(v)} ms`);
    legend.appendChild(item);
  }
  box.append(title, bar, legend);
}

async function probeNow(name, btn) {
  btn.disabled = true;
  btn.textContent = "Probing…";
  try {
    await api(`/api/targets/${encodeURIComponent(name)}/probe`, { method: "POST" });
  } catch (err) {
    console.warn("probe", err);
  } finally {
    btn.disabled = false;
    btn.textContent = "Probe now";
  }
}

// ---------- live connection ----------

function setConn(kind, text) {
  $("#conn").className = `conn ${kind}`;
  $("#conn-text").textContent = text;
}

function onSnapshot(snap) {
  renderOverall(snap.overall);
  for (const status of snap.targets) renderCard(ensureCard(status));
}

function onUpdate(u) {
  const entry = state.targets.get(u.result.target);
  if (!entry) return;
  entry.status = { ...entry.status, latest: u.result, stats: u.stats, weather: u.weather };
  entry.samples.push({ t: u.result.time, ms: u.result.latency_ms, ok: u.result.success });
  if (entry.samples.length > MAX_SAMPLES) entry.samples.shift();
  renderCard(entry);
  renderOverall(u.overall);
  logResult(u.result);
  if (state.selected === u.result.target) renderPhases();
}

let retry = 0;
function connect() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.onopen = () => { retry = 0; setConn("live", "live"); };
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.type === "snapshot") onSnapshot(msg.data);
    else if (msg.type === "update") onUpdate(msg.data);
  };
  ws.onclose = () => {
    const wait = Math.min(30000, 1000 * 2 ** retry++);
    setConn("down", `reconnecting in ${Math.round(wait / 1000)}s`);
    setTimeout(connect, wait);
  };
}

// ---------- wiring ----------

$("#detail-close").addEventListener("click", () => {
  state.selected = null;
  $("#detail").hidden = true;
  for (const e of state.targets.values()) e.el.classList.remove("selected");
});
document.querySelectorAll(".seg button").forEach((b) =>
  b.addEventListener("click", () => {
    document.querySelectorAll(".seg button").forEach((x) => x.classList.toggle("on", x === b));
    state.window = b.dataset.window;
    loadHistory();
  }));

setInterval(() => {
  for (const e of state.targets.values()) $(".ago", e.el).textContent = ago(e.status.latest?.time);
}, 1000);
setInterval(loadHistory, 30000);
window.addEventListener("resize", () => {
  for (const e of state.targets.values()) drawSpark($(".spark", e.el), e.samples);
  loadHistory();
});

connect();
