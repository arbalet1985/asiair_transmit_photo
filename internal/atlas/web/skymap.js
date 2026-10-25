// The sky map of the atlas, as the Sky Atlas of ASIAIR: the sky seen from
// the observatory at a time — the ground, the horizon and the zones hidden
// by wires of the planner, the stars and the figures of the constellations
// (skydata.js), the objects of the catalog, the Moon, the Sun and the
// planets, the Milky Way — turned by dragging, zoomed by the wheel or two
// fingers; zoomed in, the fainter stars (skystars8.js) and a photograph of
// the sky (DSS2 from CDS, over the internet) under the map; a tap on an
// object shows it with a heart and «В план». No library: a canvas with the
// stereographic projection of the sky of altitude and azimuth.
"use strict";
(function () {
  const rad = Math.PI / 180, deg = 180 / Math.PI;
  const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
  const norm360 = a => ((a % 360) + 360) % 360;
  const SIDES = ["С", "СВ", "В", "ЮВ", "Ю", "ЮЗ", "З", "СЗ"];
  const KINDS = { G: "галактика", GPair: "пара галактик", GTrpl: "три галактики", GGroup: "группа галактик", HII: "область HII", Neb: "туманность",
    EmN: "эмиссионная туманность", RfN: "отражательная туманность", SNR: "остаток сверхновой", PN: "планетарная туманность", "Cl+N": "скопление с туманностью",
    DrkN: "тёмная туманность", OCl: "рассеянное скопление", GCl: "шаровое скопление", "*Ass": "звёздная ассоциация", "кадры": "из кадров" };
  const group = k => /^G/.test(k) && k !== "GCl" ? "galaxy" : k === "PN" ? "pn" : ["OCl", "GCl", "*Ass"].includes(k) ? "cluster" : "nebula";

  // ---- astronomy, to a fraction of a degree: enough for a map
  const jd = ms => ms / 864e5 + 2440587.5;
  const lst = (ms, lon) => norm360(280.46061837 + 360.98564736629 * (jd(ms) - 2451545) + lon);
  // altitude (with the refraction) and azimuth from the north through east
  function altAz(ra, dec, lat, st) {
    const H = (st - ra) * rad, d = dec * rad, p = lat * rad;
    const alt = Math.asin(clamp(Math.sin(p) * Math.sin(d) + Math.cos(p) * Math.cos(d) * Math.cos(H), -1, 1)) * deg;
    const az = Math.atan2(-Math.cos(d) * Math.sin(H), Math.cos(p) * Math.sin(d) - Math.sin(p) * Math.cos(d) * Math.cos(H)) * deg;
    return [alt > -1 ? alt + 1.02 / Math.tan((alt + 10.3 / (alt + 5.11)) * rad) / 60 : alt, norm360(az)];
  }
  function raDec(alt, az, lat, st) {
    const a = alt * rad, A = az * rad, p = lat * rad;
    const d = Math.asin(clamp(Math.sin(p) * Math.sin(a) + Math.cos(p) * Math.cos(a) * Math.cos(A), -1, 1));
    const H = Math.atan2(-Math.sin(A) * Math.cos(a), Math.cos(p) * Math.sin(a) - Math.sin(p) * Math.cos(a) * Math.cos(A)) * deg;
    return [norm360(st - H), d * deg];
  }
  const EPS = 23.43928 * rad;
  function eclToEq(l, b) {
    l *= rad; b *= rad;
    const ra = Math.atan2(Math.sin(l) * Math.cos(EPS) - Math.tan(b) * Math.sin(EPS), Math.cos(l)) * deg;
    return [norm360(ra), Math.asin(Math.sin(b) * Math.cos(EPS) + Math.cos(b) * Math.sin(EPS) * Math.sin(l)) * deg];
  }
  // the sky point at the offset (xi east, eta north, degrees) on the plane
  // tangent at ra0, dec0
  function fromPlane(ra0, dec0, xi, eta) {
    const x = xi * rad, y = eta * rad, d0 = dec0 * rad;
    const dec = Math.asin((Math.sin(d0) + y * Math.cos(d0)) / Math.sqrt(1 + x * x + y * y));
    return [norm360(ra0 + Math.atan2(x, Math.cos(d0) - y * Math.sin(d0)) * deg), dec * deg];
  }
  const unit = (ra, dec) => [Math.cos(dec * rad) * Math.cos(ra * rad), Math.cos(dec * rad) * Math.sin(ra * rad), Math.sin(dec * rad)];
  // the Milky Way of skydata.js: 0…5 by degree, read between the degrees
  let mwGrid = null;
  function milkyWay(ra, dec) {
    if (!mwGrid) {
      const m = window.SkyData && window.SkyData.mw;
      if (!m) return 0;
      mwGrid = new Uint8Array(m.length);
      for (let i = 0; i < m.length; i++) mwGrid[i] = m.charCodeAt(i) - 48;
    }
    const x = norm360(ra - 0.5), y = clamp(dec + 89.5, 0, 179), x0 = Math.floor(x), y0 = Math.floor(y), fx = x - x0, fy = y - y0;
    const x1 = (x0 + 1) % 360, y1 = Math.min(y0 + 1, 179), g = (xx, yy) => mwGrid[yy * 360 + xx];
    return (g(x0, y0) * (1 - fx) + g(x1, y0) * fx) * (1 - fy) + (g(x0, y1) * (1 - fx) + g(x1, y1) * fx) * fy;
  }
  // the fainter stars of skystars8.js, unpacked once
  let faint = null;
  function faintStars() {
    if (faint || !window.SkyStars8) return faint;
    const b = atob(window.SkyStars8), n = b.length / 6;
    const f = { n, ra: new Float32Array(n), dec: new Float32Array(n), mag: new Float32Array(n), bv: new Float32Array(n),
      x: new Float32Array(n), y: new Float32Array(n), z: new Float32Array(n) };
    for (let i = 0; i < n; i++) {
      const o = i * 6, c = k => b.charCodeAt(o + k);
      let d = c(2) | c(3) << 8;
      if (d >= 32768) d -= 65536;
      f.ra[i] = (c(0) | c(1) << 8) / 65536 * 360; f.dec[i] = d / 32767 * 90; f.mag[i] = c(4) / 20 - 2; f.bv[i] = (c(5) << 24 >> 24) / 50;
      [f.x[i], f.y[i], f.z[i]] = unit(f.ra[i], f.dec[i]);
    }
    return faint = f;
  }

  function sunEcl(ms) {
    const n = jd(ms) - 2451545, g = (357.528 + 0.9856003 * n) * rad;
    return norm360(280.460 + 0.9856474 * n + 1.915 * Math.sin(g) + 0.020 * Math.sin(2 * g));
  }
  function moonEcl(ms) {
    const n = jd(ms) - 2451545;
    const L = 218.316 + 13.176396 * n, M = (134.963 + 13.064993 * n) * rad, F = (93.272 + 13.229350 * n) * rad;
    const D = (297.850 + 12.190749 * n) * rad, Ms = (357.528 + 0.9856003 * n) * rad;
    const l = L + 6.289 * Math.sin(M) + 1.274 * Math.sin(2 * D - M) + 0.658 * Math.sin(2 * D) + 0.214 * Math.sin(2 * M) - 0.186 * Math.sin(Ms) - 0.114 * Math.sin(2 * F);
    return [norm360(l), 5.128 * Math.sin(F)];
  }
  // the planets: the elements of JPL (Standish), good from 1800 to 2050
  const PLANETS = [
    ["Меркурий", "#c9b9a4", [0.38709927, 0.00000037, 0.20563593, 0.00001906, 7.00497902, -0.00594749, 252.25032350, 149472.67411175, 77.45779628, 0.16047689, 48.33076593, -0.12534081]],
    ["Венера", "#fff3c4", [0.72333566, 0.00000390, 0.00677672, -0.00004107, 3.39467605, -0.00078890, 181.97909950, 58517.81538729, 131.60246718, 0.00268329, 76.67984255, -0.27769418]],
    ["Марс", "#ff8a5c", [1.52371034, 0.00001847, 0.09339410, 0.00007882, 1.84969142, -0.00813131, -4.55343205, 19140.30268499, -23.94362959, 0.44441088, 49.55953891, -0.29257343]],
    ["Юпитер", "#f3d9b0", [5.20288700, -0.00011607, 0.04838624, -0.00013253, 1.30439695, -0.00183714, 34.39644051, 3034.74612775, 14.72847983, 0.21252668, 100.47390909, 0.20469106]],
    ["Сатурн", "#ecd89a", [9.53667594, -0.00125060, 0.05386179, -0.00050991, 2.48599187, 0.00193609, 49.95424423, 1222.49362201, 92.59887831, -0.41897216, 113.66242448, -0.28867794]],
    ["Уран", "#b5ecf0", [19.18916464, -0.00196176, 0.04725744, -0.00004397, 0.77263783, -0.00242939, 313.23810451, 428.48202785, 170.95427630, 0.40805281, 74.01692503, 0.04240589]],
    ["Нептун", "#8fb2ff", [30.06992276, 0.00026291, 0.00859048, 0.00005105, 1.77004347, 0.00035372, -55.12002969, 218.45945325, 44.96476227, -0.32241464, 131.78422574, -0.00508664]],
  ];
  const EARTH = [1.00000261, 0.00000562, 0.01671123, -0.00004392, -0.00001531, -0.01294668, 100.46457166, 35999.37244981, 102.93768193, 0.32327364, 0, 0];
  function helio(el, T) {
    const a = el[0] + el[1] * T, e = el[2] + el[3] * T, I = (el[4] + el[5] * T) * rad, L = el[6] + el[7] * T, w = el[8] + el[9] * T, O = el[10] + el[11] * T;
    const M = norm360(L - w) * rad, om = (w - O) * rad, Om = O * rad;
    let E = M + e * Math.sin(M);
    for (let k = 0; k < 6; k++) E -= (E - e * Math.sin(E) - M) / (1 - e * Math.cos(E));
    const x = a * (Math.cos(E) - e), y = a * Math.sqrt(1 - e * e) * Math.sin(E);
    const co = Math.cos(om), so = Math.sin(om), cO = Math.cos(Om), sO = Math.sin(Om), cI = Math.cos(I), sI = Math.sin(I);
    return [(co * cO - so * sO * cI) * x + (-so * cO - co * sO * cI) * y, (co * sO + so * cO * cI) * x + (-so * sO + co * cO * cI) * y, so * sI * x + co * sI * y];
  }
  function planets(ms) {
    const T = (jd(ms) - 2451545) / 36525, E = helio(EARTH, T);
    return PLANETS.map(([name, color, el]) => {
      const p = helio(el, T), x = p[0] - E[0], y = p[1] - E[1], z = p[2] - E[2];
      const yq = y * Math.cos(EPS) - z * Math.sin(EPS), zq = y * Math.sin(EPS) + z * Math.cos(EPS);
      return { name, color, ra: norm360(Math.atan2(yq, x) * deg), dec: Math.atan2(zq, Math.hypot(x, yq)) * deg };
    });
  }

  const starColor = bv => bv < 0 ? "#aabfff" : bv < 0.3 ? "#cad7ff" : bv < 0.6 ? "#f8f7ff" : bv < 1 ? "#fff4ea" : bv < 1.5 ? "#ffd2a1" : "#ffcc6f";
  const DSO_COLOR = { galaxy: "#ffb27a", nebula: "#7fe3a2", cluster: "#ffe476", pn: "#8fd3ff" };
  const normName = s => String(s || "").toLowerCase().replace(/ё/g, "е").replace(/messier/g, "m").replace(/[\s\-_'".(),]/g, "").replace(/([a-zа-я])0+(\d)/g, "$1$2");
  const fmtRA = d => { const s = Math.round(norm360(d) / 15 * 3600) % 86400; return `${String(s / 3600 | 0).padStart(2, "0")}h${String((s / 60 | 0) % 60).padStart(2, "0")}m${String(s % 60).padStart(2, "0")}s`; };
  const fmtDec = d => { const s = Math.round(Math.abs(d) * 3600); return `${d < 0 ? "-" : "+"}${String(s / 3600 | 0).padStart(2, "0")}°${String((s / 60 | 0) % 60).padStart(2, "0")}'${String(s % 60).padStart(2, "0")}"`; };
  const side = az => SIDES[Math.round(norm360(az) / 45) % 8];

  const CSS = `
  .smap { position: relative; border-radius: 6px; overflow: hidden; background: #04060c; touch-action: none; user-select: none; -webkit-user-select: none; }
  .smap canvas { position: absolute; inset: 0; width: 100%; height: 100%; cursor: grab; }
  .smap .sal { position: absolute; inset: 0; pointer-events: none; }
  .smap .sal * { pointer-events: none !important; }
  .smap canvas.drag { cursor: grabbing; }
  .smap .sbar { position: absolute; left: 6px; right: 6px; top: 6px; display: flex; flex-wrap: wrap; gap: 4px; align-items: center; pointer-events: none; }
  .smap .sbar > * { pointer-events: auto; }
  .smap .sbar input[type=search] { flex: 1 1 140px; min-width: 0; max-width: 260px; }
  .smap .smore { display: contents; }
  .smap .smenu { display: none; }
  @media (max-width: 700px) {
    .atlas .smap.asky, #atlas .atlas .smap.asky { height: min(72vh, 560px); }
    .smap .smenu { display: inline-block; }
    .smap .smore { display: none; }
    .smap .sbar.open .smore { display: flex; flex-wrap: wrap; gap: 4px; flex-basis: 100%; }
  }
  .smap button, .smap input { font: 12px system-ui, sans-serif; color: #e8ecf3; background: rgba(20, 26, 38, .82); border: 1px solid rgba(255,255,255,.18);
    border-radius: 6px; padding: 4px 7px; }
  .smap button.on { border-color: #5b92f0; color: #fff; background: rgba(47, 111, 222, .7); }
  .smap .shud { position: absolute; left: 8px; bottom: 6px; right: 8px; font: 11px system-ui, sans-serif; color: rgba(225,232,245,.85);
    text-shadow: 0 0 3px #000; pointer-events: none; }
  .smap .scard { position: absolute; left: 6px; bottom: 26px; max-width: min(360px, calc(100% - 12px)); background: rgba(16, 21, 31, .92);
    border: 1px solid rgba(255,255,255,.18); border-radius: 8px; padding: 8px 10px; color: #e8ecf3; font: 12px/1.4 system-ui, sans-serif; }
  .smap .scard b { font-size: 13px; }
  .smap .scard .smuted { color: #a7b0c0; }
  .smap .scard .sbtns { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 6px; }
  .smap .scard .sheart { color: #ff6b6b; font-size: 15px; line-height: 1; }
  .smap .smsg { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: #bbb; font-size: 13px; text-align: center; padding: 16px; pointer-events: none; }`;

  function el(tag, props, ...kids) {
    const e = document.createElement(tag);
    Object.assign(e, props || {});
    for (const k of kids) if (k != null) e.append(k);
    return e;
  }
  const store = {
    get(k, d) { try { const v = localStorage.getItem("skymap." + k); return v === null ? d : JSON.parse(v); } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem("skymap." + k, JSON.stringify(v)); } catch (e) { /* private window */ } },
  };

  // create builds the map in div (its class gives the height). opts:
  // objectsURL — the objects of the catalog ([name, label, short, kind, ra,
  // dec, size, fame]); find(q) → Promise of objects {name, label, ra, dec,
  // kind, size} (a search of the program); onFavorite(o, on); onAdd(o);
  // onSelect(i) — a target of the plan tapped; onPhotos(o) — «Снимки неба».
  function create(div, opts) {
    opts = opts || {};
    if (!document.getElementById("skymapCss")) document.head.append(el("style", { id: "skymapCss", textContent: CSS }));
    const view = Object.assign({ az: 180, alt: 35, fov: 100 }, store.get("view", {}));
    const show = Object.assign({ grid: true, lines: true, names: true, dso: true, mw: true, dss: true }, store.get("show", {}));
    const st = { lat: null, lon: 0, tz: null, sky: null, horizon: null, zones: [], minAlt: 0, targets: [], colors: [], sel: -1, favs: new Set(),
      frame: [0, 0], time: null, objects: [], picked: null, hits: [] };
    const canvas = el("canvas");
    const q = el("input", { type: "search", placeholder: "найти: M 31, Вега, Юпитер…", title: "Объект, звезда, планета, Луна; Enter — навести" });
    const tog = (label, key, title) => {
      const b = el("button", { type: "button", textContent: label, title, className: show[key] ? "on" : "" });
      b.onclick = () => { show[key] = !show[key]; b.classList.toggle("on", show[key]); store.set("show", show); draw(); };
      return b;
    };
    const btn = (label, title, f) => { const b = el("button", { type: "button", textContent: label, title }); b.onclick = f; return b; };
    const timeBtn = btn("сейчас", "Небо сейчас; ещё раз — следовать за временем", () => setTime(null));
    // on a phone the rest of the buttons hide behind «⋯»
    const menu = btn("⋯", "Ещё: что показывать, время, масштаб, во весь экран", () => bar.classList.toggle("open"));
    menu.classList.add("smenu");
    const bar = el("div", { className: "sbar" }, q, timeBtn, menu, el("span", { className: "smore" },
      tog("сетка", "grid", "Высота и азимут, меридиан, «не ниже» планировщика"), tog("созвездия", "lines", "Фигуры созвездий"),
      tog("имена", "names", "Названия звёзд, созвездий и объектов"), tog("объекты", "dso", "Галактики, туманности, скопления каталога"),
      tog("Млечный Путь", "mw", "Млечный Путь на тёмном небе"),
      tog("снимок", "dss", "Снимок неба DSS2 под картой (CDS, нужен интернет): при общем виде — широкий, при приближении — чёткий"),
      btn("−1 ч", "На час назад", () => setTime(now() - 3600e3)), btn("+1 ч", "На час вперёд", () => setTime(now() + 3600e3)),
      btn("−", "Отдалить", () => zoom(1.4)), btn("+", "Приблизить", () => zoom(1 / 1.4)),
      btn("⛶", "Во весь экран", () => { if (document.fullscreenElement) document.exitFullscreen(); else if (root.requestFullscreen) root.requestFullscreen(); })));
    const hud = el("div", { className: "shud" });
    const card = el("div", { className: "scard", hidden: true });
    const msg = el("div", { className: "smsg", textContent: "Загружаю звёзды…" });
    const root = el("div", { className: "smap " + (opts.className || "") }, canvas, bar, hud, card, msg);
    div.append(root);
    const ctx = canvas.getContext("2d");
    let W = 0, H = 0, dpr = 1, scale = 1, groundCv = null, faintAsked = false, dssNow = false;

    const now = () => st.time == null ? Date.now() : st.time;
    function setTime(t) {
      st.time = t;
      timeBtn.classList.toggle("on", t == null);
      draw();
    }
    timeBtn.classList.add("on");
    function zoom(k) { view.fov = clamp(view.fov * k, 1, 200); saveView(); draw(); }
    let saveH = 0;
    function saveView() { clearTimeout(saveH); saveH = setTimeout(() => store.set("view", view), 400); }

    // ---- the projection: stereographic, centred on view.az, view.alt;
    // the azimuth grows to the right as seen from below the sky
    function proj(alt, az) {
      const p0 = view.alt * rad, p = alt * rad, dl = (az - view.az) * rad;
      const c = Math.sin(p0) * Math.sin(p) + Math.cos(p0) * Math.cos(p) * Math.cos(dl);
      if (c < -0.6) return null;
      const k = 2 / (1 + c);
      return [W / 2 + scale * k * Math.cos(p) * Math.sin(dl), H / 2 - scale * k * (Math.cos(p0) * Math.sin(p) - Math.sin(p0) * Math.cos(p) * Math.cos(dl))];
    }
    function unproj(x, y) {
      const u = (x - W / 2) / scale, v = -(y - H / 2) / scale, r = Math.hypot(u, v);
      if (r < 1e-9) return [view.alt, view.az];
      const c = 2 * Math.atan(r / 2), p0 = view.alt * rad;
      const alt = Math.asin(clamp(Math.cos(c) * Math.sin(p0) + v * Math.sin(c) * Math.cos(p0) / r, -1, 1));
      const az = view.az * rad + Math.atan2(u * Math.sin(c), r * Math.cos(p0) * Math.cos(c) - v * Math.sin(p0) * Math.sin(c));
      return [alt * deg, norm360(az * deg)];
    }

    // ---- the sky of the planner: the horizon by degree and the zones
    function horizonAt(az) { return st.horizon ? st.horizon[Math.round(norm360(az)) % 360] : 0; }
    function setSky(sky) {
      st.sky = sky || null;
      st.minAlt = sky && sky.min_alt || 0;
      st.zones = sky && sky.zones || [];
      const h = sky && sky.horizon || [];
      if (!h.length) st.horizon = null;
      else {
        const s = [...h].map(p => ({ az: norm360(p.az), alt: p.alt })).sort((a, b) => a.az - b.az);
        st.horizon = new Float32Array(360);
        for (let a = 0; a < 360; a++) {
          if (s.length === 1) { st.horizon[a] = s[0].alt; continue; }
          let i = s.findIndex(p => p.az > a);
          if (i < 0) i = s.length;
          const p = s[(i - 1 + s.length) % s.length], n = s[i % s.length], span = norm360(n.az - p.az);
          st.horizon[a] = span ? p.alt + (n.alt - p.alt) * norm360(a - p.az) / span : Math.max(p.alt, n.alt);
        }
      }
      groundCv = null;
      draw();
    }
    const d180 = a => { a = norm360(a); return a > 180 ? a - 360 : a; };
    function inZone(z, az, alt) {
      if (z.poly && z.poly.length >= 3) {
        const u = [];
        z.poly.forEach((p, i) => u.push({ az: i ? u[i - 1].az + d180(p.az - u[i - 1].az) : p.az, alt: p.alt }));
        az = norm360(az);
        return [-360, 0, 360].some(k => {
          let inside = false;
          for (let i = 0, j = u.length - 1; i < u.length; j = i++) {
            const a = u[i], b = u[j];
            if ((a.alt > alt) !== (b.alt > alt) && az + k < a.az + (alt - a.alt) * (b.az - a.az) / (b.alt - a.alt)) inside = !inside;
          }
          return inside;
        });
      }
      if (alt < Math.min(z.alt1, z.alt2) || alt > Math.max(z.alt1, z.alt2)) return false;
      return norm360(az - z.az1) <= norm360(z.az2 - z.az1);
    }
    // the ground, the obstacles and the zones, and the Milky Way (mw: its
    // brightness, 0 — not drawn): pictures of a point per two pixels of the
    // screen, smoothed when drawn; the Milky Way is drawn at once, the
    // ground over the rest later (drawGround)
    let mwCv = null;
    function layers(lat, sid, mw) {
      const k = Math.max(2, Math.round(2 * dpr)), gw = Math.ceil(W / k), gh = Math.ceil(H / k);
      if (!groundCv) { groundCv = document.createElement("canvas"); mwCv = document.createElement("canvas"); }
      for (const c of [groundCv, mwCv]) if (c.width !== gw || c.height !== gh) { c.width = gw; c.height = gh; }
      const g = groundCv.getContext("2d"), im = g.createImageData(gw, gh), d = im.data;
      const m = mwCv.getContext("2d"), mi = mw > 0 ? m.createImageData(gw, gh) : null;
      const zones = st.zones;
      for (let y = 0; y < gh; y++) {
        for (let x = 0; x < gw; x++) {
          const [alt, az] = unproj(x * k + k / 2, y * k + k / 2), o = (y * gw + x) * 4;
          let r = 0, gg = 0, b = 0, a = 0;
          if (alt < 0) { r = 22; gg = 30; b = 18; a = 255; }
          else if (alt < horizonAt(az)) { r = 46; gg = 38; b = 24; a = 215; }
          else if (zones.some(z => inZone(z, az, alt))) { r = 200; gg = 60; b = 60; a = 70; }
          d[o] = r; d[o + 1] = gg; d[o + 2] = b; d[o + 3] = a;
          if (mi && a < 255) {
            const [ra, dec] = raDec(alt, az, lat, sid), v = milkyWay(ra, dec);
            if (v > 0) { mi.data[o] = 190; mi.data[o + 1] = 200; mi.data[o + 2] = 235; mi.data[o + 3] = Math.round(v / 5 * 70 * mw); }
          }
        }
      }
      g.putImageData(im, 0, 0);
      ctx.imageSmoothingEnabled = true;
      if (mi) { m.putImageData(mi, 0, 0); ctx.drawImage(mwCv, 0, 0, W, H); }
    }
    function drawGround() { ctx.imageSmoothingEnabled = true; ctx.drawImage(groundCv, 0, 0, W, H); }

    // The photographs of the sky under the map: Aladin Lite (CDS) in a layer
    // below the canvas, in the projection of the map (stereographic) with
    // its centre, field and turn set at each drawing — DSS2 zoomed in to
    // AL_DSS, the all-sky photograph of Mellinger wider; its tiles come at
    // once. Loaded when the map is first drawn with the photograph on.
    const ALADIN = "https://cdn.jsdelivr.net/npm/aladin-lite@3.8.2/dist/aladin.js", AL_DSS = 30;
    let AL = null, alState = "", alSurvey = "", alDiv = null;
    async function alLoad() {
      alState = "loading";
      try {
        const A = (await import(ALADIN)).default;
        await A.init;
        alDiv = el("div", { className: "sal" });
        root.insertBefore(alDiv, canvas);
        AL = A.aladin(alDiv, { survey: "P/DSS2/color", projection: "STG", fov: view.fov, target: "0 +0", cooFrame: "ICRS",
          showReticle: false, showZoomControl: false, showFullscreenControl: false, showLayersControl: false, showGotoControl: false,
          showFrame: false, showCooGrid: false, showCooGridControl: false, showSimbadPointerControl: false, showContextMenu: false,
          showStatusBar: false, showProjectionControl: false, showSettingsControl: false, showShareControl: false, showCooLocation: false,
          showFov: false });
        alState = "ready";
      } catch (e) {
        alState = "failed"; // no WebGL2 or no internet: the pictures of hips2fits
      }
      draw();
    }
    // alSync puts Aladin under the view: whether it is shown
    function alSync(P, ra, dec, sunAlt) {
      if (!alState) alLoad();
      if (alState !== "ready") { if (alDiv) alDiv.hidden = true; return false; }
      alDiv.hidden = false;
      const survey = view.fov <= AL_DSS ? "P/DSS2/color" : "P/Mellinger/color";
      if (survey !== alSurvey) {
        alSurvey = survey;
        AL.setBaseImageLayer(survey);
        const l = AL.getBaseImageLayer && AL.getBaseImageLayer();
        // a faint nebula comes out (DSS gives it nearly black)
        if (l && l.setGamma && survey === "P/DSS2/color") { l.setGamma(1.6); l.setSaturation(1.3); }
      }
      // the turn: north at the centre, as the map has it
      const C = P(ra, dec), N = P(ra, Math.min(dec + 0.05, 89.999));
      const turn = C && N ? Math.atan2(N[0] - C[0], C[1] - N[1]) * deg : 0;
      // the same field across; Aladin turns the other way round
      AL.setFoV(view.fov);
      AL.gotoRaDec(ra, dec);
      AL.setRotation(-turn);
      return true;
    }

    // The photograph of the sky under the map, from hips2fits (CDS), at any
    // field up to DSS_WIDE. A wide view asks for the all-sky photograph of
    // Mellinger in the projection of the map (stereographic) centred on the
    // view, which then lies on it exactly, square over the diagonal so that
    // the sky turned leaves no corner; a narrow one (DSS_NARROW) a square
    // DSS2 picture (gnomonic) around the centre. It takes the service a second or two: asked a moment
    // after the view stops, first small and quick, then sharp; the last ones
    // are kept and the sharpest drawn over the blurrier (back to a place or
    // zoomed out: at once). Each is stretched as it comes: a faint nebula
    // is black as DSS gives it.
    const DSS_WIDE = 170, DSS_NARROW = 8, DSS_KEEP = 14;
    const dssCache = [], dssLoading = new Set();
    let dssH = 0, dssErr = "", dssKey = "";
    const sepDeg = (a, b) => Math.acos(clamp(a.reduce((s, v, i) => s + v * b[i], 0), -1, 1)) * deg;
    // what the view needs: the projection, the field (degrees, across)
    function dssWant() {
      if (view.fov > DSS_NARROW) return { proj: "STG", fov: Math.min(view.fov * Math.hypot(1, H / W) * 1.1, 220) };
      return { proj: "TAN", fov: clamp(view.fov * Math.hypot(1, H / W) * 1.4, 0.5, 16) };
    }
    // the photographs to draw, the blurriest first; covers: one is sharp
    // enough and lies right for the whole view
    function dssOver(cu) {
      const half = view.fov * Math.hypot(1, H / W) / 2, want = dssWant();
      let covers = false;
      const over = dssCache.filter(d => {
        const sep = sepDeg(cu, d.u);
        if (d.proj === "STG") {
          // centred elsewhere it lies right only near its centre
          if (sep > 0.2 * d.fov) return false;
          if (want.proj === "STG" && sep < 0.06 * d.fov && d.fov >= half * 2 && d.fov <= half * 3.2 && d.big) covers = true;
          return true;
        }
        if (want.proj === "TAN" && sep + half <= d.fov / 2 && d.fov <= want.fov * 2.2 && d.big) covers = true;
        return sep < d.fov / 2 + half;
      });
      over.sort((a, b) => b.fov / b.w - a.fov / a.w);
      return { over, covers };
    }
    // stretched as the thumbnails of the receiver: the background of each
    // colour away, one factor for the three (the picture as it is when the
    // service does not let it be read); soft: for the wide photograph,
    // whose sky is to stay dark
    function stretch(img, soft) { // soft: 0 strong, 1 a wider DSS picture (its plates show), 2 Mellinger
      try {
        const c = document.createElement("canvas");
        c.width = img.naturalWidth; c.height = img.naturalHeight;
        const g = c.getContext("2d");
        g.drawImage(img, 0, 0);
        const im = g.getImageData(0, 0, c.width, c.height), d = im.data, n = d.length / 4;
        const hist = [new Uint32Array(256), new Uint32Array(256), new Uint32Array(256)];
        for (let i = 0; i < d.length; i += 4) for (let k = 0; k < 3; k++) hist[k][d[i + k]]++;
        const at = (h, q) => { let s = 0; for (let v = 0; v < 256; v++) { s += h[v]; if (s >= q * n) return v; } return 255; };
        const bg = hist.map(h => at(h, soft === 2 ? 0.02 : 0.05)), lh = new Uint32Array(256);
        for (let i = 0; i < d.length; i += 4) lh[Math.max(0, d[i] - bg[0], d[i + 1] - bg[1], d[i + 2] - bg[2])]++;
        const [q, top, gam] = [[0.995, 255, 0.8], [0.998, 245, 1], [0.9995, 225, 1.25]][soft || 0];
        const hi = Math.max(at(lh, q), 1), lut = new Uint8Array(256);
        for (let v = 0; v < 256; v++) lut[v] = Math.min(255, Math.round(top * Math.pow(v / hi, gam)));
        for (let i = 0; i < d.length; i += 4) for (let k = 0; k < 3; k++) d[i + k] = lut[Math.max(0, d[i + k] - bg[k])];
        g.putImageData(im, 0, 0);
        return c;
      } catch (e) { return img; }
    }
    function dssNeeded(ra, dec, cu, now) {
      if (dssOver(cu).covers) return;
      const want = dssWant(), key = `${want.proj} ${ra.toFixed(2)} ${dec.toFixed(2)} ${want.fov.toFixed(2)}`;
      if (key === dssKey) return;
      dssKey = key;
      clearTimeout(dssH);
      dssH = setTimeout(() => {
        for (const big of [false, true]) {
          const w = want.proj === "STG" ? (big ? 768 : 320) : (big ? 768 : 256), h = w, k = key + " " + w;
          if (dssLoading.has(k)) continue;
          dssLoading.add(k);
          const img = new Image();
          img.crossOrigin = "anonymous";
          img.onload = () => {
            dssLoading.delete(k);
            dssCache.push({ img: stretch(img, want.proj === "STG" ? 2 : want.fov > 4 ? 1 : 0), ra, dec, u: unit(ra, dec), fov: want.fov, proj: want.proj, w, h, big });
            // the small one gives way to the sharp one of the same place
            if (big) { const q = dssCache.findIndex(d => !d.big && d.ra === ra && d.dec === dec && d.fov === want.fov); if (q >= 0) dssCache.splice(q, 1); }
            while (dssCache.length > DSS_KEEP) dssCache.shift();
            dssErr = "";
            draw();
          };
          img.onerror = () => { dssLoading.delete(k); dssErr = "снимок неба не загружен (нужен интернет)"; if (dssKey === key) dssKey = ""; draw(); };
          const survey = want.proj === "STG" ? "P%2FMellinger%2Fcolor" : "CDS%2FP%2FDSS2%2Fcolor";
          img.src = `https://alasky.cds.unistra.fr/hips-image-services/hips2fits?hips=${survey}&format=jpg` +
            `&projection=${want.proj}&width=${w}&height=${h}&fov=${want.fov.toFixed(3)}&ra=${ra.toFixed(4)}&dec=${dec.toFixed(4)}`;
        }
      }, now ? 0 : 150);
    }
    // a photograph placed by three points: its centre and a small step east
    // and north (exact for one centred on the view: the same projection)
    function drawDss(P, alpha, cu) {
      if (alpha <= 0) return;
      for (const d of dssOver(cu).over) {
        const dd = Math.min(d.fov * 0.01, 0.2), s = d.fov / d.w;
        const C = P(d.ra, d.dec), E = P(...fromPlane(d.ra, d.dec, dd, 0)), N = P(...fromPlane(d.ra, d.dec, 0, dd));
        if (!C || !E || !N) continue;
        // east is to the left of the photograph, north up
        const ux = -(E[0] - C[0]) / dd * s, uy = -(E[1] - C[1]) / dd * s, vx = -(N[0] - C[0]) / dd * s, vy = -(N[1] - C[1]) / dd * s;
        ctx.save();
        ctx.globalAlpha = alpha;
        ctx.setTransform(ux, uy, vx, vy, C[0] - ux * d.w / 2 - vx * d.h / 2, C[1] - uy * d.w / 2 - vy * d.h / 2);
        ctx.drawImage(d.img, 0, 0, d.w, d.h);
        ctx.restore();
      }
    }

    function skyColor(sunAlt) {
      const stops = [[-18, [4, 6, 12]], [-12, [9, 14, 30]], [-6, [20, 34, 66]], [0, [44, 72, 118]], [10, [70, 118, 176]]];
      if (sunAlt <= stops[0][0]) return `rgb(${stops[0][1]})`;
      for (let i = 1; i < stops.length; i++) {
        if (sunAlt <= stops[i][0]) {
          const [a0, c0] = stops[i - 1], [a1, c1] = stops[i], f = (sunAlt - a0) / (a1 - a0);
          return `rgb(${c0.map((v, k) => Math.round(v + (c1[k] - v) * f))})`;
        }
      }
      return `rgb(${stops[stops.length - 1][1]})`;
    }

    // ---- drawing
    function resize() {
      const r = root.getBoundingClientRect();
      dpr = window.devicePixelRatio || 1;
      const w = Math.max(1, Math.round(r.width * dpr)), h = Math.max(1, Math.round(r.height * dpr));
      if (canvas.width !== w || canvas.height !== h) { canvas.width = w; canvas.height = h; }
      W = w; H = h;
      scale = (W / 2) / (2 * Math.tan(view.fov / 4 * rad));
    }
    let drawQueued = false;
    function draw() {
      if (drawQueued) return;
      drawQueued = true;
      requestAnimationFrame(() => { drawQueued = false; paint(); });
    }
    function line(pts, color, width, dash) {
      ctx.strokeStyle = color; ctx.lineWidth = width * dpr; ctx.setLineDash(dash ? dash.map(v => v * dpr) : []);
      ctx.beginPath();
      let pen = false, prev = null;
      for (const p of pts) {
        // a jump across the map (a point behind the observer) breaks the line
        if (!p || prev && Math.hypot(p[0] - prev[0], p[1] - prev[1]) > W) { pen = false; prev = p; continue; }
        if (pen) ctx.lineTo(p[0], p[1]); else ctx.moveTo(p[0], p[1]);
        pen = true; prev = p;
      }
      ctx.stroke();
      ctx.setLineDash([]);
    }
    function label(text, x, y, color, size, align, weight) {
      ctx.font = `${weight || ""} ${Math.round((size || 11) * dpr)}px system-ui, sans-serif`;
      ctx.fillStyle = color; ctx.textAlign = align || "left"; ctx.textBaseline = "middle";
      ctx.shadowColor = "#000"; ctx.shadowBlur = 3 * dpr;
      ctx.fillText(text, x, y);
      ctx.shadowBlur = 0;
    }
    function paint() {
      resize();
      if (!W || !root.isConnected) return;
      const t = now(), hits = [];
      const lat = st.lat == null ? 50 : st.lat, lon = st.lon || 0, sid = lst(t, lon);
      const P = (ra, dec) => { const [a, z] = altAz(ra, dec, lat, sid); return proj(a, z); };
      const pxPerDeg = scale * rad;
      // the Sun gives the colour of the sky
      const sunEq = eclToEq(sunEcl(t), 0), [sunAlt, sunAz] = altAz(sunEq[0], sunEq[1], lat, sid);
      const [cAlt, cAz] = unproj(W / 2, H / 2), [cRa, cDec] = raDec(cAlt, cAz, lat, sid);
      const cu = unit(cRa, cDec);
      // the photographs of Aladin under the map (the canvas is clear where
      // the sky is), or else the photograph of hips2fits
      const alOn = show.dss && view.fov <= DSS_WIDE && alSync(P, cRa, cDec, sunAlt);
      if (!alOn && alDiv) alDiv.hidden = true;
      ctx.clearRect(0, 0, W, H);
      ctx.fillStyle = skyColor(sunAlt);
      ctx.globalAlpha = alOn ? clamp((sunAlt + 10) / 12, 0, 0.85) : 1; // a photograph of the night sky under a day sky
      ctx.fillRect(0, 0, W, H);
      ctx.globalAlpha = 1;
      // the photograph of the sky, and the Milky Way of skydata.js on a
      // dark sky where there is none (no internet)
      const dssAlpha = !alOn && alState === "failed" && show.dss && view.fov <= DSS_WIDE ? 0.92 : 0;
      const dssShown = alOn || dssAlpha > 0 && dssOver(cu).over.length > 0;
      layers(lat, sid, show.mw && !dssShown ? clamp((-sunAlt - 8) / 8, 0, 1) : 0);
      if (dssAlpha > 0) dssNeeded(cRa, cDec, cu, dssNow);
      dssNow = false;
      drawDss(P, dssAlpha, cu);
      // the grid of altitude and azimuth, the meridian, «не ниже»
      if (show.grid) {
        const gc = "rgba(120,150,200,.22)";
        for (let a = 15; a < 90; a += 15) {
          const pts = [];
          for (let z = 0; z <= 360; z += 3) pts.push(proj(a, z));
          line(pts, gc, 1);
        }
        for (let z = 0; z < 360; z += 30) {
          const pts = [];
          for (let a = 0; a <= 80; a += 2) pts.push(proj(a, z));
          line(pts, gc, 1);
        }
        const mer = [];
        for (let a = 0; a <= 180; a += 2) mer.push(a <= 90 ? proj(a, 180) : proj(180 - a, 0));
        line(mer, "rgba(255,200,90,.35)", 1, [6, 4]);
        if (st.minAlt > 0) {
          const pts = [];
          for (let z = 0; z <= 360; z += 3) pts.push(proj(st.minAlt, z));
          line(pts, "rgba(255,160,90,.45)", 1, [3, 4]);
        }
      }
      const D = window.SkyData;
      if (D) {
        if (show.lines) {
          for (const l of D.lines) {
            const pts = [];
            for (let k = 0; k < l.length; k += 2) pts.push(P(l[k], l[k + 1]));
            line(pts, "rgba(110,160,230,.45)", 1);
          }
        }
        if (show.names && show.lines && view.fov < 160) {
          for (const [name, ra, dec, rank] of D.cons) {
            if (rank > 2 && view.fov > 90) continue;
            const p = P(ra, dec);
            if (p) label(name.toUpperCase(), p[0], p[1], "rgba(130,170,230,.65)", 10, "center");
          }
        }
        // the fainter stars when zoomed in, near the centre only
        const boost = clamp(Math.sqrt(90 / view.fov), 0.8, 2.2);
        const F = view.fov < 60 ? faintStars() : null;
        if (view.fov < 60 && !F && !faintAsked && opts.dataURL) {
          faintAsked = true;
          document.head.append(el("script", { src: opts.dataURL.replace(/skydata\.js/, "skystars8.js"), onload: draw }));
        }
        if (F) {
          const lim = view.fov < 25 ? 8.1 : 7.2, cu = unit(cRa, cDec), cl = Math.cos(Math.min(85, view.fov * 0.85) * rad);
          ctx.globalAlpha = dssShown ? 0.55 : 1;
          for (let i = F.n - 1; i >= 0; i--) {
            if (F.mag[i] > lim || F.x[i] * cu[0] + F.y[i] * cu[1] + F.z[i] * cu[2] < cl) continue;
            const p = P(F.ra[i], F.dec[i]);
            if (!p || p[0] < -3 || p[1] < -3 || p[0] > W + 3 || p[1] > H + 3) continue;
            ctx.fillStyle = starColor(F.bv[i]);
            ctx.beginPath(); ctx.arc(p[0], p[1], Math.max(0.5, (0.45 + 0.42 * (8.6 - F.mag[i])) * boost * 0.7) * dpr, 0, 2 * Math.PI); ctx.fill();
          }
          ctx.globalAlpha = 1;
        }
        // the stars, the brightest last so they lie on top
        const s = D.stars, limit = view.fov > 120 ? 5.2 : 6.5;
        for (let i = s.length - 4; i >= 0; i -= 4) {
          const mag = s[i + 2];
          if (mag > limit) continue;
          const p = P(s[i], s[i + 1]);
          if (!p || p[0] < -5 || p[1] < -5 || p[0] > W + 5 || p[1] > H + 5) continue;
          const r = Math.max(0.55, (0.45 + 0.42 * (6.6 - mag)) * boost) * dpr;
          ctx.fillStyle = starColor(s[i + 3]);
          ctx.beginPath(); ctx.arc(p[0], p[1], r, 0, 2 * Math.PI); ctx.fill();
          const n = i / 4;
          if (D.names[n] !== undefined || mag < 4) hits.push({ x: p[0], y: p[1], r, pri: 1, sel: { type: "star", name: D.names[n] || "", ra: s[i], dec: s[i + 1], mag } });
          if (show.names && D.names[n] && (mag < 1.6 || view.fov < 70)) label(D.names[n], p[0] + r + 3 * dpr, p[1], "rgba(220,228,240,.8)", 10);
        }
      }
      // the objects of the catalog
      if (show.dso) {
        for (const o of st.objects) {
          const [name, lbl, short, kind, ra, dec, size, fame] = o;
          if (fame === 2 && view.fov > 70) continue;
          const p = P(ra, dec);
          if (!p || p[0] < -20 || p[1] < -20 || p[0] > W + 20 || p[1] > H + 20) continue;
          const g = group(kind), c = DSO_COLOR[g], r = Math.max(4.5 * dpr, (size || 5) / 60 * pxPerDeg / 2);
          ctx.strokeStyle = c; ctx.lineWidth = 1.2 * dpr;
          ctx.beginPath();
          // a large one: its extent as a dashed circle (the catalog has no
          // tilt of the galaxies, a tilted ellipse would lie on the photograph)
          if (r > 22 * dpr) { ctx.setLineDash([5 * dpr, 4 * dpr]); ctx.globalAlpha = 0.6; ctx.arc(p[0], p[1], r, 0, 2 * Math.PI); }
          else if (g === "galaxy") ctx.ellipse(p[0], p[1], r, r * 0.55, -0.5, 0, 2 * Math.PI);
          else if (g === "cluster") { ctx.setLineDash([2 * dpr, 2 * dpr]); ctx.arc(p[0], p[1], r, 0, 2 * Math.PI); }
          else if (g === "pn") { ctx.arc(p[0], p[1], r, 0, 2 * Math.PI); ctx.moveTo(p[0] + 1.2 * dpr, p[1]); ctx.arc(p[0], p[1], 1.2 * dpr, 0, 2 * Math.PI); }
          else ctx.rect(p[0] - r, p[1] - r, 2 * r, 2 * r);
          ctx.stroke(); ctx.setLineDash([]); ctx.globalAlpha = 1;
          hits.push({ x: p[0], y: p[1], r, pri: 2, sel: { type: "dso", o: { name, label: lbl, ra, dec, kind, size } } });
          if (show.names && (fame === 0 && view.fov < 150 || fame === 1 && view.fov < 90 || view.fov < 35)) label(short, p[0] + r + 3 * dpr, p[1] - r * 0.6, c, 10);
        }
      }
      // the favorites
      for (const f of st.favList || []) {
        const p = P(f.ra, f.dec);
        if (!p) continue;
        label("♥", p[0], p[1] - 9 * dpr, "#ff6b8a", 12, "center");
        hits.push({ x: p[0], y: p[1], r: 6 * dpr, pri: 3, sel: { type: "dso", o: f } });
        if (show.names && !st.objects.some(o => o[0] === f.name)) label(f.label ? f.label.split(" — ")[0] : f.name, p[0] + 8 * dpr, p[1] - 9 * dpr, "#ff9db0", 10);
      }
      // the targets of the plan with their frames
      const [fw, fh] = st.frame;
      st.targets.forEach((tg, i) => {
        const c = st.colors[i] || "#5ac8ff", p = P(tg.ra, tg.dec);
        if (fw > 0 && window.Atlas) {
          const pts = Atlas.frame(tg.ra, tg.dec, fw, fh, tg.angle || 0, 6).map(([r, d]) => P(r, d));
          pts.push(pts[0]);
          line(pts, c, i === st.sel ? 2.4 : 1.4);
        }
        if (!p) return;
        ctx.strokeStyle = c; ctx.lineWidth = 1.5 * dpr;
        ctx.beginPath();
        ctx.moveTo(p[0] - 9 * dpr, p[1]); ctx.lineTo(p[0] - 3 * dpr, p[1]); ctx.moveTo(p[0] + 3 * dpr, p[1]); ctx.lineTo(p[0] + 9 * dpr, p[1]);
        ctx.moveTo(p[0], p[1] - 9 * dpr); ctx.lineTo(p[0], p[1] - 3 * dpr); ctx.moveTo(p[0], p[1] + 3 * dpr); ctx.lineTo(p[0], p[1] + 9 * dpr);
        ctx.stroke();
        label(`${i + 1}. ${tg.name}`, p[0] + 11 * dpr, p[1] + 10 * dpr, c, 11, "left", "600");
        hits.push({ x: p[0], y: p[1], r: 10 * dpr, pri: 4, sel: { type: "target", i } });
      });
      // the Sun, the Moon and the planets
      const disc = (ra, dec, sizeDeg, min) => { const p = P(ra, dec); return p ? [p, Math.max(min * dpr, sizeDeg / 2 * pxPerDeg)] : null; };
      const sd = disc(sunEq[0], sunEq[1], 0.53, 7);
      if (sd) {
        ctx.fillStyle = "#ffe066"; ctx.beginPath(); ctx.arc(sd[0][0], sd[0][1], sd[1], 0, 2 * Math.PI); ctx.fill();
        label("Солнце", sd[0][0] + sd[1] + 4 * dpr, sd[0][1], "#ffe066", 11);
        hits.push({ x: sd[0][0], y: sd[0][1], r: sd[1], pri: 5, sel: { type: "body", name: "Солнце" } });
      }
      const [ml, mb] = moonEcl(t), mEq = eclToEq(ml, mb);
      let [mAlt, mAz] = altAz(mEq[0], mEq[1], lat, sid);
      mAlt -= 0.95 * Math.cos(mAlt * rad); // the parallax: seen from the ground, not from the centre of the Earth
      const elong = norm360(ml - sunEcl(t)), illum = (1 - Math.cos(elong * rad)) / 2;
      const mp = proj(mAlt, mAz);
      if (mp) {
        const r = Math.max(7 * dpr, 0.26 * pxPerDeg);
        // the side lit, toward the Sun
        const sp = proj(sunAlt, sunAz) || proj(mAlt + (sunAlt - mAlt) * 0.1, mAz + d180(sunAz - mAz) * 0.1);
        const ang = sp ? Math.atan2(sp[1] - mp[1], sp[0] - mp[0]) : 0;
        ctx.save(); ctx.translate(mp[0], mp[1]); ctx.rotate(ang);
        ctx.fillStyle = "#3a3d44"; ctx.beginPath(); ctx.arc(0, 0, r, 0, 2 * Math.PI); ctx.fill();
        ctx.fillStyle = "#f2f0e6";
        ctx.beginPath(); ctx.arc(0, 0, r, -Math.PI / 2, Math.PI / 2); // the half toward the Sun
        ctx.ellipse(0, 0, Math.abs(1 - 2 * illum) * r, r, 0, Math.PI / 2, -Math.PI / 2, illum < 0.5);
        ctx.fill();
        ctx.restore();
        label(`Луна ${Math.round(illum * 100)}%`, mp[0] + r + 4 * dpr, mp[1], "#f2f0e6", 11);
        hits.push({ x: mp[0], y: mp[1], r, pri: 5, sel: { type: "body", name: "Луна" } });
      }
      for (const pl of planets(t)) {
        const p = P(pl.ra, pl.dec);
        if (!p) continue;
        const r = (pl.name === "Венера" || pl.name === "Юпитер" ? 4 : 3) * dpr;
        ctx.fillStyle = pl.color; ctx.beginPath(); ctx.arc(p[0], p[1], r, 0, 2 * Math.PI); ctx.fill();
        if (show.names) label(pl.name, p[0] + r + 4 * dpr, p[1], pl.color, 11);
        hits.push({ x: p[0], y: p[1], r, pri: 5, sel: { type: "body", name: pl.name } });
      }
      st.bodies = { "Солнце": sunEq, "Луна": raDec(mAlt, mAz, lat, sid), illum, waxing: elong < 180 };
      for (const pl of planets(t)) st.bodies[pl.name] = [pl.ra, pl.dec];
      // the ground over everything below it
      drawGround();
      // the horizon and the sides of the sky
      const hz = [];
      for (let z = 0; z <= 360; z += 2) hz.push(proj(0, z));
      line(hz, "rgba(160,190,130,.7)", 1.2);
      SIDES.forEach((s, k) => {
        const p = proj(-2.5, k * 45);
        if (p) label(s, p[0], p[1], k === 0 ? "#ff7a7a" : "#d8e2c8", k % 2 ? 11 : 14, "center", "700");
      });
      // the one tapped
      const pk = st.picked && where(st.picked);
      if (pk) {
        const p = P(pk[0], pk[1]);
        if (p) { ctx.strokeStyle = "#fff"; ctx.lineWidth = 1.5 * dpr; ctx.beginPath(); ctx.arc(p[0], p[1], 14 * dpr, 0, 2 * Math.PI); ctx.stroke(); }
        // the field of the camera around it, as ASIAIR shows it
        if (fw > 0 && window.Atlas && st.picked.type !== "target") {
          const pts = Atlas.frame(pk[0], pk[1], fw, fh, 0, 6).map(([r, d]) => P(r, d));
          pts.push(pts[0]);
          line(pts, "rgba(255,255,255,.75)", 1.2, [6, 4]);
        }
        showCard();
      }
      st.hits = hits;
      const zone = st.tz == null ? null : st.tz;
      const tt = zone == null ? new Date(t).toLocaleString("ru-RU", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" })
        : new Date(t + zone * 3600e3).toISOString().slice(5, 16).replace("T", " ").replace(/^(\d\d)-(\d\d)/, "$2.$1");
      hud.textContent = `${tt}${st.time == null ? " (сейчас)" : ""} · центр ${side(view.az)} ${Math.round(view.az)}°, высота ${Math.round(view.alt)}° · поле ${Math.round(view.fov)}°` +
        (st.lat == null ? " · координаты обсерватории не заданы" : "") + (sunAlt > -6 ? ` · Солнце на ${Math.round(sunAlt)}°` : "") +
        (alOn ? (view.fov <= AL_DSS ? " · снимки DSS2 (Aladin, CDS)" : " · снимок неба Mellinger (Aladin, CDS)") :
          dssAlpha > 0 ? (dssErr ? " · " + dssErr : dssLoading.size ? " · загружаю снимок неба…" : dssShown ? (view.fov > DSS_NARROW ? " · снимок неба Mellinger (CDS)" : " · снимок DSS2 (CDS)") : "") : "");
    }

    // where the one picked is now: [ra, dec]
    function where(s) {
      if (s.type === "body") return st.bodies && st.bodies[s.name];
      if (s.type === "target") { const t = st.targets[s.i]; return t ? [t.ra, t.dec] : null; }
      if (s.type === "dso") return [s.o.ra, s.o.dec];
      if (s.type === "star") return [s.ra, s.dec];
      return null;
    }
    function showCard() {
      const s = st.picked, w = s && where(s);
      if (!s || !w) { card.hidden = true; return; }
      const lat = st.lat == null ? 50 : st.lat, [alt, az] = altAz(w[0], w[1], lat, lst(now(), st.lon || 0));
      let title = "", sub = "", fav = null;
      if (s.type === "dso") {
        title = s.o.label || s.o.name;
        sub = KINDS[s.o.kind] || s.o.kind || "";
        if (s.o.size) sub += (sub ? " · " : "") + (s.o.size >= 60 ? `${(s.o.size / 60).toFixed(1)}°` : `${Math.round(s.o.size)}′`);
        fav = s.o;
      } else if (s.type === "target") {
        const tg = st.targets[s.i];
        title = `${s.i + 1}. ${tg.name}`; sub = "объект плана";
        fav = { name: tg.name, label: tg.name, ra: tg.ra, dec: tg.dec, kind: "" };
      } else if (s.type === "star") {
        title = s.name || "Звезда"; sub = `звезда, ${s.mag.toFixed(1)}m`;
      } else {
        title = s.name;
        sub = s.name === "Луна" ? `освещена на ${Math.round(st.bodies.illum * 100)}%, ${st.bodies.waxing ? "растущая" : "убывающая"}` : s.name === "Солнце" ? "" : "планета";
      }
      const kids = [el("b", { textContent: title }), sub ? el("div", { className: "smuted", textContent: sub }) : null,
        el("div", { textContent: `RA ${fmtRA(w[0])} · Dec ${fmtDec(w[1])}` }),
        el("div", { className: "smuted", textContent: alt > 0 ? `высота ${Math.round(alt)}°, азимут ${Math.round(az)}° (${side(az)})` : `под горизонтом (${Math.round(alt)}°)` })];
      const btns = el("div", { className: "sbtns" });
      if (fav && opts.onFavorite) {
        const on = st.favs.has(fav.name);
        const b = el("button", { type: "button", title: on ? "Убрать из избранного" : "В избранное" }, el("span", { className: "sheart", textContent: on ? "♥" : "♡" }), on ? " в избранном" : " в избранное");
        b.onclick = () => { opts.onFavorite(fav, !on); };
        btns.append(b);
      }
      if (fav && s.type !== "target" && opts.onAdd) btns.append(btn("В план", "Добавить в объекты планировщика", () => opts.onAdd(fav)));
      if (s.type === "target" && opts.onSelect) btns.append(btn("Выбрать в плане", "Выбрать строку этого объекта", () => opts.onSelect(s.i)));
      if (opts.onPhotos && s.type !== "body") btns.append(btn("Снимки неба", "Показать это место на снимках неба", () => opts.onPhotos({ ra: w[0], dec: w[1], name: title })));
      btns.append(btn("✕", "Закрыть", () => { st.picked = null; card.hidden = true; draw(); }));
      kids.push(btns);
      card.replaceChildren(...kids.filter(Boolean));
      card.hidden = false;
    }

    // ---- the mouse and the fingers
    const ptrs = new Map();
    let drag = null;
    canvas.addEventListener("pointerdown", e => {
      canvas.setPointerCapture(e.pointerId);
      ptrs.set(e.pointerId, [e.clientX, e.clientY]);
      drag = { x: e.clientX, y: e.clientY, moved: 0, t: Date.now(), pinch: ptrs.size > 1 ? dist() : 0, fov: view.fov };
    });
    const dist = () => { const p = [...ptrs.values()]; return p.length > 1 ? Math.hypot(p[0][0] - p[1][0], p[0][1] - p[1][1]) : 0; };
    canvas.addEventListener("pointermove", e => {
      if (!ptrs.has(e.pointerId) || !drag) return;
      const prev = ptrs.get(e.pointerId);
      ptrs.set(e.pointerId, [e.clientX, e.clientY]);
      if (ptrs.size > 1) {
        if (!drag.pinch) { drag.pinch = dist(); drag.fov = view.fov; }
        view.fov = clamp(drag.fov * drag.pinch / Math.max(1, dist()), 1, 200);
        drag.moved += 99;
      } else {
        const dx = e.clientX - prev[0], dy = e.clientY - prev[1];
        drag.moved += Math.abs(dx) + Math.abs(dy);
        const dpp = deg / scale * dpr; // degrees per CSS pixel at the centre
        view.az = norm360(view.az - dx * dpp / Math.max(0.25, Math.cos(view.alt * rad)));
        view.alt = clamp(view.alt + dy * dpp, -30, 90);
        canvas.classList.add("drag");
      }
      saveView();
      draw();
    });
    const up = e => {
      ptrs.delete(e.pointerId);
      canvas.classList.remove("drag");
      if (drag && ptrs.size === 0) {
        if (drag.moved < 8 && Date.now() - drag.t < 600) {
          const r = canvas.getBoundingClientRect();
          pick((e.clientX - r.left) * dpr, (e.clientY - r.top) * dpr);
        }
        drag = null;
      }
    };
    canvas.addEventListener("pointerup", up);
    canvas.addEventListener("pointercancel", up);
    canvas.addEventListener("wheel", e => { e.preventDefault(); zoom(Math.exp(e.deltaY * 0.0015)); }, { passive: false });
    canvas.addEventListener("dblclick", e => {
      const r = canvas.getBoundingClientRect(), [alt, az] = unproj((e.clientX - r.left) * dpr, (e.clientY - r.top) * dpr);
      view.alt = clamp(alt, -30, 90); view.az = az; zoom(1 / 1.6);
    });
    function pick(x, y) {
      let best = null, bd = Infinity;
      for (const h of st.hits) {
        const d = Math.hypot(h.x - x, h.y - y) - Math.min(h.r, 20 * dpr);
        const reach = 10 * dpr;
        if (d > reach) continue;
        // the targets and the bodies before the stars under them
        const score = d - h.pri * 3 * dpr;
        if (score < bd) { bd = score; best = h; }
      }
      st.picked = best ? best.sel : null;
      if (!best) card.hidden = true;
      draw();
    }

    // ---- the search: the objects of the map, the stars, the planets, then
    // the program
    async function find(text) {
      const n = normName(text);
      if (!n) return;
      const bodies = ["Солнце", "Луна", ...PLANETS.map(p => p[0])];
      let s = null;
      const b = bodies.find(x => normName(x) === n);
      if (b) s = { type: "body", name: b };
      const o = !s && (st.objects.find(o => normName(o[2]) === n || normName(o[0]) === n) || st.objects.find(o => normName(o[1]).includes(n)));
      if (o) s = { type: "dso", o: { name: o[0], label: o[1], ra: o[4], dec: o[5], kind: o[3], size: o[6] } };
      if (!s && window.SkyData) {
        const D = window.SkyData, k = Object.keys(D.names).find(i => normName(D.names[i]) === n);
        if (k != null) s = { type: "star", name: D.names[k], ra: D.stars[k * 4], dec: D.stars[k * 4 + 1], mag: D.stars[k * 4 + 2] };
      }
      if (!s && opts.find) {
        try {
          const l = await opts.find(text);
          if (l && l[0]) s = { type: "dso", o: { name: l[0].name, label: l[0].label || l[0].name, ra: l[0].ra, dec: l[0].dec, kind: l[0].kind, size: l[0].size || 0 } };
        } catch (e) { /* told below */ }
      }
      if (!s) { hud.textContent = `«${text}» не найден`; return; }
      st.picked = s;
      paint(); // the bodies' places
      goToSel(s);
    }
    q.addEventListener("keydown", e => { if (e.key === "Enter") find(q.value); });
    function goToSel(s) {
      const w = where(s);
      if (!w) return;
      goTo(w[0], w[1], s.type === "dso" && s.o.size ? clamp(s.o.size / 60 * 6, 3, 60) : null);
    }
    function goTo(ra, dec, fov) {
      dssNow = true; // the photograph of the place asked at once
      const lat = st.lat == null ? 50 : st.lat, [alt, az] = altAz(ra, dec, lat, lst(now(), st.lon || 0));
      view.az = az; view.alt = clamp(alt, -30, 90);
      if (fov) view.fov = Math.min(view.fov, fov);
      saveView();
      draw();
    }

    // the stars and the objects
    (async () => {
      try {
        if (!window.SkyData && opts.dataURL) await new Promise((ok, fail) => document.head.append(el("script", { src: opts.dataURL, onload: ok, onerror: () => fail(new Error("звёзды не загружены")) })));
        if (opts.objectsURL) {
          const r = await fetch(opts.objectsURL);
          if (r.ok) st.objects = await r.json();
        }
        msg.remove();
      } catch (e) {
        msg.textContent = "Карта неба: " + e.message;
      }
      draw();
    })();
    // the sky now is followed: a redraw each half a minute while shown
    setInterval(() => { if (st.time == null && root.offsetParent) draw(); }, 30e3);
    window.addEventListener("resize", draw);
    if (window.ResizeObserver) new ResizeObserver(draw).observe(root);

    return {
      root,
      // the observatory: degrees, east positive; tz — hours of its clock (null: this computer's)
      setSite(lat, lon, tz) { st.lat = lat == null || isNaN(lat) ? null : +lat; st.lon = +lon || 0; st.tz = tz == null || isNaN(tz) ? null : +tz; draw(); },
      setSky,
      setTargets(targets, colors, sel) { st.targets = targets || []; st.colors = colors || []; st.sel = sel; draw(); },
      // the favorite objects: {name, label, ra, dec, kind, size}
      setFavorites(list) { st.favList = list || []; st.favs = new Set(st.favList.map(f => f.name)); draw(); },
      // the field of the frame of the optics, degrees
      setFrame(w, h) { st.frame = [w || 0, h || 0]; draw(); },
      // the time shown (null: now, followed)
      setTime,
      goTo,
      // the target i tapped elsewhere: the map to it
      select(i) { const t = st.targets[i]; if (t) { st.picked = { type: "target", i }; goTo(t.ra, t.dec); } },
      draw,
    };
  }

  window.SkyMap = { create, altAz, lst };
})();
