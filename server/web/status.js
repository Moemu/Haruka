/* Haruka 状态页逻辑：30 秒轮询公开快照，平静叙述渲染（设计案 §10）。
   所有动态文本一律 textContent 写入，绝不拼接 HTML。 */
(() => {
  "use strict";

  var REFRESH_MS = 30000;
  var STALE_MS = 120000; // generated_at 超过 2 分钟 → 状态暂不可用（设计案 §10）
  var LOCATION_STALE_MS = 1800000; // 位置观测超过 30 分钟 → 标注「有点旧了」

  var src = document.body.dataset.statusSrc || "/v1/public/status";

  var $ = function (id) { return document.getElementById(id); };
  var els = {
    heroState: $("hero-state"),
    heroWord: $("hero-word"),
    heroMeta: $("hero-meta"),
    activityText: $("activity-text"),
    activityAsof: $("activity-asof"),
    locationText: $("location-text"),
    locationAsof: $("location-asof"),
    moodText: $("mood-text"),
    moodAsof: $("mood-asof"),
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

  // presence 词即文案：大字直接展示，meta 负责时间语境。
  var STATE_WORD = { active: "在线", away: "离开", unknown: "状态未知", offline: "离线" };

  // mood 是协议封闭枚举（ingest 白名单），展示层映射为中文；未知值原样展示，不猜。
  var MOOD_LABEL = {
    happy: "开心",
    focused: "专注",
    tired: "累了",
    anxious: "有点慌",
    chilling: "放松中",
    sick: "不舒服",
  };

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
    svg.appendChild(svgEl("text", { x: 32, y: 38, "class": "ring-num" }, String(level)));
    row.appendChild(svg);

    var meta = document.createElement("div");
    meta.className = "device-meta";
    var name = document.createElement("p");
    name.className = "device-name";
    name.textContent = DEVICE_LABEL[d.kind] || "设备";
    var sub = document.createElement("p");
    sub.className = "device-sub";
    var subText = charging ? "充电中" : (low ? "电量 " + level + "% · 该充电啦" : "电量 " + level + "%");
    if (d.steps > 0) { subText += " · 今日 " + d.steps + " 步"; }
    sub.textContent = subText;
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

    if (snap.mood) {
      var moodWord = snap.mood.mood || "";
      var moodLabel = MOOD_LABEL[moodWord] || moodWord;
      var moodText = [moodLabel, snap.mood.text].filter(Boolean).join(" — ");
      setField(els.moodText, els.moodAsof, moodText || "一切如常", snap.mood.until ? "至 " + clock(snap.mood.until) + " 有效" : "", false);
    } else {
      setField(els.moodText, els.moodAsof, "一切如常", "", false);
    }

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

  function renderError(kind, snap) {
    if (kind === "loading") {
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
  start();
})();
