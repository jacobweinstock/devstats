// Front-end for the Tinkerbell developer-activity board. Loads the day-precise
// event list once, then filters/aggregates entirely in the browser so any
// range × repo × metric selection is instant. See docs/PLAN.md §9.

const KIND_INDEX = { commit: 0, issue: 1, pr: 2, review: 3, comment: 4 };

const els = {
  range: document.getElementById("range"),
  customRange: document.getElementById("custom-range"),
  from: document.getElementById("from"),
  to: document.getElementById("to"),
  metric: document.getElementById("metric"),
  group: document.getElementById("group"),
  excludeBots: document.getElementById("exclude-bots"),
  summary: document.getElementById("summary"),
  tbody: document.querySelector("#board tbody"),
  footer: document.getElementById("footer"),
};

let DATA = null; // parsed events.json
let EVENTS = null; // [loginIdx, repoIdx, kindIdx, "YYYY-MM-DD", epochMs]
let botSet = new Set();

const NBINS = 20; // sparkline resolution

// toDay returns a YYYY-MM-DD string (dates compare lexicographically = chronologically).
function toDay(d) {
  return d.toISOString().slice(0, 10);
}

function daysAgo(n) {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - n);
  return toDay(d);
}

function tomorrow() {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() + 1);
  return toDay(d);
}

// addDays returns the YYYY-MM-DD n days after the given YYYY-MM-DD string.
function addDays(day, n) {
  const d = new Date(day + "T00:00:00Z");
  d.setUTCDate(d.getUTCDate() + n);
  return toDay(d);
}

// selectedWindow returns [start, end) day strings for the current Range control.
// For a custom range the To field is inclusive, so end is the day after it.
function selectedWindow() {
  const v = els.range.value;
  if (v === "all") return [DATA.minDay, tomorrow()];
  if (v === "custom") {
    const start = els.from.value || DATA.minDay;
    const end = els.to.value ? addDays(els.to.value, 1) : tomorrow();
    return [start, end];
  }
  return [daysAgo(parseInt(v, 10)), tomorrow()];
}

// selectedRepos resolves the chosen repo to a Set of repo indexes; "All"
// returns null, meaning every repo.
function selectedRepos() {
  const v = els.group.value;
  if (v === "All") return null;
  const i = DATA.repoIndex.get(v);
  return i === undefined ? new Set() : new Set([i]);
}

function render() {
  if (!DATA) return;
  const [start, end] = selectedWindow();
  // Keep the From/To fields in sync with a preset so they always show the
  // active window (To is displayed as the last included day).
  if (els.range.value !== "custom") {
    els.from.value = start;
    els.to.value = addDays(end, -1);
  }
  const repos = selectedRepos();
  const metric = els.metric.value;
  const metricKind = KIND_INDEX[metric]; // undefined for "contributions"
  const excludeBots = els.excludeBots.checked;
  const startT = Date.parse(start);
  const endT = Date.parse(end);
  const span = Math.max(1, endT - startT);

  // Single pass: per-login total, activity bins (sparkline), and per-repo split.
  const agg = new Map();
  for (const e of EVENTS) {
    const login = e[0], repo = e[1], kind = e[2], t = e[4];
    if (t < startT || t >= endT) continue;
    if (repos && !repos.has(repo)) continue;
    if (metricKind !== undefined && kind !== metricKind) continue;
    if (excludeBots && botSet.has(login)) continue;
    let a = agg.get(login);
    if (!a) {
      a = { n: 0, bins: new Array(NBINS).fill(0), perRepo: new Map() };
      agg.set(login, a);
    }
    a.n++;
    let bi = Math.floor(((t - startT) / span) * NBINS);
    if (bi < 0) bi = 0;
    else if (bi >= NBINS) bi = NBINS - 1;
    a.bins[bi]++;
    a.perRepo.set(repo, (a.perRepo.get(repo) || 0) + 1);
  }

  const rows = [...agg.entries()]
    .map(([login, a]) => ({ name: DATA.logins[login], ...a }))
    .filter((r) => r.n > 0)
    .sort((a, b) => b.n - a.n || a.name.localeCompare(b.name));

  els.tbody.innerHTML = "";
  rows.forEach((r, i) => {
    const tr = document.createElement("tr");
    tr.className = "row";
    tr.innerHTML =
      `<td class="rank">${i + 1}</td>` +
      `<td class="login"><span class="expander" aria-hidden="true">▸</span>` +
      `<a href="https://github.com/${encodeURIComponent(r.name)}" target="_blank" rel="noopener">${r.name}</a></td>` +
      `<td class="spark">${sparkline(r.bins)}</td>` +
      `<td class="number">${r.n.toLocaleString()}</td>`;

    const detail = document.createElement("tr");
    detail.className = "detail";
    detail.hidden = true;
    const cell = document.createElement("td");
    cell.colSpan = 4;
    cell.appendChild(repoBreakdown(r.perRepo));
    detail.appendChild(cell);

    tr.addEventListener("click", (ev) => {
      if (ev.target.closest("a")) return; // don't toggle when following the profile link
      detail.hidden = !detail.hidden;
      tr.classList.toggle("open", !detail.hidden);
    });

    els.tbody.appendChild(tr);
    els.tbody.appendChild(detail);
  });

  const metricLabel = els.metric.options[els.metric.selectedIndex].text;
  const repoLabel = els.group.value === "All" ? "all repos" : `repo: ${els.group.value}`;
  els.summary.textContent =
    `Tinkerbell developer statistics — ${metricLabel}, ${start} → ${end}, ` +
    `${repoLabel}${excludeBots ? ", bots excluded" : ""} · ` +
    `${rows.length.toLocaleString()} contributors`;

  writeStateToURL();
}

// sparkline renders activity bins as a tiny inline-SVG bar chart.
function sparkline(bins) {
  const w = 96, h = 18, pad = 1;
  const max = Math.max(1, ...bins);
  const bw = w / bins.length;
  let rects = "";
  for (let i = 0; i < bins.length; i++) {
    const bh = bins[i] > 0 ? Math.max(1, Math.round((bins[i] / max) * (h - 1))) : 0;
    if (bh === 0) continue;
    const x = (i * bw + pad).toFixed(2);
    rects += `<rect x="${x}" y="${(h - bh).toFixed(2)}" width="${(bw - pad).toFixed(2)}" height="${bh}" />`;
  }
  return `<svg class="spark-svg" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}" preserveAspectRatio="none" aria-hidden="true">${rects}</svg>`;
}

// repoBreakdown builds the per-repo chip list shown when a row is expanded.
function repoBreakdown(perRepo) {
  const wrap = document.createElement("div");
  wrap.className = "breakdown";
  const items = [...perRepo.entries()]
    .map(([r, n]) => ({ repo: DATA.repos[r], n }))
    .sort((a, b) => b.n - a.n || a.repo.localeCompare(b.repo));
  wrap.innerHTML = items
    .map(
      (it) =>
        `<span class="chip"><span class="chip-repo">${it.repo}</span><span class="chip-n">${it.n.toLocaleString()}</span></span>`
    )
    .join("");
  return wrap;
}

function populateRepos() {
  els.group.innerHTML = "";
  // "All" plus every individual repo, sorted (DATA.repos is already sorted).
  ["All", ...DATA.repos].forEach((name) => {
    const opt = document.createElement("option");
    opt.value = name;
    opt.textContent = name;
    els.group.appendChild(opt);
  });
}

// writeStateToURL reflects the current controls into the query string so the
// exact view is shareable and survives a reload.
function writeStateToURL() {
  const p = new URLSearchParams();
  p.set("range", els.range.value);
  if (els.range.value === "custom") {
    p.set("start", els.from.value);
    p.set("end", els.to.value);
  }
  p.set("metric", els.metric.value);
  p.set("group", els.group.value);
  if (!els.excludeBots.checked) p.set("bots", "1"); // default is bots-excluded
  history.replaceState(null, "", `${location.pathname}?${p.toString()}`);
}

// applyStateFromURL restores controls from the query string on load.
function applyStateFromURL() {
  const p = new URLSearchParams(location.search);
  if (p.has("range")) els.range.value = p.get("range");
  if (els.range.value === "custom") {
    if (p.has("start")) els.from.value = p.get("start");
    if (p.has("end")) els.to.value = p.get("end");
  }
  if (p.has("metric")) els.metric.value = p.get("metric");
  const g = p.get("group");
  if (g && (g === "All" || DATA.repoIndex.has(g))) els.group.value = g;
  else els.group.value = "All";
  els.excludeBots.checked = p.get("bots") !== "1";
}

function wireEvents() {
  els.range.addEventListener("change", render);
  // Editing a date means the user wants a custom window, so switch to it.
  const onDateEdit = () => {
    els.range.value = "custom";
    render();
  };
  els.from.addEventListener("change", onDateEdit);
  els.to.addEventListener("change", onDateEdit);
  // Open the native calendar on click, not just on the tiny indicator icon.
  [els.from, els.to].forEach((el) =>
    el.addEventListener("click", () => {
      try {
        el.showPicker();
      } catch {
        /* showPicker unsupported or blocked; the indicator icon still works */
      }
    })
  );
  [els.metric, els.group, els.excludeBots].forEach((el) =>
    el.addEventListener("change", render)
  );
}

async function main() {
  els.summary.textContent = "Loading…";
  const res = await fetch("data/events.json", { cache: "no-cache" });
  if (!res.ok) {
    els.summary.textContent = `Failed to load data (${res.status}).`;
    return;
  }
  DATA = await res.json();

  DATA.repoIndex = new Map(DATA.repos.map((r, i) => [r, i]));
  const loginIndex = new Map(DATA.logins.map((l, i) => [l, i]));
  botSet = new Set(
    (DATA.bot_logins || []).map((l) => loginIndex.get(l)).filter((i) => i !== undefined)
  );
  DATA.minDay = DATA.events.length ? DATA.events[0][3] : tomorrow();

  // Precompute epoch millis per event once so filtering/binning avoids re-parsing.
  EVENTS = DATA.events.map((e) => [e[0], e[1], e[2], e[3], Date.parse(e[3])]);

  // Default the custom pickers to the last-year window before restoring URL
  // state (To is inclusive, so it is today, not tomorrow).
  els.from.value = daysAgo(365);
  els.to.value = toDay(new Date());

  populateRepos();
  applyStateFromURL();
  wireEvents();
  render();

  const maxDay = DATA.events.length ? DATA.events[DATA.events.length - 1][3] : DATA.minDay;
  els.footer.textContent =
    `${DATA.events.length.toLocaleString()} events · ${DATA.repos.length} repos · ` +
    `data ${DATA.minDay} → ${maxDay} · generated ${DATA.generated_at} · ` +
    `click a row for the per-repo breakdown`;
}

main();
