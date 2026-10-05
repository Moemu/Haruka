/* Haruka 状态页逻辑：30 秒轮询公开快照，按设计案 §10 映射渲染。
   所有动态文本一律 textContent 写入，绝不拼接 HTML。 */
(() => {
  "use strict";

  var REFRESH_MS = 30000;
  var STALE_MS = 120000; // generated_at 超过 2 分钟 → 状态暂不可用（设计案 §10）
  var LOCATION_STALE_MS = 1800000; // 位置观测超过 30 分钟 → 标注「有点旧了」

  var src = document.body.dataset.statusSrc || "/v1/public/status";

  var $ = function (id) { return document.getElementById(id); };
  var els = {
    banner: $("banner"),
    bannerState: $("banner-state"),
    bannerMeta: $("banner-meta"),
    activityText: $("activity-text"),
    activityAsof: $("activity-asof"),
    locationText: $("location-text"),
    locationAsof: $("location-asof"),
    moodText: $("mood-text"),
    moodAsof: $("mood-asof"),
    footerSync: $("footer-sync"),
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

  var PHRASE = {
    active: "在线！正在大显身手 ✧",
    away: "摸鱼去了 (´･ω･`)",
    unknown: "在线？在做什么还是个谜喵…",
    offline: "下线补番去了 zzZ",
  };

  // mood 是协议封闭枚举（ingest 白名单），展示层映射为中文+emoji；
  // 未知值原样展示，不猜。
  var MOOD_LABEL = {
    happy: "😊 开心",
    focused: "🎯 专注中",
    tired: "🥱 累了…",
    anxious: "😰 有点慌",
    chilling: "🍵 摸鱼中",
    sick: "🤒 不舒服",
  };

  function setField(bodyEl, asofEl, text, asof, stale) {
    bodyEl.textContent = text;
    asofEl.textContent = asof || "";
    asofEl.classList.toggle("stale", !!stale);
  }

  function mode(name) {
    document.body.className = "mode-" + name;
  }

  function render(snap) {
    if (snap.visibility === "hidden") {
      els.hiddenUntil.textContent = snap.hidden_until
        ? "预计 " + clock(snap.hidden_until) + " 解除"
        : "想回来的时候自然会回来~";
      mode("hidden");
      return;
    }

    var pv = snap.presence || { state: "unknown" };
    var st = PHRASE[pv.state] ? pv.state : "unknown";
    els.banner.dataset.state = st;
    els.bannerState.textContent = PHRASE[st];
    if (st === "offline") {
      els.bannerMeta.textContent = (pv.as_of || pv.last_active_at)
        ? "最后活跃于 " + rel(pv.as_of || pv.last_active_at)
        : "还没有报过信";
    } else {
      els.bannerMeta.textContent = pv.as_of ? rel(pv.as_of) + "观测" : "";
    }

    if (snap.activity) {
      setField(els.activityText, els.activityAsof, snap.activity.display, rel(snap.activity.as_of), false);
    } else {
      setField(els.activityText, els.activityAsof, "什么都没在做的样子…真的吗？(・ω・)", "", false);
    }

    if (snap.location && snap.location.display) {
      var locAge = Date.now() - Date.parse(snap.location.as_of);
      var locStale = !Number.isNaN(locAge) && locAge > LOCATION_STALE_MS;
      setField(els.locationText, els.locationAsof, snap.location.display, rel(snap.location.as_of), locStale);
    } else {
      setField(els.locationText, els.locationAsof, "位置是个秘密", "", false);
    }

    if (snap.mood) {
      var moodWord = snap.mood.mood || "";
      var moodLabel = MOOD_LABEL[moodWord] || moodWord;
      var moodText = [moodLabel, snap.mood.text].filter(Boolean).join(" — ");
      setField(els.moodText, els.moodAsof, moodText || "心情平静如水~", snap.mood.until ? "至 " + clock(snap.mood.until) + " 解除" : "", false);
    } else {
      setField(els.moodText, els.moodAsof, "心情平静如水~", "", false);
    }

    var gen = new Date(snap.generated_at);
    els.footerSync.textContent = "数据时间 " +
      (isNaN(gen.getTime()) ? "" : gen.toLocaleTimeString("zh-CN", { hour12: false })) +
      " · 每 30 秒自动刷新";
    mode("normal");
  }

  function renderError(kind, snap) {
    if (kind === "loading") {
      els.errorTitle.textContent = "正在连接灯塔…";
      els.errorBody.textContent = "少女正在爬上灯塔，请稍等哦 (๑•̀ㅂ•́)و✧";
      els.errorMeta.textContent = "";
    } else {
      els.errorTitle.textContent = "灯塔信号丢失…";
      els.errorBody.textContent = "(╥﹏╥) 状态暂不可用，先去逛逛别的地方吧";
      var seen = snap && snap.generated_at ? snap.generated_at : (lastSnap && lastSnap.generated_at);
      els.errorMeta.textContent = seen ? "数据时间 " + rel(seen) : "暂时拿不到数据";
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

  renderError("loading");
  start();
})();
