/* Haruka 状态页逻辑：30 秒轮询公开快照，平静叙述渲染（设计案 §10）。
   所有动态文本一律 textContent 写入，绝不拼接 HTML。 */
(() => {
  "use strict";

  var REFRESH_MS = 30000;
  var STALE_MS = 120000; // generated_at 超过 2 分钟 → 状态暂不可用（设计案 §10）
  var LOCATION_STALE_MS = 1800000; // 位置观测超过 30 分钟 → 标注「有点旧了」

  var src = document.body.dataset.statusSrc || "/v1/public/status";
  var CAPS_SRC = "/v1/capabilities";

  var $ = function (id) { return document.getElementById(id); };
  var els = {
    heroState: $("hero-state"),
    heroWord: $("hero-word"),
    heroMeta: $("hero-meta"),
    activityText: $("activity-text"),
    activityAsof: $("activity-asof"),
    locationText: $("location-text"),
    locationAsof: $("location-asof"),
    pluginCards: $("plugin-cards"),
    cardDevices: $("card-devices"),
    devicesList: $("devices-list"),
    footerSync: $("footer-sync"),
    week: document.querySelectorAll("#week-row span"),
    hiddenUntil: $("hidden-until"),
    errorTitle: $("error-title"),
    errorBody: $("error-body"),
    errorMeta: $("error-meta"),
  };

  var lastSnap = null;
  var timer = null;
  var manifest = [];   // capabilities.plugins：插件卡清单（端口 3）
  var pluginRefs = {}; // name -> { card, body, subs[], asof }

  function rel(iso) {
    var t = Date.parse(iso);
    if (Number.isNaN(t)) { return ""; }
    var s = Math.max(0, Math.floor((Date.now() - t) / 1000));
    if (s < 60) { return "刚刚"; }
    var m = Math.floor(s / 60);
    if (m < 60) { return m + " 分钟前"; }
    var h = Math.floor(m / 60);
    if (h < 24) { return h + " 小时前"; }
    return Math.floor(h / 24) + " 天前";
  }

  function clock(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return ""; }
    return d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
  }

  // 时长：分钟起步；累计到小时后取整并用千分位。
  function dur(sec) {
    var m = Math.round(sec / 60);
    var h = Math.floor(m / 60);
    return h >= 1 ? h + " 小时 " + (m % 60) + " 分" : m + " 分钟";
  }
  function durTotal(sec) {
    return sec < 3600 ? dur(sec) : Math.round(sec / 3600).toLocaleString("zh-CN") + " 小时";
  }

  // presence 词即文案：大字直接展示，meta 负责时间语境。
  var STATE_WORD = { active: "在线", away: "离开", unknown: "状态未知", offline: "离线" };

  var DEVICE_LABEL = { desktop: "电脑", android: "手机" };
  var SVGNS = "http://www.w3.org/2000/svg";
  var RING_C = 2 * Math.PI * 26; // 电量环周长（viewBox 64, r=26）

  // 电量环：SVG 逐节点构建，不碰 innerHTML。
  function svgEl(name, attrs, text) {
    var node = document.createElementNS(SVGNS, name);
    for (var k in attrs) { node.setAttribute(k, attrs[k]); }
    if (text !== undefined) { node.textContent = text; }
    return node;
  }

  // 设备 kind → 环心图标（24 视窗线性形，与卡头 chip 图标同体系）。
  // 圆环本身继续表达电量，环心只作设备身份；未知 kind 回退桌面形。
  var KIND_ICON = {
    desktop: [
      ["rect", { x: 2, y: 3, width: 20, height: 14, rx: 2 }],
      ["path", { d: "M8 21h8" }],
      ["path", { d: "M12 17v4" }],
    ],
    android: [
      ["rect", { x: 7, y: 2.5, width: 10, height: 19, rx: 2.5 }],
      ["path", { d: "M11 18.5h2" }],
    ],
  };

  // 插件卡图标表（清单 icon 字段 → 线性形）；未知名回退中性圆点。
  var PLUGIN_ICON = {
    mood: [
      ["circle", { cx: 12, cy: 12, r: 9 }],
      ["path", { d: "M8.5 13.5a5 5 0 0 0 7 0" }],
      ["path", { d: "M9 9.5h.01M15 9.5h.01" }],
    ],
    persona: [
      ["circle", { cx: 12, cy: 8, r: 4 }],
      ["path", { d: "M4 21c0-4.2 3.6-6.5 8-6.5s8 2.3 8 6.5" }],
    ],
    coding: [
      ["path", { d: "m8 7-5 5 5 5" }],
      ["path", { d: "m16 7 5 5-5 5" }],
    ],
    reading: [
      ["path", { d: "M4 19.5A2.5 2.5 0 0 1 6.5 17H20" }],
      ["path", { d: "M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2Z" }],
    ],
  };

  function deviceRow(d) {
    var level = Math.max(0, Math.min(100, d.battery ? d.battery.level : 0));
    var charging = !!(d.battery && d.battery.charging);
    var low = level < 20 && !charging;

    var row = document.createElement("div");
    row.className = "device" + (charging ? " charging" : "") + (low ? " low" : "");

    var svg = svgEl("svg", { viewBox: "0 0 64 64", "class": "ring", "aria-hidden": "true" });
    svg.appendChild(svgEl("circle", { cx: 32, cy: 32, r: 26, "class": "ring-track" }));
    svg.appendChild(svgEl("circle", {
      cx: 32, cy: 32, r: 26, "class": "ring-arc",
      "stroke-dasharray": RING_C.toFixed(1),
      "stroke-dashoffset": (RING_C * (1 - level / 100)).toFixed(1),
      transform: "rotate(-90 32 32)",
    }));
    var icon = svgEl("svg", { x: 20, y: 20, width: 24, height: 24, viewBox: "0 0 24 24", "class": "ring-icon" });
    (KIND_ICON[d.kind] || KIND_ICON.desktop).forEach(function (shape) {
      icon.appendChild(svgEl(shape[0], shape[1]));
    });
    svg.appendChild(icon);
    row.appendChild(svg);

    var meta = document.createElement("div");
    meta.className = "device-meta";
    var name = document.createElement("p");
    name.className = "device-name";
    name.textContent = d.name || DEVICE_LABEL[d.kind] || "设备";
    var sub = document.createElement("p");
    sub.className = "device-sub";
    // 各片段整体不拆行（CJK 任意字间可断，宽裕度紧张时会撕词）：
    // 装不下时整段换行，断点只落在段间的分隔符上。
    var parts = [];
    if (charging) {
      parts.push("充电中");
    } else {
      var levelPart = document.createElement("span");
      levelPart.style.whiteSpace = "nowrap";
      levelPart.textContent = "电量 " + level + "%";
      parts.push(levelPart);
      if (low) { parts.push("该充电啦"); }
    }
    if (d.steps > 0) {
      var stepsPart = document.createElement("span");
      stepsPart.style.whiteSpace = "nowrap";
      stepsPart.textContent = "今日 " + d.steps + " 步";
      parts.push(stepsPart);
    }
    parts.forEach(function (part, i) {
      if (i > 0) { sub.appendChild(document.createTextNode(" · ")); }
      if (typeof part === "string") { sub.appendChild(document.createTextNode(part)); }
      else { sub.appendChild(part); }
    });
    meta.appendChild(name);
    meta.appendChild(sub);
    row.appendChild(meta);
    return row;
  }

  function setField(bodyEl, asofEl, text, asof, stale) {
    bodyEl.textContent = text;
    asofEl.textContent = asof || "";
    asofEl.classList.toggle("stale", !!stale);
  }

  function mode(name) {
    document.body.className = "mode-" + name;
  }

  // 页脚星期行：当天高亮，全页唯一的幽默位。
  function markToday() {
    var d = new Date().getDay();
    for (var i = 0; i < els.week.length; i++) {
      els.week[i].classList.toggle("today", i === d);
    }
  }

  function render(snap) {
    markToday();

    if (snap.visibility === "hidden") {
      els.hiddenUntil.textContent = snap.hidden_until
        ? "预计 " + clock(snap.hidden_until) + " 恢复"
        : "恢复时间未定";
      mode("hidden");
      return;
    }

    var pv = snap.presence || { state: "unknown" };
    var st = STATE_WORD[pv.state] ? pv.state : "unknown";
    els.heroState.dataset.state = st;
    els.heroWord.textContent = STATE_WORD[st];
    if (st === "offline") {
      els.heroMeta.textContent = (pv.as_of || pv.last_active_at)
        ? "最后活跃于 " + rel(pv.as_of || pv.last_active_at)
        : "还没有上报过状态";
    } else {
      els.heroMeta.textContent = pv.as_of ? rel(pv.as_of) + "上报" : "";
    }

    if (snap.activity) {
      setField(els.activityText, els.activityAsof, snap.activity.display, rel(snap.activity.as_of), false);
    } else {
      setField(els.activityText, els.activityAsof, "暂时没有活动记录", "", false);
    }

    if (snap.location && snap.location.display) {
      var locAge = Date.now() - Date.parse(snap.location.as_of);
      var locStale = !Number.isNaN(locAge) && locAge > LOCATION_STALE_MS;
      setField(els.locationText, els.locationAsof, snap.location.display, rel(snap.location.as_of), locStale);
    } else {
      setField(els.locationText, els.locationAsof, "位置未公开", "", false);
    }

    // 插件卡：清单驱动通用渲染——卡片只由自身插件的数据决定，
    // 节缺席即不出卡，插件之间零感知（设计案 §7.7）。
    renderPlugins(snap);

    // 设备状态：只列出上报了电量数据的在线设备；全部缺席时整卡隐藏。
    while (els.devicesList.firstChild) { els.devicesList.removeChild(els.devicesList.firstChild); }
    var devs = snap.devices || [];
    els.cardDevices.hidden = devs.length === 0;
    devs.forEach(function (d) { els.devicesList.appendChild(deviceRow(d)); });

    var gen = new Date(snap.generated_at);
    els.footerSync.textContent = "数据时间 " +
      (isNaN(gen.getTime()) ? "" : gen.toLocaleTimeString("zh-CN", { hour12: false })) +
      " · 每 30 秒自动刷新";
    mode("normal");
  }

  // --- 插件卡：清单驱动通用渲染（状态页端口） ---

  // 定制渲染钩子：特殊视觉的插件在此按名字注册（缺省走通用渲染）。
  var CARD_RENDERERS = {};

  function chipSvg(iconName) {
    var chip = document.createElement("span");
    chip.className = "chip";
    chip.setAttribute("aria-hidden", "true");
    var svg = svgEl("svg", { viewBox: "0 0 24 24" });
    (PLUGIN_ICON[iconName] || [["circle", { cx: 12, cy: 12, r: 9 }]]).forEach(function (shape) {
      svg.appendChild(svgEl(shape[0], shape[1]));
    });
    chip.appendChild(svg);
    return chip;
  }

  // buildPluginCards 依据清单构建卡片 DOM（每插件一张，缺数据 hidden）。
  function buildPluginCards() {
    while (els.pluginCards.firstChild) { els.pluginCards.removeChild(els.pluginCards.firstChild); }
    pluginRefs = {};
    manifest.forEach(function (p) {
      var card = document.createElement("article");
      card.className = "card";
      card.hidden = true;

      var h2 = document.createElement("h2");
      h2.appendChild(chipSvg(p.icon));
      h2.appendChild(document.createTextNode(p.title || p.name));
      card.appendChild(h2);

      var refs = { card: card, body: null, subs: [], asof: null };
      (p.fields || []).forEach(function (f, i) {
        if (i === 0) {
          refs.body = document.createElement("p");
          refs.body.className = "card-body";
          refs.body.textContent = "…";
          card.appendChild(refs.body);
        } else {
          var sub = document.createElement("p");
          sub.className = "card-sub";
          card.appendChild(sub);
          refs.subs.push(sub);
        }
      });
      refs.asof = document.createElement("p");
      refs.asof.className = "card-asof";
      card.appendChild(refs.asof);

      els.pluginCards.appendChild(card);
      pluginRefs[p.name] = refs;
    });
  }

  // 字段值格式化：labels 映射优先，随后按 format 口径。
  function formatValue(f, v) {
    if (v == null) { return ""; }
    if (f.labels && f.labels[v]) { return f.labels[v]; }
    switch (f.format) {
      case "duration": return dur(Number(v));
      case "duration_total": return durTotal(Number(v));
      case "time": return clock(v);
      case "number": return Number(v).toLocaleString("zh-CN");
      default: return String(v);
    }
  }

  function renderPlugins(snap) {
    manifest.forEach(function (p) {
      var refs = pluginRefs[p.name];
      if (!refs) { return; }
      var sec = snap[p.name];
      if (!sec) { refs.card.hidden = true; return; }
      refs.card.hidden = false;
      if (CARD_RENDERERS[p.name]) { CARD_RENDERERS[p.name](sec, refs); return; }
      (p.fields || []).forEach(function (f, i) {
        var text = formatValue(f, sec[f.key]);
        if (i === 0) {
          refs.body.textContent = text || "—";
        } else {
          refs.subs[i - 1].textContent = f.label ? f.label + "：" + text : text;
        }
      });
      refs.asof.textContent = sec.as_of ? rel(sec.as_of) : "";
    });
  }

  // loadManifest 拉插件清单（页面装载一次；失败 = 无插件卡，核心卡照常）。
  function loadManifest() {
    return fetch(CAPS_SRC, { headers: { Accept: "application/json" } })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (cap) {
        manifest = (cap && cap.plugins) || [];
        buildPluginCards();
        if (lastSnap) { render(lastSnap); }
      })
      .catch(function () { manifest = []; });
  }

  function renderError(kind, snap) {    if (kind === "loading") {
      els.errorTitle.textContent = "正在连接…";
      els.errorBody.textContent = "正在获取最新状态。";
      els.errorMeta.textContent = "";
    } else {
      els.errorTitle.textContent = "状态暂时不可用";
      els.errorBody.textContent = kind === "stale" ? "数据已超过两分钟没有更新。" : "连接失败，稍后会自动重试。";
      var seen = snap && snap.generated_at ? snap.generated_at : (lastSnap && lastSnap.generated_at);
      els.errorMeta.textContent = seen ? "最后数据 " + rel(seen) : "暂时拿不到数据";
    }
    mode("error");
  }

  function tick() {
    return fetch(src, { headers: { Accept: "application/json" } })
      .then(function (res) {
        if (!res.ok) { throw new Error("HTTP " + res.status); }
        return res.json();
      })
      .then(function (snap) {
        lastSnap = snap;
        var age = Date.now() - Date.parse(snap.generated_at);
        if (!Number.isNaN(age) && age > STALE_MS) {
          renderError("stale", snap);
        } else {
          render(snap);
        }
      })
      .catch(function () {
        renderError("fail");
      });
  }

  function start() {
    if (timer) { return; }
    tick();
    timer = setInterval(tick, REFRESH_MS);
  }

  function stop() {
    if (timer) { clearInterval(timer); timer = null; }
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) { stop(); } else { start(); }
  });

  markToday();
  renderError("loading");
  loadManifest();
  start();
})();
