(function () {
  var base = document.body.dataset.base || "";
  var q = document.getElementById("q"), box = document.getElementById("results");
  var index = null, sel = -1;

  document.querySelector(".menu").addEventListener("click", function () { document.body.classList.toggle("open"); });

  function load(cb) {
    if (index) return cb();
    fetch(base + "/search.json").then(function (r) { return r.json(); }).then(function (j) { index = j; cb(); });
  }
  function esc(s) { return s.replace(/[&<>"]/g, function (c) { return {"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]; }); }

  function search(term) {
    var words = term.toLowerCase().split(/\s+/).filter(Boolean), out = [];
    index.forEach(function (e) {
      var t = e.t.toLowerCase(), x = e.x.toLowerCase(), score = 0;
      for (var i = 0; i < words.length; i++) {
        var w = words[i], inT = t.indexOf(w) >= 0, inX = x.indexOf(w);
        if (!inT && inX < 0) return;
        score += (inT ? 10 : 0) + (inX >= 0 ? 1 : 0);
      }
      var at = Math.max(0, x.indexOf(words[0]) - 40);
      if (at > 0) { var sp = e.x.indexOf(" ", at); at = sp < 0 ? at : sp + 1; } // start on a word
      out.push({ e: e, score: score, snip: e.x.substr(at, 110) });
    });
    out.sort(function (a, b) { return b.score - a.score; });
    return out.slice(0, 8);
  }

  function show(term) {
    if (!term.trim()) { box.hidden = true; return; }
    load(function () {
      var r = search(term); sel = -1;
      box.innerHTML = r.length ? r.map(function (o) {
        return '<a href="' + o.e.u + '"><b>' + esc(o.e.t) + '</b><small>' + esc(o.e.s) + ' — ' + esc(o.snip) + '…</small></a>';
      }).join("") : '<div class="none">No results</div>';
      box.hidden = false;
    });
  }
  q.addEventListener("input", function () { show(q.value); });
  q.addEventListener("keydown", function (ev) {
    var items = box.querySelectorAll("a");
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      ev.preventDefault(); if (!items.length) return;
      sel = (sel + (ev.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
      items.forEach(function (a, i) { a.classList.toggle("sel", i === sel); });
    } else if (ev.key === "Enter" && items.length) { items[Math.max(sel, 0)].click(); }
    else if (ev.key === "Escape") { q.value = ""; box.hidden = true; q.blur(); }
  });
  document.addEventListener("click", function (e) { if (!e.target.closest(".search")) box.hidden = true; });

  document.addEventListener("keydown", function (e) {
    var t = e.target.tagName;
    if (t === "INPUT" || t === "TEXTAREA") return;
    if (e.key === "/" || ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k")) { e.preventDefault(); q.focus(); return; }
    if (e.altKey || e.metaKey || e.ctrlKey) return;
    var a = document.querySelector(e.key === "ArrowLeft" ? ".pager a:not(.next)" : e.key === "ArrowRight" ? ".pager a.next" : "x-none");
    if (a) location.href = a.href;
  });

  // current section in the right-hand index
  var links = [].slice.call(document.querySelectorAll("aside a"));
  if (links.length && "IntersectionObserver" in window) {
    var heads = links.map(function (a) { return document.getElementById(a.getAttribute("href").slice(1)); });
    var io = new IntersectionObserver(function (es) {
      es.forEach(function (en) {
        if (!en.isIntersecting) return;
        links.forEach(function (a) { a.classList.toggle("on", a.getAttribute("href") === "#" + en.target.id); });
      });
    }, { rootMargin: "-70px 0px -70% 0px" });
    heads.forEach(function (h) { if (h) io.observe(h); });
  }
})();
