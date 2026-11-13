"use strict";
// keepgoing panel page. Reads one versioned JSON endpoint relative to this
// page and renders it with DOM text nodes only; no data is parsed as HTML.
// Ages come from the report's read time, never this browser's clock, so the
// page reads the same wherever it is opened. Every sentence about a loop or a
// route is sent by the report, which the Go tests check word for word; the
// page only shows its times in this browser's time zone.
(function () {
  var SCHEMA = 1;
  var API = "api/v1/dock";
  var SERVER = "api/v1/server";
  var state = { data: null, identifiers: false, paused: false, timer: 0, busy: false, again: false, lastOk: null, failed: false, pending: false, said: "", server: null };

  var TONES = {
    supervising: "good", waiting_for_you: "attn", waiting_on_limit: "limit", waiting_on_network: "limit", checks_failing: "warn",
    not_checked_recently: "warn", outside_hours: "calm", paused: "calm", stopped: "calm", finished: "calm", off: "calm", unknown: "unknown"
  };
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
    supervising: "good", waiting_for_you: "attn", waiting_on_limit: "limit", waiting_on_network: "limit", checks_failing: "warn",
    not_checked_recently: "warn", outside_hours: "limit", paused: "paused", stopped: "stopped", finished: "finished", off: "off", unknown: "unknown"
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
  function link(href, text, cls) {
    var a = el("a", cls || null, text);
    a.href = href;
    return a;
  }
  function list(v) { return Array.isArray(v) ? v : []; }

  // Time. Every sentence comes from the report as parts: literal text, or a
  // time shown here in this browser's own time zone. Ages and countdowns are
  // already text, computed against the report's read time, so the page reads
  // the same wherever it is opened. A time that does not parse is said to be
  // unknown; it never throws.
  function readTime() { var t = state.data ? Date.parse(state.data.readAt) : NaN; return isNaN(t) ? Date.now() : t; }
  function span(seconds) {
    var s = Math.abs(seconds), m = Math.round(s / 60);
    if (s < 45) return "under a minute";
    if (m < 60) return m + " min";
    var h = Math.floor(m / 60), mm = m % 60;
    if (h < 48) return h + " h" + (mm && h < 6 ? " " + mm + " min" : "");
    var d = Math.floor(h / 24), hh = h % 24;
    return d + " days" + (hh ? " " + hh + " h" : "");
  }
  var timeFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
  var dayTimeFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  var dayTimeYearFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", year: "numeric", hour: "numeric", minute: "2-digit" });
  var dateFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short" });
  var dateYearFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short", year: "numeric" });
  var secFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit", second: "2-digit" });
  var YEAR = 365 * 24 * 3600 * 1000, CLOCK = 18 * 3600 * 1000;
  // date: "Thu 12 Nov"; datetime: "Wed 18 Nov, 19:12"; clock: a time of day
  // within 18 hours of the read, otherwise date and time. The year is added
  // when the time is more than a year from the read.
  function when(p) {
    var t = Date.parse(p.at);
    if (isNaN(t)) return "an unknown time";
    var d = new Date(t), far = Math.abs(t - readTime()) > YEAR;
    if (p.f === "date") return (far ? dateYearFmt : dateFmt).format(d);
    if (p.f === "clock" && !far && Math.abs(t - readTime()) < CLOCK) return timeFmt.format(d);
    return (far ? dayTimeYearFmt : dayTimeFmt).format(d);
  }
  function fmt(f, iso) { var t = Date.parse(iso); return isNaN(t) ? "an unknown time" : f.format(new Date(t)); }
  function say(parts) {
    if (typeof parts === "string") return parts;
    return list(parts).map(function (p) { return p && p.at ? when(p) : (p && p.t) || ""; }).join("");
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

  // Routing: "#/" is the overview, "#/loop/<key>" one loop, "#/routes" every
  // route and "#/routes/<key>" every route, at that one.
  function route() {
    var h = location.hash.replace(/^#\/?/, "");
    var m = /^routes(?:\/(r[0-9a-f]+))?$/.exec(h);
    if (m) return { view: "routes", key: m[1] || "" };
    m = /^loop\/([a-z0-9]+)$/.exec(h);
    if (m) return { view: "loop", key: m[1] };
    return { view: "home" };
  }
  function loopByKey(key) { return list(state.data.loops).filter(function (l) { return l.key === key; })[0]; }
  function needFor(key) { return list(state.data.needsYou).filter(function (n) { return n.key === key; })[0]; }

  // ---- Small pieces shared by the views ----

  function lastCheck(c) {
    if (c.availability === "never" || c.availability === "unknown" || !c.text) return unknown(c.text);
    var f = frag();
    f.appendChild(document.createTextNode(c.text));
    if (c.availability === "stale") f.appendChild(tag("stale", "Stale"));
    if (c.failed) f.appendChild(el("div", "sub", c.failed));
    return f;
  }
  function nextCheck(n) {
    if (n.availability === "known") return document.createTextNode(say(n.text));
    if (n.availability === "in_progress") return document.createTextNode(n.phrase);
    return joinParts([unknown("Unknown"), n.detail ? el("span", "sub", n.detail) : null]);
  }
  function limits(items, wait) {
    var f = frag();
    if (wait) {
      var w = el("div", null, say(wait.text));
      if (!wait.passed) w.appendChild(tag("reached", "Reached"));
      f.appendChild(w);
    }
    list(items).forEach(function (l) {
      var row = el("div");
      if (l.label) row.appendChild(el("span", "sub", l.label + ": "));
      row.appendChild(l.unknown ? unknown(say(l.text)) : document.createTextNode(say(l.text)));
      if (l.reached) row.appendChild(tag("reached", "Reached"));
      f.appendChild(row);
    });
    if (!wait && !list(items).length) f.appendChild(unknown());
    return f;
  }

  // Usage windows as meters. A stale window is drawn empty and hatched: it
  // says nothing about how much is used now.
  function meters(t) {
    var f = frag();
    list(t.windows).forEach(function (w) {
      var row = el("div", "meter-row");
      row.appendChild(el("span", "w", w.name));
      var m = el("div", "meter" + (w.stale ? " is-stale" : w.usedPercent >= 100 ? " full" : w.usedPercent >= 70 ? " hi" : ""));
      m.setAttribute("role", "img");
      m.setAttribute("aria-label", w.name + " window " + (w.stale ? "reading is stale" : w.usedPercent + " percent used"));
      var bar = el("i");
      bar.style.width = (w.stale ? 0 : Math.max(2, w.usedPercent)) + "%";
      m.appendChild(bar);
      row.appendChild(m);
      row.appendChild(el("span", "pct", w.stale ? "—" : w.usedPercent + "%"));
      if (list(w.reset).length) row.appendChild(el("span", "meter-reset", say(w.reset)));
      f.appendChild(row);
    });
    if (t.omitted) f.appendChild(el("p", "more", t.omitted + (t.omitted === 1 ? " more window is" : " more windows are") + " not shown."));
    return f;
  }
  function usageBlock(u) {
    u = u || { availability: "unavailable" };
    var box = el("div", "route-usage");
    if (u.availability === "unavailable") {
      box.appendChild(el("div", "sub unknown usage-none", (u.readLine || "No usage reading") + (u.reason ? ": " + u.reason : "")));
      return box;
    }
    var uh = el("div", "sub usage-read", u.readLine || "Usage reading");
    if (u.availability === "stale") uh.appendChild(tag("stale", "Stale"));
    box.appendChild(uh);
    box.appendChild(meters(u));
    return box;
  }
  function notesOf(notes) { return list(notes).map(function (n) { return { tone: n.tone, text: say(n.text) }; }); }

  // The route component: one route (a model on an account at an effort
  // level). It leads with what is recorded against it, then readiness tied to
  // its test, then what the route is. A loop's routes and the view across
  // loops both render it. Its data-route is the opaque route key.
  var READY_TONE = { ready: "good", ready_overdue: "calm", not_ready: "warn", not_probed: "unknown", disabled: "calm", unknown: "unknown" };
  function routeItem(o) {
    var li = el("li", "route" + (o.active ? " is-active" : "") + (o.off ? " is-off" : ""));
    if (o.anchor) { li.id = o.anchor; li.tabIndex = -1; }
    if (o.route) li.dataset.route = o.route;
    if (o.position != null) li.dataset.position = String(o.position);
    if (o.badge) {
      var pos = el("span", "pos", o.badge);
      pos.setAttribute("aria-hidden", "true");
      li.appendChild(pos);
    }
    var body = el("div", "route-body");
    var top = el("div", "route-top");
    var name = el("h3", "route-name", o.title);
    if (o.titleAccount) name.appendChild(el("span", "route-account", " on " + o.titleAccount));
    top.appendChild(name);
    if (o.use) top.appendChild(el("span", o.active ? "in-use" : "in-use is-recorded", o.use));
    body.appendChild(top);
    list(o.notes).forEach(function (n) { body.appendChild(el("p", "route-note is-" + (n.tone || "plain"), n.text)); });
    if (o.readiness) {
      var st = el("div", "route-status tone-" + (READY_TONE[o.readiness] || "unknown"));
      st.appendChild(el("span", "dot"));
      st.appendChild(el("span", null, o.phrase));
      body.appendChild(st);
    }
    list(o.after).forEach(function (t) { body.appendChild(el("p", "route-note is-plain", t)); });
    var meta = el("div", "sub route-meta");
    meta.appendChild(joinParts(list(o.meta).concat(state.identifiers && o.named ? [o.named] : [])));
    body.appendChild(meta);
    body.appendChild(usageBlock(o.usage));
    list(o.boxes).forEach(function (b) { body.appendChild(b); });
    li.appendChild(body);
    return li;
  }
  function box(label, content) {
    var b = el("div", "lanes");
    b.appendChild(el("span", "lanes-label", label));
    b.appendChild(content);
    return b;
  }

  // A hold's note is shown only with identifiers and notes; the card says one
  // exists and offers to show it.
  function showNotesButton() {
    var b = el("button", "linkish", "Show identifiers and notes");
    b.type = "button";
    b.addEventListener("click", function () { if (!state.identifiers) $("identifiers").click(); });
    return b;
  }
  function needCard(n, onOverview) {
    var li = el("li", "need");
    var head = el("div", "need-head");
    if (onOverview) {
      var h = el("h2", "need-name");
      h.appendChild(link("#/loop/" + n.key, n.loop));
      head.appendChild(h);
    }
    head.appendChild(el("span", "waited", n.waited || "Waiting time unknown"));
    li.appendChild(head);
    li.appendChild(el("p", "reason", n.reason));
    if (n.note && state.identifiers) {
      var note = el("div", "note");
      note.appendChild(el("span", "note-label", "Note left with the hold"));
      note.appendChild(document.createTextNode("“" + n.note + "”"));
      li.appendChild(note);
    } else if (n.noteWithheld || n.note) {
      var p = el("p", "sub", "A note was left with the hold. ");
      p.appendChild(showNotesButton());
      li.appendChild(p);
    }
    var todo = el("p", "todo");
    todo.appendChild(el("span", "todo-label", "What to do"));
    todo.appendChild(document.createTextNode(n.action));
    li.appendChild(todo);
    return li;
  }

  // ---- Level 1: the picker ----
  // The needs-you line and the Routes view stay put; only the loop list
  // scrolls, so neither can scroll out of reach.
  function pickRow(href, dotClass, name, sub, current) {
    var a = el("a", "pick");
    a.href = href;
    if (current) a.setAttribute("aria-current", "page");
    a.appendChild(el("span", "dot " + dotClass));
    var n = el("span", "pick-name", name);
    n.title = name;
    a.appendChild(n);
    var s = el("span", "pick-state", sub);
    s.title = sub;
    a.appendChild(s);
    var go = icon("chevron");
    go.setAttribute("class", "go");
    a.appendChild(go);
    return a;
  }
  function renderPicker(d, r) {
    var nav = $("picker");
    var old = nav.querySelector(".pick-scroll");
    var scrollTop = old ? old.scrollTop : 0;
    nav.replaceChildren();

    var fixed = el("div", "pick-fixed");
    var needs = list(d.needsYou).length;
    var top = el("a", "pick pick-needs" + (needs ? "" : " is-clear"));
    top.href = "#/";
    if (r.view === "home") top.setAttribute("aria-current", "page");
    if (needs) top.appendChild(el("span", "count", String(needs)));
    else top.appendChild(icon(d.allClear ? "check" : "unknown"));
    var line = d.needsLine || "Needs you: unknown";
    top.appendChild(el("span", "pick-needs-text", needs ? line.replace(/^\d+ /, "") : line));
    if (needs) top.setAttribute("aria-label", line);
    top.appendChild(icon("chevron"));
    fixed.appendChild(top);
    var rs = d.routesSummary || {};
    fixed.appendChild(pickRow("#/routes", rs.trouble ? "dot-limit" : "dot-calm", "Routes", rs.picker || "", r.view === "routes"));
    nav.appendChild(fixed);

    var label = el("p", "pick-label", "Loops");
    label.id = "pick-loops";
    nav.appendChild(label);
    var scroll = el("div", "pick-scroll");
    var ul = el("ul", "pick-list");
    ul.setAttribute("aria-labelledby", "pick-loops");
    list(d.loops).forEach(function (l) {
      var li = el("li");
      li.appendChild(pickRow("#/loop/" + l.key, "dot-" + (TONES[l.state] || "unknown"), l.name, l.pickerState, r.view === "loop" && r.key === l.key));
      ul.appendChild(li);
    });
    if (!list(d.loops).length) ul.appendChild(el("li", "sub pick-empty", d.registry.availability === "available" ? "No loops registered yet" : "Unknown"));
    scroll.appendChild(ul);
    if (d.registry.omitted) scroll.appendChild(el("p", "sub pick-label", d.registry.omitted + " more not shown"));
    nav.appendChild(scroll);
    scroll.scrollTop = scrollTop;
  }

  function paneHead(title, pill) {
    var head = el("div", "pane-head");
    var h = el("h1", null, title);
    h.id = "pane-title";
    head.appendChild(h);
    if (pill) head.appendChild(pill);
    return head;
  }
  function back() {
    var a = link("#/", "", "back");
    a.appendChild(icon("chevron"));
    a.appendChild(document.createTextNode("All loops"));
    return a;
  }
  function panel(title, extra) {
    var p = el("section", "panel");
    var h = el("h2", null, title);
    list(extra).forEach(function (x) { if (x) h.appendChild(x); });
    p.appendChild(h);
    return p;
  }
  function emptyBox(title, body) {
    var b = el("div", "empty");
    b.appendChild(el("p", null, title));
    if (body) b.appendChild(el("p", "sub", body));
    return b;
  }

  // ---- Overview: what needs you, then what else is not routine ----
  function renderHome(d, pane) {
    pane.appendChild(paneHead("Needs you"));
    if (d.registry.availability !== "available") {
      pane.appendChild(el("p", "lede", "Unknown."));
      pane.appendChild(emptyBox("The registry could not be read.", "Nothing is shown rather than guessing. Check it with keepgoing registry list."));
      return;
    }
    var needs = list(d.needsYou);
    if (d.lede) pane.appendChild(el("p", "lede", d.lede));
    if (!list(d.loops).length) {
      pane.appendChild(emptyBox("No loops are registered yet.", "Register one with keepgoing registry add -c path/to/keepgoing.yaml, and it appears here on the next read."));
      return;
    }
    if (!needs.length && !d.allClear) {
      pane.appendChild(emptyBox("Not every loop was read.", "So this cannot say that nothing needs you. It reads again shortly."));
    } else if (!needs.length) {
      var p = el("p", "clear panel");
      p.appendChild(icon("check"));
      p.appendChild(document.createTextNode("Nothing needs you: every registered loop was read and none is held."));
      pane.appendChild(p);
    } else {
      var ol = el("ol", "need-list");
      needs.forEach(function (x) { ol.appendChild(needCard(x, true)); });
      var wrap = el("div", "panel is-need");
      wrap.appendChild(ol);
      pane.appendChild(wrap);
    }
    // Loops that do not need a person but are not routine either: waiting on
    // something, not checked, or on a later route.
    var worth = list(d.worth);
    if (worth.length) {
      var sec = panel("Worth knowing", [el("span", "sub", "not waiting on you")]);
      var ul = el("ul", "worth");
      worth.forEach(function (w) {
        var li = el("li");
        li.appendChild(el("span", "dot dot-" + (TONES[w.state] || "unknown")));
        var t = el("div");
        t.appendChild(link("#/loop/" + w.key, w.loop));
        t.appendChild(el("span", "sub", say(w.text)));
        li.appendChild(t);
        ul.appendChild(li);
      });
      sec.appendChild(ul);
      pane.appendChild(sec);
    }
  }

  // Last decision, hold and route: short answers under the loop's name.
  function nowRow(l) {
    var dl = el("dl", "now");
    function card(label, value, sub) {
      var d = el("div");
      d.appendChild(el("dt", null, label));
      var dd = el("dd");
      if (typeof value === "string") dd.textContent = value; else dd.appendChild(value);
      if (sub) dd.appendChild(el("span", "sub", sub));
      d.appendChild(dd);
      dl.appendChild(d);
    }
    var dec = l.decision;
    if (dec.availability !== "known") card("Last decision", unknown(dec.phrase));
    else card("Last decision", dec.phrase, say(dec.sub));
    var h = l.hold;
    if (h.state === "unknown") card("Hold", unknown());
    else if (h.state === "none") card("Hold", "None");
    else card("Hold", h.phrase, h.sub);
    if (l.away) card("Route", say(l.away.short), say(l.away.why));
    return dl;
  }

  // Each lane of the last verified listing: its state, then what it is (its
  // title when one is recorded, otherwise its number).
  function laneBox(l) {
    var c = l.children, lanes = l.lanes, rows = list(lanes && lanes.lanes);
    var b = el("div", "lanes is-wide");
    b.appendChild(el("span", "lanes-label", (lanes && lanes.heading) || "This loop's lanes"));
    if (!rows.length) {
      b.appendChild(!c || c.availability === "unknown" || !c.phrase ? unknown() : el("span", "sub", c.phrase));
      return b;
    }
    var ul = el("ul", "lane-list");
    rows.forEach(function (x) {
      var li = el("li", "lane");
      li.appendChild(el("span", "dot " + (x.state === "working" ? "dot-good" : x.state === "waiting" ? "dot-attn" : x.state === "failed" ? "dot-warn" : "dot-unknown")));
      li.appendChild(el("span", "lane-state", x.phrase));
      var what = el("span", "lane-what");
      what.appendChild(el("span", "lane-title", x.title || "Lane " + x.number));
      if (x.loop) {
        what.appendChild(document.createTextNode(" · also a registered loop: "));
        what.appendChild(link("#/loop/" + x.loopKey, x.loop));
      }
      if (x.key && state.identifiers) {
        what.appendChild(document.createTextNode(" · "));
        what.appendChild(el("code", null, x.key));
      }
      li.appendChild(what);
      ul.appendChild(li);
    });
    b.appendChild(ul);
    if (lanes && lanes.footer) b.appendChild(el("p", "more", lanes.footer));
    return b;
  }

  // The newest retained history entries, newest first.
  function eventsPanel(ev) {
    var p = panel("Recent activity", [el("span", "sub", "from the retained history; newest first")]);
    if (ev.availability === "unknown") { p.appendChild(unknown("Unknown: the history could not be read.")); return p; }
    var events = list(ev.events);
    if (!events.length) { p.appendChild(el("p", "sub chain-how", "Nothing is recorded yet.")); return p; }
    var ol = el("ol", "events");
    events.forEach(function (x) {
      var li = el("li", "event is-" + x.tone);
      var t = el("time", null, say(x.when));
      t.dateTime = x.at;
      li.appendChild(t);
      li.appendChild(el("span", "mark"));
      var body = el("div", null, x.phrase);
      if (x.detail) body.appendChild(el("span", "sub", x.detail));
      if (list(x.foldedText).length) body.appendChild(el("span", "sub", say(x.foldedText)));
      li.appendChild(body);
      ol.appendChild(li);
    });
    p.appendChild(ol);
    var more = el("p", "more", (ev.footer ? ev.footer + " " : "") + "More with ");
    more.appendChild(el("code", null, "keepgoing timeline"));
    more.appendChild(document.createTextNode("."));
    p.appendChild(more);
    return p;
  }

  // ---- Level 2: one loop, shown through its routes ----
  function renderLoop(d, pane, key) {
    pane.appendChild(back());
    var l = loopByKey(key);
    if (!l) {
      pane.appendChild(paneHead("Loop not found"));
      pane.appendChild(emptyBox("This loop is no longer registered.", "Pick another from the list."));
      return;
    }
    var st = el("span", "state tone-" + (TONES[l.state] || "unknown"));
    st.appendChild(icon(STATE_ICON[l.state] || "unknown"));
    st.appendChild(document.createTextNode(l.phrase));
    pane.appendChild(paneHead(l.name, st));
    var need = needFor(key);
    if (!need) pane.appendChild(el("p", "detail", l.detail));
    if (need) {
      var nb = el("div", "panel is-need");
      var nl = el("ol", "need-list");
      nl.appendChild(needCard(need, false));
      nb.appendChild(nl);
      pane.appendChild(nb);
    }
    pane.appendChild(nowRow(l));

    // The ordered preferred routes are the main structure.
    var c = l.chain, entries = list(c.entries);
    var brief = !c.brief ? null : c.availability !== "none_configured" && c.brief.indexOf("unknown") >= 0 ? unknown(c.brief) :
      el("span", c.availability === "none_configured" ? "sub" : "chain-ready" + (c.short ? " is-short" : ""), c.brief);
    var routing = panel("Preferred routes", [brief]);
    routing.appendChild(el("p", "sub chain-how", c.how || "Unknown."));
    var ol = el("ol", "route-list is-ordered");
    entries.forEach(function (e) {
      var item = routeItem({
        route: e.route, position: e.position, badge: String(e.position + 1), title: e.title, titleAccount: e.titleAccount, active: e.inUse, use: e.use, off: e.disabled,
        readiness: e.position > 0 && e.readiness !== "current" ? e.readiness : "", phrase: e.phrase, notes: notesOf(e.notes), after: e.after,
        meta: e.meta, named: e.named, usage: e.usage, boxes: e.current ? [laneBox(l)] : []
      });
      item.setAttribute("aria-label", "Choice " + (e.position + 1) + ": " + e.title + (e.use ? ", " + e.use : ""));
      ol.appendChild(item);
    });
    routing.appendChild(ol);
    if (c.more) routing.appendChild(el("p", "sub", c.more));
    if (!entries.some(function (e) { return e.current; })) routing.appendChild(laneBox(l));
    pane.appendChild(routing);

    pane.appendChild(eventsPanel(l.events));

    var checks = panel("Checks and limits");
    var dl = el("dl", "facts"), dev = l.device || {};
    dl.appendChild(fact("Last check", lastCheck(l.lastCheck)));
    dl.appendChild(fact("Next check", nextCheck(l.nextCheck)));
    dl.appendChild(fact("Limits", limits(l.limits, l.limitWait)));
    dl.appendChild(fact("Device", (state.identifiers && dev.hostText) || dev.text || unknown()));
    checks.appendChild(dl);
    if (list(l.issues).length) {
      var ul = el("ul", "issues");
      ul.setAttribute("aria-label", "Missing evidence");
      l.issues.forEach(function (i) { ul.appendChild(el("li", null, i)); });
      checks.appendChild(ul);
    }
    if (state.identifiers && l.identifiers) {
      var ids = el("dl", "ids");
      ids.appendChild(el("dt", null, "Registration"));
      ids.appendChild(el("dd", null, l.identifiers.registrationId));
      if (l.identifiers.thread) {
        ids.appendChild(el("dt", null, "Configured BB thread"));
        ids.appendChild(el("dd", null, l.identifiers.thread));
      }
      checks.appendChild(ids);
    }
    pane.appendChild(checks);
  }

  // ---- Routes across loops ----
  // One line per account: the newest recorded reading and its resets.
  function allowancesPanel(d) {
    var items = list(d.allowances);
    var p = panel("Allowances", [el("span", "sub", "the newest reading recorded for each account; not a live reading")]);
    if (!items.length) { p.appendChild(el("p", "sub chain-how", "No accounts to show.")); return p; }
    var ul = el("ul", "allow-list");
    items.forEach(function (a) {
      var li = el("li", "allow");
      var head = el("div", "allow-head");
      head.appendChild(el("h3", "allow-name", a.title));
      head.appendChild(el("span", "sub", a.sub));
      li.appendChild(head);
      var body = el("div", "allow-body");
      notesOf(a.notes).forEach(function (n) { body.appendChild(el("p", "route-note is-" + n.tone, n.text)); });
      var blk = usageBlock(a.usage);
      if (list(a.next).length) blk.appendChild(el("div", "sub allow-next", say(a.next)));
      body.appendChild(blk);
      li.appendChild(body);
      ul.appendChild(li);
    });
    p.appendChild(ul);
    if (d.allowancesOmitted) p.appendChild(el("p", "more", d.allowancesOmitted + (d.allowancesOmitted === 1 ? " more account is" : " more accounts are") + " not shown."));
    return p;
  }

  function renderRoutes(d, pane) {
    pane.appendChild(back());
    pane.appendChild(paneHead("Routes"));
    pane.appendChild(el("p", "lede", "Each model on an account that a loop runs on or keeps on standby, as configured. keepgoing does not verify which account a thread is using."));
    var routes = list(d.routes), rs = d.routesSummary || {};
    if (!routes.length) {
      pane.appendChild(emptyBox(d.registry.availability === "available" ? "No routes to show." : "Unknown: the registry could not be read.", ""));
      return;
    }
    var sum = el("p", "route-summary");
    sum.appendChild(el("span", null, rs.total || routes.length + " routes"));
    if (rs.inUseText) sum.appendChild(el("span", "c-good", rs.inUseText));
    if (rs.trouble && rs.first) sum.appendChild(link("#/routes/" + rs.first, rs.problem, "c-warn"));
    else if (rs.problem) sum.appendChild(el("span", "sub", rs.problem));
    pane.appendChild(sum);
    pane.appendChild(allowancesPanel(d));

    var wrap = panel("All routes", [el("span", "sub", "those at a limit or not ready first")]);
    var ul = el("ul", "route-list");
    // Loops on the route, one per line with their state.
    function loopLines(items) {
      var u = el("ul");
      items.forEach(function (x) {
        var li = el("li");
        li.appendChild(link("#/loop/" + x.key, x.loop));
        li.appendChild(el("span", "sub", " · " + (x.line || x.phrase || "")));
        u.appendChild(li);
      });
      return u;
    }
    // Loops that only keep it on standby, grouped by which choice it is for
    // them, since the order is the point of preferred routes.
    function byChoice(groups) {
      var w = el("div");
      groups.forEach(function (g) {
        var p = el("p", "standby", g.label + " ");
        list(g.loops).forEach(function (x, i) {
          if (i) p.appendChild(document.createTextNode(", "));
          p.appendChild(link("#/loop/" + x.key, x.loop));
        });
        w.appendChild(p);
      });
      return w;
    }
    routes.forEach(function (n) {
      var running = list(n.runningNow), recorded = list(n.recordedFor), standby = list(n.standby), unk = list(n.unknown);
      var boxes = [];
      if (running.length) boxes.push(box("Running on it now", loopLines(running)));
      if (recorded.length) boxes.push(box("Their current route, but not running now", loopLines(recorded)));
      if (standby.length) boxes.push(box("On standby", byChoice(standby)));
      if (unk.length) boxes.push(box("Listed by loops whose current route could not be read", byChoice(unk)));
      if (n.more) boxes.push(el("p", "sub", n.more));
      var hide = n.readiness === "current" || n.readiness === "main" || (n.readiness === "unknown" && n.probeAgeSeconds == null);
      ul.appendChild(routeItem({
        anchor: "route-" + n.key, route: n.key, title: n.title, titleAccount: n.titleAccount, active: running.length > 0, use: n.useText,
        readiness: hide ? "" : n.readiness, phrase: n.phrase, notes: notesOf(n.notes), meta: n.meta, usage: n.usage, boxes: boxes
      }));
    });
    wrap.appendChild(ul);
    if (d.routesOmitted) wrap.appendChild(el("p", "more", d.routesOmitted + (d.routesOmitted === 1 ? " more route is" : " more routes are") + " not shown."));
    pane.appendChild(wrap);
  }

  // ---- Reading, rendering, and what a screen reader is told ----
  function renderReadline() {
    var r = $("readline");
    if (!state.lastOk) { r.textContent = state.failed ? "Could not read yet" : "Reading…"; return; }
    var every = refreshSeconds();
    r.textContent = "Read at " + fmt(secFmt, state.data.readAt) + (state.paused ? " · updates paused" : " · reads again every " + (every >= 60 ? span(every) : every + " s")) +
      (state.failed ? " · the last read failed" : "");
    r.classList.toggle("is-stale", state.failed);
  }
  // Only what changes for a person is announced: the needs-you line, and a
  // read that failed. The time of each read is not.
  function announce(text) {
    if (text === state.said) return;
    state.said = text;
    $("live").textContent = text;
  }
  // A focusable element is found again after a redraw by what it is: its
  // tag, its link target and its text, and which of those it was in order.
  var FOCUSABLE = "a[href], button, [tabindex]";
  function signature(n) { return n.tagName + "|" + (n.getAttribute("href") || "") + "|" + (n.id || "") + "|" + (n.textContent || "").trim(); }
  function focusKey(node, root) {
    if (!node || node === document.body || !root.contains(node) || !node.matches(FOCUSABLE)) return null;
    var sig = signature(node), nth = 0, all = root.querySelectorAll(FOCUSABLE);
    for (var i = 0; i < all.length && all[i] !== node; i++) if (signature(all[i]) === sig) nth++;
    return { sig: sig, nth: nth };
  }
  function findKey(key, root) {
    var all = root.querySelectorAll(FOCUSABLE), nth = 0;
    for (var i = 0; i < all.length; i++) {
      if (signature(all[i]) !== key.sig) continue;
      if (nth++ === key.nth) return all[i];
    }
    return null;
  }
  function selecting() {
    var s = window.getSelection && window.getSelection();
    return !!s && !s.isCollapsed && $("layout").contains(s.anchorNode);
  }

  // force redraws even while text is selected: hiding identifiers must not wait.
  function render(navigated, force) {
    var d = state.data;
    if (!d) return;
    // Text someone is selecting is not replaced under them; the next read,
    // or the end of the selection, brings the page up to date.
    if (!navigated && !force && selecting()) { state.pending = true; renderReadline(); return; }
    state.pending = false;
    var r = route(), pane = $("pane");
    var top = navigated ? 0 : window.scrollY;
    var active = document.activeElement, inPane = pane.contains(active), inPicker = $("picker").contains(active);
    var key = inPane ? focusKey(active, pane) : inPicker ? focusKey(active, $("picker")) : null;
    try {
      $("layout").dataset.view = r.view;
      renderPicker(d, r);
      pane.replaceChildren();
      pane.setAttribute("aria-labelledby", "pane-title");
      if (r.view === "loop") renderLoop(d, pane, r.key);
      else if (r.view === "routes") renderRoutes(d, pane);
      else renderHome(d, pane);
    } catch (err) {
      pane.replaceChildren(emptyBox("This page could not show the reply it was sent.", "Reload the page, or restart keepgoing " + served() + "."));
      banner("This page could not show the reply it was sent. Reload the page, or restart keepgoing " + served() + ".");
    }
    renderReadline();
    var needs = list(d.needsYou).length;
    document.title = (needs ? "(" + needs + ") " : "") + "keepgoing";
    announce((d.needsLine || "Needs you: unknown") + ".");
    window.scrollTo(0, top);
    if (navigated) {
      // A link to one route lands on that route; any other lands on the pane.
      var at = r.view === "routes" && r.key ? $("route-" + r.key) : null;
      if (at) { at.focus({ preventScroll: true }); at.scrollIntoView({ block: "start" }); }
      else pane.focus({ preventScroll: true });
      var cur = $("picker").querySelector('.pick-scroll [aria-current="page"]');
      if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
    } else if (key) {
      // Keep the keyboard where it was: the same link or button, rebuilt.
      var again = findKey(key, inPane ? pane : $("picker"));
      if (again) again.focus({ preventScroll: true });
    }
  }

  function banner(text) {
    var b = $("banner");
    b.hidden = !text;
    b.textContent = text || "";
  }
  function refreshSeconds() {
    var n = Number(state.data && state.data.refreshSeconds);
    return n >= 5 && n <= 3600 ? n : 15;
  }

  // Which command serves this page: "dock" until the server says otherwise.
  function served() { return state.server && state.server.servedBy === "panel" ? "panel" : "dock"; }

  function idleSpan(seconds) {
    if (seconds % 3600 === 0) return seconds === 3600 ? "1 hour" : seconds / 3600 + " hours";
    if (seconds % 60 === 0) return seconds === 60 ? "1 minute" : seconds / 60 + " minutes";
    return seconds + " seconds";
  }

  function put(node, parts) {
    parts.forEach(function (part) { node.appendChild(typeof part === "string" ? document.createTextNode(part) : part); });
  }

  // One sentence under the footer: what serves the page and when it stops.
  function renderServed() {
    var p = $("served");
    if (!p) return;
    p.textContent = "";
    if (!state.server) return;
    if (served() === "panel") {
      put(p, ["Served to this computer only by ", el("code", null, "keepgoing panel"), ", which stops by itself after " + idleSpan(state.server.idleSeconds || 1800) + " with no page reading from it. ", el("code", null, "keepgoing panel stop"), " ends it now."]);
    } else {
      put(p, ["Served to this computer only by ", el("code", null, "keepgoing dock"), ", until Ctrl-C in its terminal."]);
    }
  }

  function loadServer() {
    fetch(SERVER, { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .then(function (d) { if (d && d.schemaVersion === 1) { state.server = d; renderServed(); } })
      .catch(function () { /* the page works without it */ });
  }

  function schedule() {
    clearTimeout(state.timer);
    if (state.paused) return;
    state.timer = setTimeout(load, refreshSeconds() * 1000);
  }

  function load(byHand) {
    if (state.busy) { state.again = true; return; }
    // A hidden tab does not poll; it reads again when it is shown.
    if (document.hidden && state.data && !byHand) { schedule(); return; }
    state.busy = true;
    var btn = $("refresh");
    btn.setAttribute("aria-busy", "true");
    fetch(API + (state.identifiers ? "?identifiers=1" : ""), { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .then(function (d) {
        if (!d || d.schemaVersion !== SCHEMA || !d.registry || !Array.isArray(d.loops)) throw new Error("schema");
        state.data = d; state.lastOk = Date.now(); state.failed = false;
        banner(d.complete === false ? "This read was cut off before every loop was read. Loops that were not read are listed as could not be read; it reads again shortly." : "");
        render(false);
      })
      .catch(function (err) {
        state.failed = true;
        var schema = err && err.message === "schema";
        var cmd = "keepgoing " + served();
        var again = served() === "panel" ? "run keepgoing panel again." : "start it again and open the new address it prints.";
        var text = schema ? "This page does not understand the reply it was sent. Reload the page, or restart " + cmd + "." :
          state.data ? "The last read failed, so what you see may be out of date. " + cmd + " may have stopped; it tries again shortly." :
          "Could not read from " + cmd + ". It may have stopped; " + again;
        banner(text);
        announce(text);
        if (!state.data) {
          $("picker").replaceChildren(el("p", "placeholder", "Nothing could be read."));
          $("pane").replaceChildren(paneHead("Could not read"), emptyBox("Nothing could be read from " + cmd + ".", "It may have stopped; " + again));
        }
        renderReadline();
      })
      .then(function () {
        state.busy = false;
        btn.removeAttribute("aria-busy");
        if (state.again) { state.again = false; load(true); return; }
        schedule();
      });
  }

  document.addEventListener("DOMContentLoaded", function () {
    $("refresh").addEventListener("click", function () { clearTimeout(state.timer); load(true); });
    $("pause").addEventListener("click", function () {
      state.paused = !state.paused;
      this.setAttribute("aria-pressed", String(state.paused));
      if (state.paused) clearTimeout(state.timer); else load(true);
      renderReadline();
    });
    $("identifiers").addEventListener("click", function () {
      state.identifiers = !state.identifiers;
      this.setAttribute("aria-pressed", String(state.identifiers));
      // Hide (or keep hidden) at once from the data in hand: identifiers and
      // notes never stay on screen waiting for a read that is slow or failing.
      render(false, true);
      clearTimeout(state.timer);
      load(true);
    });
    // The skip link moves focus to the details without changing the view.
    document.querySelector("a.skip").addEventListener("click", function (ev) {
      ev.preventDefault();
      $("pane").focus();
    });
    window.addEventListener("hashchange", function () { render(true); });
    document.addEventListener("selectionchange", function () {
      if (state.pending && !selecting()) render(false);
    });
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && !state.paused && state.lastOk && Date.now() - state.lastOk > 5000) { clearTimeout(state.timer); load(true); }
    });
    loadServer();
    load(true);
  });
})();
