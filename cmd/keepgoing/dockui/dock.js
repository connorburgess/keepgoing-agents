"use strict";
// keepgoing dock page. Reads one versioned JSON endpoint relative to this
// page and renders it with DOM text nodes only; no data is parsed as HTML.
// Ages come from the server's read time, never this browser's clock, so the
// page reads the same wherever it is opened.
(function () {
  var SCHEMA = 1;
  var API = "api/v1/dock";
  var state = { open: {}, data: null, identifiers: false, filter: "all", timer: 0, busy: false, lastOk: null, failed: false };

  var TONES = {
    supervising: "good", waiting_for_you: "attn", waiting_on_limit: "limit", checks_failing: "warn",
    not_checked_recently: "warn", paused: "calm", stopped: "calm", finished: "calm", off: "calm", unknown: "unknown"
  };
  var FILTERS = [
    { id: "all", label: "All", match: function () { return true; } },
    { id: "attention", label: "Needs attention", match: function (l) { return ["waiting_for_you", "checks_failing", "not_checked_recently", "unknown"].indexOf(l.state) >= 0; } },
    { id: "running", label: "Running", match: function (l) { return l.state === "supervising" || l.state === "waiting_on_limit"; } },
    { id: "resting", label: "Paused, off or done", match: function (l) { return ["paused", "stopped", "finished", "off"].indexOf(l.state) >= 0; } }
  ];

  // Icons: one 20px grid, one stroke weight.
  var ICONS = {
    good: '<circle cx="10" cy="10" r="3.2" fill="currentColor" stroke="none"/><circle cx="10" cy="10" r="6.5" opacity=".35"/>',
    attn: '<circle cx="10" cy="10" r="7"/><path d="M10 6.2v4.6"/><path d="M10 13.6v.1"/>',
    limit: '<circle cx="10" cy="10" r="7"/><path d="M10 6v4l2.6 1.6"/>',
    warn: '<path d="M10 3.2 17.3 16H2.7z"/><path d="M10 8.2v3.6"/><path d="M10 13.9v.1"/>',
    paused: '<path d="M7.5 5.5v9"/><path d="M12.5 5.5v9"/>',
    stopped: '<rect x="5.5" y="5.5" width="9" height="9" rx="1.5"/>',
    finished: '<path d="m4.5 10.5 3.6 3.5 7.4-8"/>',
    off: '<path d="M10 3.5v6"/><path d="M6.1 5.8a6 6 0 1 0 7.8 0"/>',
    unknown: '<circle cx="10" cy="10" r="7" stroke-dasharray="2.4 2.4"/>',
    check: '<path d="m4.5 10.5 3.6 3.5 7.4-8"/>',
    chevron: '<path d="m7.5 5 5 5-5 5"/>'
  };
  var STATE_ICON = {
    supervising: "good", waiting_for_you: "attn", waiting_on_limit: "limit", checks_failing: "warn", not_checked_recently: "warn",
    paused: "paused", stopped: "stopped", finished: "finished", off: "off", unknown: "unknown"
  };

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }
  function icon(name) {
    var s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    s.setAttribute("viewBox", "0 0 20 20");
    s.setAttribute("aria-hidden", "true");
    s.innerHTML = ICONS[name] || ICONS.unknown; // fixed markup above, never data
    return s;
  }
  function tag(kind, text) { return el("span", "tag tag-" + kind, text); }

  // Time, relative to the server's read time.
  function readTime() { return state.data ? Date.parse(state.data.readAt) : Date.now(); }
  function span(seconds) {
    var s = Math.abs(seconds), m = Math.round(s / 60);
    if (s < 45) return "under a minute";
    if (m < 60) return m + " min";
    var h = Math.floor(m / 60), mm = m % 60;
    if (h < 48) return h + " h" + (mm && h < 6 ? " " + mm + " min" : "");
    var d = Math.floor(h / 24), hh = h % 24;
    return d + " days" + (hh ? " " + hh + " h" : "");
  }
  function ago(seconds) {
    if (seconds == null) return "at an unknown time";
    if (seconds < -60) return "in the future (check this computer's clock)";
    if (seconds < 45) return "just now";
    return span(seconds) + " ago";
  }
  var timeFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
  var dayFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  function clock(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var near = Math.abs(t - readTime()) < 18 * 3600 * 1000;
    return (near ? timeFmt : dayFmt).format(new Date(t));
  }
  function until(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var s = Math.round((t - readTime()) / 1000);
    if (s <= 0) return clock(iso) + " (passed)";
    return "in " + span(s) + " (" + clock(iso) + ")";
  }

  function fact(label, value) {
    var row = el("div", "fact");
    row.appendChild(el("dt", null, label));
    var dd = el("dd");
    if (typeof value === "string") dd.textContent = value; else dd.appendChild(value);
    row.appendChild(dd);
    return row;
  }
  function frag() { return document.createDocumentFragment(); }
  function unknown(text) { return el("span", "unknown", text || "Unknown"); }
  function joinParts(parts) {
    var f = frag();
    parts.filter(Boolean).forEach(function (p, i) {
      if (i) f.appendChild(el("span", "sep", "·"));
      f.appendChild(typeof p === "string" ? document.createTextNode(p) : p);
    });
    return f;
  }

  // Needs you.
  function renderNeeds(d) {
    var box = $("needs"), body = $("needs-body"), h = $("needs-h");
    body.replaceChildren();
    h.replaceChildren(document.createTextNode("Needs you"));
    if (d.registry.availability !== "available") {
      box.classList.add("is-clear");
      body.appendChild(el("p", "placeholder", "Unknown: the registry could not be read."));
      return;
    }
    if (!d.needsYou.length) {
      box.classList.add("is-clear");
      var p = el("p", "clear");
      p.appendChild(icon("check"));
      p.appendChild(document.createTextNode("Nothing needs you right now."));
      body.appendChild(p);
      return;
    }
    box.classList.remove("is-clear");
    var count = el("span", "count", String(d.needsYou.length));
    count.setAttribute("aria-label", d.needsYou.length + (d.needsYou.length === 1 ? " loop" : " loops"));
    h.appendChild(count);
    var list = el("ol", "need-list");
    d.needsYou.forEach(function (n) {
      var li = el("li", "need");
      var head = el("div", "need-head");
      head.appendChild(el("h3", null, n.loop));
      head.appendChild(el("span", "waited", n.waitedSeconds == null ? "Waiting time unknown" : (n.sinceEvidence === "hold_recorded" ? "Waiting " : "Waiting at least ") + span(n.waitedSeconds)));
      li.appendChild(head);
      li.appendChild(el("p", "reason", n.reason));
      if (n.note) {
        var note = el("div", "note");
        note.appendChild(el("span", "note-label", "Note left with the hold"));
        note.appendChild(document.createTextNode("“" + n.note + "”"));
        li.appendChild(note);
      }
      var todo = el("p", "todo");
      todo.appendChild(el("span", "todo-label", "What to do"));
      todo.appendChild(document.createTextNode(n.action));
      li.appendChild(todo);
      var a = el("a", "jump", "Go to " + n.loop);
      a.href = "#" + n.key;
      a.addEventListener("click", function (ev) { ev.preventDefault(); focusCard(n.key); });
      li.appendChild(a);
      list.appendChild(li);
    });
    body.appendChild(list);
  }

  function focusCard(key) {
    if (state.filter !== "all") { state.filter = "all"; renderLoops(state.data); }
    var card = $(key);
    if (!card) return;
    card.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start" });
    card.focus({ preventScroll: true });
    card.classList.add("is-flash");
    setTimeout(function () { card.classList.remove("is-flash"); }, 1600);
  }

  // Loop cards.
  function children(c) {
    if (c.availability === "current" || c.availability === "held") {
      if (!c.working && !c.stuck && !c.failed) return joinParts([c.phrase, c.ageSeconds != null ? "listed " + ago(c.ageSeconds) : ""]);
      var wrap = el("span", "counts");
      [[c.working, "working", "c-good"], [c.stuck, "waiting for an answer", "c-attn"], [c.failed, "failed", "c-warn"]].forEach(function (x) {
        if (!x[0]) return;
        var item = el("span", "count-item " + x[2]);
        item.appendChild(el("span", "dot"));
        item.appendChild(el("span", "lbl", x[0] + " " + x[1]));
        wrap.appendChild(item);
      });
      var f = frag();
      f.appendChild(wrap);
      var meta = el("div", "sub", (c.availability === "held" ? "As of the check that held it, " : "Listed ") + ago(c.ageSeconds));
      f.appendChild(meta);
      return f;
    }
    if (c.availability === "stale") {
      var s = frag();
      s.appendChild(unknown("Not current"));
      s.appendChild(tag("stale", "Stale"));
      if (c.ageSeconds != null) s.appendChild(el("div", "sub", "Last listed " + ago(c.ageSeconds)));
      return s;
    }
    if (c.availability === "none_recorded") return el("span", "sub", "None recorded");
    return unknown();
  }

  function lastCheck(c) {
    if (c.availability === "never") return unknown("No check recorded yet");
    if (c.availability === "unknown") return unknown();
    var f = joinParts([c.phrase, ago(c.ageSeconds)]);
    if (c.availability === "stale") f.appendChild(tag("stale", "Stale"));
    if (c.failedInRow > 1) f.appendChild(el("div", "sub", c.failedInRow + " checks in a row could not observe or act"));
    return f;
  }

  function nextCheck(n) {
    if (n.availability === "known" && n.at) return document.createTextNode(until(n.at));
    if (n.availability === "in_progress") return document.createTextNode(n.phrase);
    return joinParts([unknown("Unknown"), n.detail ? el("span", "sub", n.detail) : null]);
  }

  function runningOn(t) {
    if (t.availability !== "known") return unknown();
    var who = t.role === "main" ? "Main target" : "Fallback " + t.name;
    return joinParts([who, t.tool, t.provider, t.model]);
  }

  function limits(list, wait) {
    var f = frag();
    if (wait) {
      var w = el("div");
      w.appendChild(document.createTextNode((wait.passed ? "Provider limit reset " : "Provider limit resets ") + (wait.resetsAt ? until(wait.resetsAt) : "at an unknown time")));
      if (!wait.passed) w.appendChild(tag("reached", "Reached"));
      f.appendChild(w);
    }
    list.forEach(function (l) {
      var row = el("div");
      if (l.label) row.appendChild(el("span", "sub", l.label + ": "));
      row.appendChild(l.unknown ? unknown(l.phrase) : document.createTextNode(l.label === "Deadline" ? clock(l.phrase) || l.phrase : l.phrase));
      if (l.reached) row.appendChild(tag("reached", "Reached"));
      f.appendChild(row);
    });
    if (!wait && !list.length) f.appendChild(unknown());
    return f;
  }

  function meters(t) {
    var f = frag();
    t.windows.forEach(function (w) {
      var row = el("div", "meter-row");
      row.appendChild(el("span", "w", w.name));
      var m = el("div", "meter" + (w.stale ? " is-stale" : w.usedPercent >= 90 ? " full" : w.usedPercent >= 70 ? " hi" : ""));
      m.setAttribute("role", "img");
      m.setAttribute("aria-label", w.name + " window " + (w.stale ? "reading is stale" : w.usedPercent + " percent used"));
      var bar = el("i");
      bar.style.width = (w.stale ? 100 : Math.max(2, w.usedPercent)) + "%";
      m.appendChild(bar);
      row.appendChild(m);
      row.appendChild(el("span", "pct", w.stale ? "—" : w.usedPercent + "%"));
      if (w.resetsAt) row.appendChild(el("span", "meter-reset", w.stale ? "That window ended " + clock(w.resetsAt) : "Resets " + until(w.resetsAt)));
      f.appendChild(row);
    });
    return f;
  }

  var READY_TONE = { main: "calm", ready: "good", not_ready: "warn", not_probed: "unknown", disabled: "calm", unknown: "unknown" };

  // The handoff chain is an ordered list with one row per target, keyed by
  // position and ID, so reorder/add/remove controls can attach to rows later.
  function chain(c, f, key) {
    var box = el("details", "chain");
    box.open = !!state.open[key];
    box.addEventListener("toggle", function () { state.open[key] = box.open; });
    var head = el("summary", "chain-head");
    head.appendChild(icon("chevron"));
    head.appendChild(el("span", "chain-title", "Handoff chain"));
    var fallbackCount = c.entries.length - 1 + c.omitted;
    var brief;
    if (c.availability === "none_configured") brief = el("span", "sub", "No fallbacks set up");
    else if (f && f.availability === "known") brief = el("span", "chain-ready" + (f.eligible > 0 && !f.enough ? " is-short" : ""), f.eligible ? f.ready + " of " + f.eligible + " fallbacks ready" : "No fallback can take over");
    else brief = unknown("Readiness unknown");
    head.appendChild(brief);
    box.appendChild(head);
    var how = c.availability === "none_configured" ? "Nothing takes over if the main target cannot continue." :
      c.handoffOn.length ? "Hands off automatically on " + c.handoffOn.map(function (x) { return x.replace(/_/g, " ") + "s"; }).join(", ") + ", to the first ready fallback in this order." :
      "Automatic handoff is off. " + fallbackCount + (fallbackCount === 1 ? " fallback is" : " fallbacks are") + " set up for a manual move.";
    box.appendChild(el("p", "sub chain-how", how));
    var ol = el("ol", "chain-list");
    c.entries.forEach(function (e) {
      var li = el("li", "link" + (e.active ? " is-active" : "") + (e.disabled ? " is-off" : ""));
      li.dataset.position = String(e.position);
      li.dataset.id = e.id;
      var pos = el("span", "pos", e.position === 0 ? "Main" : String(e.position));
      pos.setAttribute("aria-hidden", "true");
      li.appendChild(pos);
      var body = el("div", "link-body");
      var top = el("div", "link-top");
      var name = el("span", "link-name", e.position === 0 ? "Main target" : e.id);
      top.appendChild(name);
      if (e.active) top.appendChild(el("span", "in-use", "In use"));
      if (e.position > 0) {
        var rd = el("span", "ready tone-" + (READY_TONE[e.readiness] || "unknown"), e.phrase);
        top.appendChild(rd);
      }
      body.appendChild(top);
      var meta = joinParts([e.tool, e.provider, e.model]);
      var metaEl = el("div", "sub link-meta");
      metaEl.appendChild(meta);
      if (e.probeAgeSeconds != null) {
        metaEl.appendChild(el("span", "sep", "·"));
        metaEl.appendChild(document.createTextNode("checked " + ago(e.probeAgeSeconds)));
      }
      body.appendChild(metaEl);
      var u = e.usage;
      if (u.availability === "unavailable") {
        body.appendChild(el("div", "sub unknown usage-none", "No usage reading"));
      } else {
        var uh = el("div", "sub usage-read");
        uh.appendChild(document.createTextNode("Usage read " + ago(u.ageSeconds)));
        if (u.availability === "stale") uh.appendChild(tag("stale", "Stale"));
        body.appendChild(uh);
        body.appendChild(meters(u));
      }
      li.appendChild(body);
      li.setAttribute("aria-label", (e.position === 0 ? "Main target" : "Fallback " + e.position + ", " + e.id) + (e.active ? ", in use" : ""));
      ol.appendChild(li);
    });
    box.appendChild(ol);
    if (c.omitted) box.appendChild(el("p", "sub", c.omitted + " more not shown"));
    return box;
  }

  function card(l) {
    var li = el("li");
    var a = el("article", "card");
    a.id = l.key;
    a.tabIndex = -1;
    a.setAttribute("aria-labelledby", l.key + "-name");
    var head = el("div", "card-head");
    var h = el("h3", null, l.name);
    h.id = l.key + "-name";
    head.appendChild(h);
    var st = el("span", "state tone-" + (TONES[l.state] || "unknown"));
    st.appendChild(icon(STATE_ICON[l.state] || "unknown"));
    st.appendChild(document.createTextNode(l.phrase));
    head.appendChild(st);
    a.appendChild(head);
    a.appendChild(el("p", "detail", l.detail));

    var dl = el("dl", "facts");
    dl.appendChild(fact("Running on", runningOn(l.runningOn)));
    dl.appendChild(fact("Child work", children(l.children)));
    dl.appendChild(fact("Last check", lastCheck(l.lastCheck)));
    dl.appendChild(fact("Next check", nextCheck(l.nextCheck)));
    dl.appendChild(fact("Limits", limits(l.limits, l.limitWait)));
    a.appendChild(dl);

    a.appendChild(chain(l.chain, l.fallbacks, l.key));

    if (l.issues.length) {
      var ul = el("ul", "issues");
      ul.setAttribute("aria-label", "Missing evidence");
      l.issues.forEach(function (i) { ul.appendChild(el("li", null, i)); });
      a.appendChild(ul);
    }
    if (state.identifiers && l.identifiers) {
      var ids = el("dl", "ids");
      ids.appendChild(el("dt", null, "Registration"));
      ids.appendChild(el("dd", null, l.identifiers.registrationId));
      if (l.identifiers.thread) {
        ids.appendChild(el("dt", null, "Configured BB thread"));
        ids.appendChild(el("dd", null, l.identifiers.thread));
      }
      a.appendChild(ids);
    }
    li.appendChild(a);
    return li;
  }

  function renderFilters(d) {
    var box = $("filters");
    box.replaceChildren();
    FILTERS.forEach(function (f) {
      var n = d.loops.filter(f.match).length;
      if (f.id !== "all" && n === 0 && state.filter !== f.id) return;
      var b = el("button", "chip");
      b.type = "button";
      b.setAttribute("aria-pressed", String(state.filter === f.id));
      b.appendChild(el("span", null, f.label));
      b.appendChild(el("span", "n", String(n)));
      b.addEventListener("click", function () { state.filter = f.id; renderLoops(state.data); box.querySelector('[aria-pressed="true"]').focus(); });
      box.appendChild(b);
    });
  }

  function renderLoops(d) {
    var grid = $("grid");
    grid.replaceChildren();
    renderFilters(d);
    if (d.registry.availability !== "available") {
      grid.appendChild(emptyItem("The registry could not be read.", "Nothing is shown rather than guessing. Check it with keepgoing registry list."));
      return;
    }
    if (!d.loops.length) {
      grid.appendChild(emptyItem("No loops are registered yet.", "Register one with keepgoing registry add -c path/to/keepgoing.yaml, and it appears here on the next read."));
      return;
    }
    var f = FILTERS.filter(function (x) { return x.id === state.filter; })[0] || FILTERS[0];
    var shown = d.loops.filter(f.match);
    if (!shown.length) {
      grid.appendChild(emptyItem("No loops match this view.", "Choose All to see every loop."));
      return;
    }
    shown.forEach(function (l) { grid.appendChild(card(l)); });
    if (d.registry.omitted) grid.appendChild(emptyItem(d.registry.omitted + " more registered loops are not shown.", ""));
  }
  function emptyItem(title, body) {
    var li = el("li", "empty");
    li.style.gridColumn = "1 / -1";
    li.appendChild(el("p", null, title));
    if (body) li.appendChild(el("p", "sub", body));
    return li;
  }

  function renderLede(d) {
    if (d.registry.availability !== "available") { $("lede").textContent = "The registry could not be read."; return; }
    var n = d.loops.length, parts = [n + (n === 1 ? " loop" : " loops")];
    var waiting = d.needsYou.length;
    if (waiting) parts.push(waiting + " " + (waiting === 1 ? "needs" : "need") + " you");
    var sup = d.loops.filter(function (l) { return l.state === "supervising"; }).length;
    if (sup) parts.push(sup + " supervising");
    $("lede").textContent = parts.join(" · ") + ". Read from keepgoing's own records; nothing here is a live view of a conversation.";
  }

  function renderReadline() {
    var r = $("readline");
    if (!state.lastOk) { r.textContent = state.failed ? "Could not read yet" : "Reading…"; return; }
    var every = state.data.refreshSeconds;
    r.textContent = "Read at " + new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit", second: "2-digit" }).format(new Date(state.data.readAt)) +
      " · reads again every " + (every >= 60 ? span(every) : every + " s");
    r.classList.toggle("is-stale", state.failed);
  }

  function render() {
    var d = state.data;
    renderLede(d);
    renderNeeds(d);
    renderLoops(d);
    renderReadline();
    document.title = (d.needsYou.length ? "(" + d.needsYou.length + ") " : "") + "keepgoing";
  }

  function banner(text) {
    var b = $("banner");
    b.hidden = !text;
    b.textContent = text || "";
  }

  function schedule() {
    clearTimeout(state.timer);
    var every = state.data ? state.data.refreshSeconds : 15;
    state.timer = setTimeout(load, Math.max(5, every) * 1000);
  }

  function load() {
    if (state.busy) return;
    state.busy = true;
    var btn = $("refresh");
    btn.setAttribute("aria-busy", "true");
    fetch(API + (state.identifiers ? "?identifiers=1" : ""), { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .then(function (d) {
        if (!d || d.schemaVersion !== SCHEMA) throw new Error("schema");
        state.data = d; state.lastOk = Date.now(); state.failed = false;
        banner("");
        render();
      })
      .catch(function (err) {
        state.failed = true;
        var schema = err && err.message === "schema";
        banner(schema ? "This page does not understand the server's reply. Reload the page, or restart keepgoing dock." :
          state.data ? "The last read failed, so what you see may be out of date. keepgoing dock may have stopped; it tries again shortly." :
          "Could not read from keepgoing dock. It may have stopped; start it again and open the new address it prints.");
        renderReadline();
      })
      .then(function () {
        state.busy = false;
        btn.removeAttribute("aria-busy");
        schedule();
      });
  }

  document.addEventListener("DOMContentLoaded", function () {
    $("refresh").addEventListener("click", function () { clearTimeout(state.timer); load(); });
    $("identifiers").addEventListener("click", function () {
      state.identifiers = !state.identifiers;
      this.setAttribute("aria-pressed", String(state.identifiers));
      this.querySelector("span").textContent = state.identifiers ? "Hide identifiers" : "Show identifiers";
      this.title = this.querySelector("span").textContent;
      clearTimeout(state.timer);
      load();
    });
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && state.lastOk && Date.now() - state.lastOk > 5000) { clearTimeout(state.timer); load(); }
    });
    load();
  });
})();
