// The atlas of the planner: the sky (Aladin Lite, CDS Strasbourg) with the
// field of view of every target of the plan, and the framing of a target:
// its frame held in the middle while the sky is moved under it, turned by
// the camera angle. Shared by the receiver page and the sorter's atlas page.
"use strict";
(function () {
  const ALADIN = "https://cdn.jsdelivr.net/npm/aladin-lite@3.8.2/dist/aladin.js";
  const SURVEYS = [["P/DSS2/color", "DSS2 (цвет)"], ["P/PanSTARRS/DR1/color-z-zg-g", "PanSTARRS"], ["P/2MASS/color", "2MASS (ИК)"], ["P/Mellinger/color", "Mellinger (широко)"]];
  const rad = Math.PI / 180;

  // the field of view of the optics, degrees ([0, 0] when not known)
  function fov(o) {
    if (!o) return [0, 0];
    const f = (o.focal || 0) * (o.reducer > 0 ? o.reducer : 1);
    if (!(f > 0) || !(o.pixel > 0) || !(o.width > 0) || !(o.height > 0)) return [0, 0];
    const s = o.pixel / 1000 / f / rad; // degrees per pixel
    return [o.width * s, o.height * s];
  }

  // the sky point at the offset (xi east, eta north, degrees) from the centre
  // on the tangent plane
  function fromPlane(ra0, dec0, xi, eta) {
    const x = xi * rad, y = eta * rad, d0 = dec0 * rad;
    const dec = Math.asin((Math.sin(d0) + y * Math.cos(d0)) / Math.sqrt(1 + x * x + y * y));
    const ra = ra0 + Math.atan2(x, Math.cos(d0) - y * Math.sin(d0)) / rad;
    return [((ra % 360) + 360) % 360, dec / rad];
  }

  // the offset of a sky point from the centre on the tangent plane, degrees
  // (null behind it)
  function toPlane(ra0, dec0, ra, dec) {
    const d0 = dec0 * rad, d = dec * rad, da = (ra - ra0) * rad;
    const c = Math.sin(d0) * Math.sin(d) + Math.cos(d0) * Math.cos(d) * Math.cos(da);
    if (c <= 0) return null;
    return [Math.cos(d) * Math.sin(da) / c / rad, (Math.cos(d0) * Math.sin(d) - Math.sin(d0) * Math.cos(d) * Math.cos(da)) / c / rad];
  }

  // the frame w×h degrees around the centre, its top turned from north
  // through east by angle: n points along each side
  function frame(ra, dec, w, h, angle, n) {
    n = n || 4;
    const a = (angle || 0) * rad, ca = Math.cos(a), sa = Math.sin(a);
    const pts = [];
    const side = (u0, v0, u1, v1) => {
      for (let k = 0; k < n; k++) {
        const u = u0 + (u1 - u0) * k / n, v = v0 + (v1 - v0) * k / n;
        pts.push(fromPlane(ra, dec, u * ca + v * sa, -u * sa + v * ca));
      }
    };
    const x = w / 2, y = h / 2;
    side(-x, y, x, y); side(x, y, x, -y); side(x, -y, -x, -y); side(-x, -y, -x, y);
    return pts;
  }

  // whether the sky point lies inside the frame
  function inFrame(ra, dec, w, h, angle, pra, pdec) {
    const p = toPlane(ra, dec, pra, pdec);
    if (!p) return false;
    const a = (angle || 0) * rad;
    const u = p[0] * Math.cos(a) - p[1] * Math.sin(a), v = p[0] * Math.sin(a) + p[1] * Math.cos(a);
    return Math.abs(u) <= w / 2 && Math.abs(v) <= h / 2;
  }

  const ru = (v, d) => v.toLocaleString("ru-RU", { maximumFractionDigits: d });
  const setupLabel = s => [s.camera || "камера", `${ru(s.focal, 0)} мм`, `${s.width}×${s.height}`, `${ru(s.pixel, 2)} мкм`].join(" · ") +
    (s.frames ? ` — кадров ${s.frames}` : "");

  const CSS = `
  .atlas { display: grid; gap: 8px; }
  .atlas .arow { display: flex; flex-wrap: wrap; gap: 6px 10px; align-items: center; }
  .atlas .arow label { display: inline-flex; gap: 4px; align-items: center; max-width: 100%; min-width: 0; }
  .atlas .arow input[type=number] { width: 5.5em; }
  /* a long name of the optics never widens the page of a phone */
  .atlas .arow select { min-width: 0; max-width: calc(100vw - 120px); text-overflow: ellipsis; }
  .atlas .asky { position: relative; height: 420px; border-radius: 6px; overflow: hidden; background: #000; }
  .atlas .asky .amsg { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; text-align: center;
    padding: 16px; color: #bbb; font-size: 13px; }
  .atlas .ainfo { font-size: 12px; color: var(--muted, #888); overflow-wrap: anywhere; }
  .atlas .aframe[hidden] { display: none; }
  .atlas .aframe input[type=range] { flex: 1 1 140px; min-width: 100px; }
  .atlas button.on { background: var(--accent, #5b92f0); border-color: var(--accent, #5b92f0); color: #fff; }
  @media (max-width: 700px) { .atlas .asky { height: 340px; } }`;

  function el(tag, props, ...kids) {
    const e = document.createElement(tag);
    Object.assign(e, props || {});
    for (const k of kids) if (k != null) e.append(k);
    return e;
  }

  // create builds the atlas in div. opts: onFrame(i, ra, dec, angle) — the
  // framing of target i applied; onSelect(i) — a frame clicked; onOptics(o)
  // — the optics changed.
  function create(div, opts) {
    opts = opts || {};
    if (!document.getElementById("atlasCss")) document.head.append(el("style", { id: "atlasCss", textContent: CSS }));
    const st = { targets: [], colors: [], sel: -1, optics: null, setups: [], preview: null, framing: null, survey: SURVEYS[0][0],
      photo: null, photoOpacity: 0.75, pendingFrame: -1 };
    let al = null, A = null, overlay = null, dashed = null, labels = null, failed = false;

    const survey = el("select", { title: "Снимки неба" }, ...SURVEYS.map(([v, t]) => el("option", { value: v, textContent: t })));
    const btnFrame = el("button", { type: "button", textContent: "Кадрировать", title: "Рамка выбранного объекта встаёт в центр: двигайте небо под ней и поворачивайте её, затем «Применить» — координаты и угол объекта меняются" });
    const setupSel = el("select", { title: "Оптика по кадрам: камера, фокус, кадр, пиксель" });
    const num = (title, step, min) => el("input", { type: "number", step, min, title });
    const fFocal = num("Фокусное расстояние телескопа, мм (как FOCALLEN в кадрах)", "1", "1");
    const fRed = num("Кратность редьюсера / барлоу: 0,8 для редьюсера 0,8×, 1 — без него", "0.01", "0.1");
    const fPix = num("Размер пикселя кадра, мкм (с бинингом, как XPIXSZ)", "0.01", "0.1");
    const fW = num("Ширина кадра, пикс.", "1", "1"), fH = num("Высота кадра, пикс.", "1", "1");
    const optRow = el("div", { className: "arow" },
      el("label", {}, "оптика", setupSel),
      el("label", {}, "фокус", fFocal, "мм"), el("label", {}, "×", fRed),
      el("label", {}, "пиксель", fPix, "мкм"), el("label", {}, "кадр", fW, "×", fH));
    const angle = el("input", { type: "range", min: "0", max: "360", step: "0.5", value: "0", title: "Угол рамки: от севера через восток" });
    const angleNum = num("Угол рамки, °", "0.5", "0");
    angleNum.max = "360";
    const btnL = el("button", { type: "button", textContent: "−1°" }), btnR = el("button", { type: "button", textContent: "+1°" });
    const btnApply = el("button", { type: "button", textContent: "Применить", className: "primary" });
    const btnCancel = el("button", { type: "button", textContent: "Отмена" });
    const frameRow = el("div", { className: "arow aframe", hidden: true }, el("span", {}, "угол"), btnL, angle, btnR, angleNum, "°", btnApply, btnCancel);
    // a frame laid over the sky, to compare its framing with the atlas
    const photoName = el("span", { className: "ainfo" });
    const photoOp = el("input", { type: "range", min: "0", max: "1", step: "0.05", value: "0.75", title: "Прозрачность снимка поверх неба" });
    const btnPhotoOff = el("button", { type: "button", textContent: "Убрать снимок" });
    const photoRow = el("div", { className: "arow", hidden: true }, el("span", {}, "снимок"), photoOp, btnPhotoOff, photoName);
    const sky = el("div", { className: "asky" });
    const msg = el("div", { className: "amsg", textContent: "Загружаю атлас…" });
    sky.append(msg);
    const info = el("div", { className: "ainfo" });
    const root = el("div", { className: "atlas" }, el("div", { className: "arow" }, survey, btnFrame), optRow, frameRow, photoRow, sky, info);
    div.replaceChildren(root);

    // the optics in effect: the ones chosen, else the last setup of the frames
    function optics() {
      const o = st.optics || {};
      if (o.focal > 0 && o.pixel > 0 && o.width > 0 && o.height > 0) return o;
      const s = st.setups[0];
      return s ? Object.assign({}, s, { reducer: o.reducer }) : o;
    }
    function showOptics() {
      const o = optics();
      setupSel.replaceChildren(el("option", { value: "", textContent: st.setups.length ? "вручную" : "по кадрам не найдена — введите" }),
        ...st.setups.map((s, i) => el("option", { value: String(i), textContent: setupLabel(s) })));
      const k = st.setups.findIndex(s => s.camera === o.camera && s.focal === o.focal && s.pixel === o.pixel && s.width === o.width && s.height === o.height);
      setupSel.value = k >= 0 ? String(k) : "";
      for (const [e, v] of [[fFocal, o.focal], [fRed, o.reducer > 0 ? o.reducer : 1], [fPix, o.pixel], [fW, o.width], [fH, o.height]])
        if (document.activeElement !== e) e.value = v > 0 ? String(v) : "";
    }
    function opticsEdited(o) {
      st.optics = o;
      showOptics();
      draw();
      if (opts.onOptics) opts.onOptics(o);
    }
    setupSel.onchange = () => {
      const s = st.setups[+setupSel.value];
      if (s) opticsEdited({ camera: s.camera, focal: s.focal, pixel: s.pixel, width: s.width, height: s.height, reducer: optics().reducer });
    };
    for (const e of [fFocal, fRed, fPix, fW, fH]) e.onchange = () => {
      const o = optics();
      opticsEdited({ camera: o.camera, focal: +fFocal.value || 0, reducer: +fRed.value || 1, pixel: +fPix.value || 0,
        width: Math.round(+fW.value) || 0, height: Math.round(+fH.value) || 0 });
    };

    function infoText() {
      const [w, h] = fov(optics()), o = optics();
      if (!w) return "Размер рамки неизвестен: выберите оптику или введите фокус, пиксель и размер кадра.";
      const scale = o.pixel / 1000 / (o.focal * (o.reducer > 0 ? o.reducer : 1)) / rad * 3600;
      const deg = v => v >= 1 ? `${ru(v, 2)}°` : `${ru(v * 60, 1)}′`;
      let t = `Поле ${deg(w)} × ${deg(h)}, ${ru(scale, 2)}″/пикс.`;
      if (st.framing) t += " Двигайте небо под рамкой (мышь, палец), поворачивайте её, затем «Применить».";
      else if (st.sel < 0) t += " Выберите объект в таблице — атлас наведётся на него; щелчок по рамке выбирает объект.";
      return t;
    }

    function draw() {
      info.textContent = infoText();
      if (!al || !overlay || !labels) return;
      const [w, h] = fov(optics());
      overlay.removeAll(); dashed.removeAll(); labels.removeAll();
      const c = st.framing ? al.getRaDec() : null;
      st.targets.forEach((t, i) => {
        if (!(w > 0)) return;
        const fr = st.framing && st.framing.i === i;
        const ra = fr ? c[0] : t.ra, dec = fr ? c[1] : t.dec, ang = fr ? st.framing.angle : t.angle || 0;
        overlay.add(A.polygon(frame(ra, dec, w, h, ang), { color: st.colors[i] || "#5ac8ff", lineWidth: i === st.sel ? 3 : 1.5,
          opacity: t.off ? 0.45 : 1 }), false);
        labels.addSources([A.source(ra, dec, { name: t.name || "" })]);
      });
      if (st.preview && w > 0) dashed.add(A.polygon(frame(st.preview.ra, st.preview.dec, w, h, 0), { color: "#c8c8c8", lineWidth: 1.5 }), false);
      al.view.requestRedraw();
    }

    function setFraming(f) {
      st.framing = f;
      frameRow.hidden = !f;
      btnFrame.classList.toggle("on", !!f);
      if (f) { angle.value = String(f.angle); angleNum.value = String(f.angle); }
      draw();
    }
    const turn = v => {
      if (!st.framing) return;
      v = ((+v % 360) + 360) % 360;
      st.framing.angle = Math.round(v * 10) / 10;
      angle.value = String(st.framing.angle);
      if (document.activeElement !== angleNum) angleNum.value = String(st.framing.angle);
      draw();
    };
    angle.oninput = () => turn(angle.value);
    angleNum.onchange = () => turn(angleNum.value);
    btnL.onclick = () => turn(st.framing.angle - 1);
    btnR.onclick = () => turn(st.framing.angle + 1);
    btnFrame.onclick = () => {
      if (st.framing) { setFraming(null); return; }
      const t = st.targets[st.sel];
      if (!t || !al) { info.textContent = "Сначала выберите объект в таблице."; return; }
      al.gotoRaDec(t.ra, t.dec);
      setFraming({ i: st.sel, angle: t.angle || 0 });
    };
    btnCancel.onclick = () => {
      const f = st.framing, t = f && st.targets[f.i];
      setFraming(null);
      if (t && al) al.gotoRaDec(t.ra, t.dec);
    };
    btnApply.onclick = () => {
      const f = st.framing;
      if (!f || !al) return;
      const [ra, dec] = al.getRaDec();
      const t = st.targets[f.i];
      if (t) { t.ra = ra; t.dec = dec; t.angle = f.angle; }
      setFraming(null);
      if (opts.onFrame) opts.onFrame(f.i, Math.round(ra * 1e5) / 1e5, Math.round(dec * 1e5) / 1e5, f.angle);
    };
    survey.onchange = () => { st.survey = survey.value; if (al) al.setBaseImageLayer(st.survey); };

    let photoLayer = null;
    function showPhoto() {
      photoRow.hidden = !st.photo;
      if (!al || !A) return;
      if (photoLayer) { try { al.removeImageLayer("снимок"); } catch (e) { /* gone already */ } photoLayer = null; }
      if (!st.photo) return;
      photoLayer = A.image(st.photo.url, { name: "снимок", imgFormat: "jpeg", wcs: st.photo.wcs, opacity: st.photoOpacity,
        errorCallback: e => { info.textContent = "Снимок не наложен: " + (e && e.message || e); } });
      al.setOverlayImageLayer(photoLayer, "снимок");
    }
    photoOp.oninput = () => { st.photoOpacity = +photoOp.value; if (photoLayer) photoLayer.setOpacity(st.photoOpacity); };
    btnPhotoOff.onclick = () => { st.photo = null; showPhoto(); };
    // start framing target i: its frame in the middle, the field a little wider
    function frameTarget(i) {
      const t = st.targets[i];
      if (!t) return;
      if (!al) { st.pendingFrame = i; return; }
      st.sel = i;
      al.gotoRaDec(t.ra, t.dec);
      const [w, h] = fov(optics());
      const ph = st.photo ? Math.max(st.photo.width || 0, st.photo.height || 0) : 0;
      const span = Math.max(w, h, ph);
      if (span > 0) al.setFoV(Math.min(Math.max(span * 1.6, 0.2), 60));
      setFraming({ i, angle: t.angle || 0 });
    }

    // Aladin is loaded once the atlas is shown
    (async () => {
      try {
        A = (await import(ALADIN)).default;
        await A.init;
        const t = st.targets[st.sel] || st.targets[0];
        const [w] = fov(optics());
        const a = A.aladin(sky, { survey: st.survey, fov: w > 0 ? Math.min(Math.max(w * 3, 0.3), 60) : 5, target: t ? `${t.ra} ${t.dec}` : "0 +0",
          cooFrame: "ICRS", showReticle: false, showContextMenu: false, showProjectionControl: false, showSettingsControl: false,
          showLayersControl: false, showStatusBar: false, showCooGridControl: true, showSimbadPointerControl: true, showFullscreenControl: true });
        overlay = A.graphicOverlay({ name: "рамки", lineWidth: 2 });
        dashed = A.graphicOverlay({ name: "найденный", lineWidth: 1.5, lineDash: [6, 4] });
        // the centres of the targets, named
        labels = A.catalog({ name: "объекты", sourceSize: 10, shape: "plus", color: "#e8e8e8", displayLabel: true, labelColumn: "name",
          labelColor: "#e8e8e8", labelFont: "12px sans-serif" });
        a.addOverlay(overlay); a.addOverlay(dashed); a.addCatalog(labels);
        al = a;
        msg.remove();
        al.on("positionChanged", () => { if (st.framing) draw(); });
        al.on("click", e => {
          if (st.framing || !e || e.isDragging) return;
          const [w, h] = fov(optics());
          if (!(w > 0)) return;
          for (let k = 0; k < st.targets.length; k++) {
            const i = (st.sel + 1 + k) % st.targets.length; // the next of overlapping frames
            const t = st.targets[i];
            if (inFrame(t.ra, t.dec, w, h, t.angle || 0, e.ra, e.dec)) { if (i !== st.sel && opts.onSelect) opts.onSelect(i); return; }
          }
        });
        if (t) al.gotoRaDec(t.ra, t.dec);
        showPhoto();
        if (st.pendingFrame >= 0) { frameTarget(st.pendingFrame); st.pendingFrame = -1; }
        draw();
      } catch (e) {
        failed = true;
        msg.textContent = "Атлас не загрузился (нужен интернет в браузере и WebGL2): " + (e && e.message || e);
      }
    })();

    showOptics();
    draw();
    return {
      // the targets of the plan with their colours and the one chosen
      setTargets(targets, colors, sel) {
        st.targets = (targets || []).map(t => ({ name: t.name, ra: +t.ra, dec: +t.dec, angle: +t.angle || 0, off: !!t.off }));
        st.colors = colors || [];
        if (st.framing && !st.targets[st.framing.i]) setFraming(null);
        st.sel = sel != null && sel >= 0 && sel < st.targets.length ? sel : -1;
        draw();
      },
      // the optics chosen (null: the frames tell) and the setups of the frames
      setOptics(o, setups) {
        st.optics = o || null;
        st.setups = setups || [];
        showOptics();
        draw();
      },
      // a target found but not added yet: a dashed frame there (null: none)
      setPreview(ra, dec) {
        st.preview = ra == null || dec == null || isNaN(ra) || isNaN(dec) ? null : { ra: +ra, dec: +dec };
        if (st.preview && al && !st.framing) al.gotoRaDec(st.preview.ra, st.preview.dec);
        draw();
      },
      // the atlas to target i
      goTo(i) {
        if (st.framing) return;
        st.sel = i;
        const t = st.targets[i];
        if (t && al) {
          al.gotoRaDec(t.ra, t.dec);
          const [w] = fov(optics());
          if (w > 0) al.setFoV(Math.min(Math.max(w * 3, 0.3), 60));
        }
        draw();
      },
      // a frame over the sky: {url, wcs, name, width, height} (null: none)
      setPhoto(p) {
        st.photo = p || null;
        const deg = v => v >= 1 ? `${ru(v, 2)}°` : `${ru(v * 60, 1)}′`;
        photoName.textContent = !p ? "" : (p.name || "") + (p.width > 0 ? ` — поле снимка ${deg(p.width)} × ${deg(p.height)}` : "");
        showPhoto();
      },
      // framing of target i at once, as «Кадрировать» does
      frame(i) { frameTarget(i); },
      get failed() { return failed; },
      get aladin() { return al; },
    };
  }

  window.Atlas = { create, fov, frame, inFrame, fromPlane, toPlane };
})();
