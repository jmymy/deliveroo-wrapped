/* =============================================================================
   DIRECTION A — interaction layer (vanilla JS, no deps)
   count-ups · scroll reveals · scene observer · year selector · confetti
   Loaded after data.js. Respects prefers-reduced-motion.
   ========================================================================== */
(function () {
  "use strict";
  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* The year selector is server-rendered as ?year= links (active state set by
     the template), so no client-side persistence is needed. */

  /* ---- format helpers ---------------------------------------------------- */
  function fmt(n, opts) {
    opts = opts || {};
    if (opts.money) return "£" + Number(n).toLocaleString("en-GB", { minimumFractionDigits: opts.dp || 0, maximumFractionDigits: opts.dp || 0 });
    return Number(n).toLocaleString("en-GB", { maximumFractionDigits: opts.dp || 0, minimumFractionDigits: opts.dp || 0 });
  }
  window.RooFmt = fmt;

  /* ---- count-up ---------------------------------------------------------- */
  function countUp(el) {
    var target = parseFloat(el.dataset.count);
    var dp = parseInt(el.dataset.dp || "0", 10);
    var prefix = el.dataset.prefix || "";
    var suffix = el.dataset.suffix || "";
    if (REDUCED) { el.textContent = prefix + Number(target).toLocaleString("en-GB", { minimumFractionDigits: dp, maximumFractionDigits: dp }) + suffix; return; }
    var dur = 1500, start = null;
    function step(ts) {
      if (!start) start = ts;
      var p = Math.min((ts - start) / dur, 1);
      var eased = 1 - Math.pow(1 - p, 4);
      var val = target * eased;
      el.textContent = prefix + Number(val).toLocaleString("en-GB", { minimumFractionDigits: dp, maximumFractionDigits: dp }) + suffix;
      if (p < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }

  /* ---- generic reveal on enter ------------------------------------------ */
  function observeReveals() {
    var els = document.querySelectorAll(".card");
    if (!els.length) return;
    if (REDUCED) { els.forEach(function (e) { e.classList.add("in"); }); return; }
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting) { en.target.classList.add("in"); io.unobserve(en.target); }
      });
    }, { threshold: .18 });
    els.forEach(function (e) { io.observe(e); });
  }

  /* ---- count-ups that fire when visible --------------------------------- */
  function observeCounts() {
    var els = document.querySelectorAll("[data-count]");
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting) { countUp(en.target); io.unobserve(en.target); }
      });
    }, { threshold: .4 });
    els.forEach(function (e) { io.observe(e); });
  }

  /* ---- bar fills --------------------------------------------------------- */
  function observeBars() {
    var bars = document.querySelectorAll(".bar-fill[data-w]");
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting) { en.target.style.width = en.target.dataset.w + "%"; io.unobserve(en.target); }
      });
    }, { threshold: .3 });
    bars.forEach(function (b) { io.observe(b); });
  }

  /* =========================================================================
     STORY engine — scene snapping, active dot, progress, confetti on finale
     ====================================================================== */
  function initStory() {
    var scroller = document.querySelector(".scenes");
    if (!scroller) return;
    var scenes = Array.prototype.slice.call(scroller.querySelectorAll(".scene"));
    var dotsWrap = document.querySelector(".story-dots");
    var prog = document.querySelector(".story-prog");
    var topbar = document.querySelector(".topbar");

    // build dots
    scenes.forEach(function (s, i) {
      var b = document.createElement("button");
      b.setAttribute("aria-label", "Go to scene " + (i + 1));
      b.addEventListener("click", function () { scenes[i].scrollIntoView({ behavior: REDUCED ? "auto" : "smooth", block: "start" }); });
      // note: scrollIntoView on a snap child inside our own scroller is safe here
      dotsWrap.appendChild(b);
    });
    var dots = dotsWrap.querySelectorAll("button");

    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        var idx = scenes.indexOf(en.target);
        if (en.isIntersecting) {
          en.target.classList.add("in");
          dots.forEach(function (d, i) { d.classList.toggle("active", i === idx); });
          // top bar tints to match scene darkness
          if (topbar) topbar.classList.toggle("on-dark", !en.target.classList.contains("bg-paper") && !en.target.classList.contains("bg-amber") && !en.target.classList.contains("bg-teal"));
          if (en.target.dataset.finale === "1") fireConfetti();
        }
      });
    }, { root: scroller, threshold: .55 });
    scenes.forEach(function (s) { io.observe(s); });

    // progress bar
    scroller.addEventListener("scroll", function () {
      var max = scroller.scrollHeight - scroller.clientHeight;
      if (prog) prog.style.width = (scroller.scrollTop / max * 100) + "%";
    }, { passive: true });

    // keyboard nav
    document.addEventListener("keydown", function (e) {
      var cur = scenes.findIndex(function (s) { return s.classList.contains("in"); });
      if (e.key === "ArrowDown" || e.key === "ArrowRight" || e.key === " ") { e.preventDefault(); if (scenes[cur + 1]) scenes[cur + 1].scrollIntoView({ behavior: "smooth" }); }
      if (e.key === "ArrowUp" || e.key === "ArrowLeft") { e.preventDefault(); if (scenes[cur - 1]) scenes[cur - 1].scrollIntoView({ behavior: "smooth" }); }
    });
  }

  /* ---- confetti (lightweight canvas) ------------------------------------ */
  var confettiFired = false;
  function fireConfetti() {
    if (confettiFired || REDUCED) return;
    confettiFired = true;
    var cv = document.getElementById("confetti");
    if (!cv) return;
    var ctx = cv.getContext("2d");
    var W = cv.width = innerWidth, H = cv.height = innerHeight;
    addEventListener("resize", function () { W = cv.width = innerWidth; H = cv.height = innerHeight; });
    var colors = ["#00CCBC", "#FF5E5B", "#FFB400", "#7B61FF", "#FF8A00", "#ffffff"];
    var bits = [];
    for (var i = 0; i < 180; i++) {
      bits.push({ x: Math.random() * W, y: -20 - Math.random() * H, r: 5 + Math.random() * 7,
        c: colors[i % colors.length], vy: 2 + Math.random() * 4, vx: -1.5 + Math.random() * 3,
        rot: Math.random() * 6.28, vr: -.1 + Math.random() * .2 });
    }
    var t0 = performance.now();
    (function frame(now) {
      ctx.clearRect(0, 0, W, H);
      bits.forEach(function (b) {
        b.y += b.vy; b.x += b.vx; b.rot += b.vr;
        if (b.y > H + 20) b.y = -20;
        ctx.save(); ctx.translate(b.x, b.y); ctx.rotate(b.rot); ctx.fillStyle = b.c;
        ctx.fillRect(-b.r / 2, -b.r / 2, b.r, b.r * .6); ctx.restore();
      });
      if (now - t0 < 5000) requestAnimationFrame(frame);
      else ctx.clearRect(0, 0, W, H);
    })(t0);
  }
  window.RooConfetti = fireConfetti;

  /* ---- parallax floaty shapes on pointer move --------------------------- */
  function initParallax() {
    if (REDUCED) return;
    var floats = document.querySelectorAll("[data-parallax]");
    if (!floats.length) return;
    window.addEventListener("pointermove", function (e) {
      var cx = (e.clientX / innerWidth - .5), cy = (e.clientY / innerHeight - .5);
      floats.forEach(function (f) {
        var d = parseFloat(f.dataset.parallax) || 20;
        f.style.transform = "translate(" + (cx * d) + "px," + (cy * d) + "px)";
      });
    }, { passive: true });
  }

  /* ---- share-card download (html-to-canvas-free: uses SVG foreignObject) - */
  function wireDownloads() {
    document.querySelectorAll("[data-dl]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var node = document.getElementById(btn.dataset.dl);
        if (!node) return;
        // Prototype affordance: in production the Go app renders a PNG server-side
        // or html2canvas is wired here. We flash feedback so the gesture is real.
        var old = btn.innerHTML;
        btn.innerHTML = "Saved ✓";
        node.style.outline = "4px solid #fff";
        setTimeout(function () { btn.innerHTML = old; node.style.outline = "none"; }, 1400);
      });
    });
  }

  /* ---- boot -------------------------------------------------------------- */
  document.addEventListener("DOMContentLoaded", function () {
    observeReveals();
    observeCounts();
    observeBars();
    initStory();
    initParallax();
    wireDownloads();
    if (REDUCED) { var cv = document.getElementById("confetti"); if (cv) cv.style.display = "none"; }
  });
})();
