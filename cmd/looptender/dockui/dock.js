"use strict";
// keepgoing panel page. Reads bounded versioned JSON relative to this
// page and renders it with DOM text nodes only; no data is parsed as HTML.
// Ages come from the report's read time, never this browser's clock, so the
// page reads the same wherever it is opened. Every sentence about a loop or a
// route is sent by the report, which the Go tests check word for word; the
// page only shows its times in this browser's time zone.
(function () {
  // The header buttons and the reading lines are shown only once this runs;
  // without a script the page says so instead (index.html, noscript).
  document.documentElement.classList.add("has-script");
  var SCHEMA = 1;
  // How long reads may fail before the page asks a person to look (G4).
  var FAIL_LONG_MS = 120000;
  var API = "api/v1/dock";
  var SERVER = "api/v1/server";
  var SAVED_ACCOUNTS = "api/v1/saved-accounts";
  var savedAccounts = { reference: "", status: "", data: null, serial: 0, abort: null };
  // The end of the route brief that says a missing standby does not touch the
  // route in use; the line is then a plain notice, not a warning. Kept in step
  // with dockCurrentUnaffected in dock_view.go (a test checks).
  var CURRENT_UNAFFECTED = "its current route is unaffected";
  // What looptender did: worded by the same read as API, served on its own
  // for its size, and read only for the Usage page and a loop's page.
  var USAGE = "api/v1/usage";
  var state = { data: null, identifiers: false, paused: false, timer: 0, busy: false, again: false, missed: false, lastOk: null, failed: false, failedSince: 0, failedLong: false, notice: "", pending: false, said: "", server: null, treeOpen: Object.create(null), accountOpen: Object.create(null), usage: null, usageText: "", usageFailed: false, usageBusy: false };

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
  // route and "#/routes/<key>" every route, at that one; "#/usage" is what
  // looptender did.
  function route() {
    var h = location.hash.replace(/^#\/?/, "");
    var m = /^routes(?:\/(r[0-9a-f]+))?$/.exec(h);
    if (m) return { view: "routes", key: m[1] || "" };
    if (h === "accounts") return { view: "accounts" };
    if (h === "versions") return { view: "versions" };
    if (h === "usage") return { view: "usage" };
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
    // The age is said in plain words, with no badge: a loop that is on and
    // not checked for longer than it should be has its own state, "Not
    // checked recently"; for a paused or held loop an old check is expected.
    f.appendChild(document.createTextNode(c.text));
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
      if (l.reached && l.effect) f.appendChild(el("div", "sub", "Reached: " + l.effect));
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
      m.setAttribute("aria-label", w.name + " window " + (w.stale ? "has ended since this reading" : w.usedPercent + " percent used"));
      var bar = el("i");
      bar.style.width = (w.stale ? 0 : Math.max(2, w.usedPercent)) + "%";
      m.appendChild(bar);
      row.appendChild(m);
      row.appendChild(el("span", "pct", w.stale ? "—" : w.usedPercent + "%"));
      if (list(w.reset).length) row.appendChild(el("span", "meter-reset", say(w.reset)));
      if (list(w.projection).length) row.appendChild(el("span", "meter-projection", say(w.projection)));
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
    // Age is said in the read line and is not a warning. Availability
    // "stale" is that every window it describes has ended since it was
    // taken: a neutral label. Only an old reading that was near its limit
    // (needsPerson) gets one amber line, as only then may its age change a
    // decision.
    if (u.availability === "stale") uh.appendChild(tag("calm", "Windows ended"));
    box.appendChild(uh);
    if (u.needsPerson && u.ageNote) box.appendChild(el("p", "route-note is-attn usage-age", u.ageNote));
    box.appendChild(meters(u));
    if (u.readingsNote) box.appendChild(el("p", "sub usage-note", u.readingsNote));
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

  // A hold's note is always shown. Without identifiers, parts shaped like an
  // identifier read "[hidden]" and the card offers to show them.
  function showNotesButton() {
    var b = el("button", "linkish", "Show identifiers");
    b.setAttribute("data-focus", "show-notes");
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
    // Data read with identifiers carries the masked note too, so turning
    // identifiers off hides them at once.
    var text = !state.identifiers && n.maskedNote ? n.maskedNote : n.note;
    if (text) {
      var note = el("div", "note");
      note.appendChild(el("span", "note-label", "Note left with the hold"));
      note.appendChild(document.createTextNode("“" + text + "”"));
      li.appendChild(note);
      if (n.noteMasked && !state.identifiers) {
        var p = el("p", "sub", "Parts that look like an ID, path, web address, email address, network address or long code are hidden. ");
        p.appendChild(showNotesButton());
        li.appendChild(p);
      }
    }
    var todo = el("p", "todo");
    todo.appendChild(el("span", "todo-label", "What to do"));
    todo.appendChild(document.createTextNode(n.action));
    li.appendChild(todo);
    // A page that cannot resume says how to, rather than end on an action
    // it cannot take. With changes allowed, the loop's own controls do it.
    if (n.resume && !changesOn()) {
      li.appendChild(el("p", "sub resume-where", "This page cannot resume a loop. In a terminal, run:"));
      // The page may hold several copy buttons; this one names its loop.
      var cmd = copyCommand("p", n.resume, "copy-resume-" + n.key, "resume-where-" + n.key);
      cmd.classList.add("resume-command");
      li.appendChild(cmd);
      var where = el("p", "sub resume-where");
      where.id = "resume-where-" + n.key;
      put(where, ["CONFIG is the path of " + n.loop + "'s configuration file; ", el("code", null, "keepgoing registry list --private"), " lists it."]);
      li.appendChild(where);
    }
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
    // The full text stays in the link, so it is the accessible name and what
    // find and copy see; the ellipsis is only how it is drawn. A cut name is
    // read in full by focusing or hovering the row (see dock.css), and by
    // opening it: the page heading shows it whole.
    var n = el("span", "pick-name");
    n.appendChild(el("span", null, name));
    a.appendChild(n);
    var s = el("span", "pick-state");
    s.appendChild(el("span", null, sub));
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
    var ac = d.accounts || {};
    fixed.appendChild(pickRow("#/accounts", "dot-calm", "Accounts", ac.complete ? (d.accountsReference ? d.selectedAccounts || 0 : list(ac.cards).length) + " selected" : "Unavailable", r.view === "accounts"));
    var vs = d.versions || {};
    fixed.appendChild(pickRow("#/versions", vs.behind ? "dot-limit" : "dot-calm", "Versions", vs.summary || "", r.view === "versions"));
    fixed.appendChild(pickRow("#/usage", "dot-calm", "Usage", d.usagePicker || "", r.view === "usage"));
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
    // Focus lands here after a change of view or the skip link.
    h.tabIndex = -1;
    head.appendChild(h);
    if (pill) head.appendChild(pill);
    return head;
  }
  // The view's heading takes focus; the pane only when there is none.
  function focusPaneTitle(scroll) {
    ($("pane-title") || $("pane")).focus({ preventScroll: !scroll });
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
  // Pause all and stop all: each offered while any loop can have it; the
  // server reviews every loop and words the review, its options and the
  // outcome.
  function groupBox(d) {
    var can = ["pause", "stop"].filter(function (a) {
      return list(d.loops).some(function (l) { return list(l.can).indexOf(a) >= 0; });
    });
    return changeBox({ key: "all", can: can }, true);
  }
  function renderHome(d, pane) {
    pane.appendChild(paneHead("Overview"));
    var group = groupBox(d);
    if (group) pane.appendChild(group);
    if (d.registry.availability !== "available") {
      pane.appendChild(el("p", "lede", "Unknown."));
      pane.appendChild(emptyBox("The registry could not be read.", "Nothing is shown rather than guessing. Check it with keepgoing registry list."));
      return;
    }
    var needs = list(d.needsYou);
    if (d.lede) pane.appendChild(el("p", "lede", d.lede));
    if (!list(d.loops).length) {
      pane.appendChild(emptyBox("No loops are registered yet.", "Register one with keepgoing registry add -c path/to/looptender.yaml (or keepgoing.yaml), and it appears here on the next read."));
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
      pane.appendChild(el("h2", "overview-heading", "Needs you"));
      var ol = el("ol", "need-list");
      needs.forEach(function (x) { ol.appendChild(needCard(x, true)); });
      var wrap = el("div", "panel is-need");
      wrap.appendChild(ol);
      pane.appendChild(wrap);
    }
    pane.appendChild(workTree(d));
    pane.appendChild(recentAcrossLoops(d));
    // Loops that do not need a person but are not routine either: waiting on
    // something, not checked, or on a later route.
    var worth = list(d.worth);
    if (worth.length) {
      var sec = panel("Worth knowing", [el("span", "sub", "not waiting on you")]);
      var ul = el("ul", "worth");
      worth.forEach(function (w) {
        var li = el("li");
        // A failed lane is said in words; its dot is not the green of a
        // loop with nothing to say.
        var wl = loopByKey(w.key), tone = TONES[w.state] || "unknown";
        if (wl && wl.lanesFailed && tone === "good") tone = "warn";
        li.appendChild(el("span", "dot dot-" + tone));
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

  // The existing feed verifies direct lanes at a check, not an entire live
  // descendant tree. Keep each registered loop visible independently, even
  // when a lane links to it: its own control state may differ from the
  // parent's observation. No relationship is inferred from names or order.
  function workTree(d) {
    var p = panel("Loops and lanes");
    var ul = el("ul", "work-tree");
    var retainedOpen = Object.create(null);
    list(d.loops).forEach(function (l) {
      var li = el("li", "work-branch"), details = el("details");
      details.dataset.loop = l.key;
      details.open = Object.prototype.hasOwnProperty.call(state.treeOpen, l.key) ? state.treeOpen[l.key] : !!needFor(l.key);
      retainedOpen[l.key] = details.open;
      var summary = el("summary", "work-summary");
      summary.setAttribute("data-focus", "tree-" + l.key);
      var name = el("span", "work-name", l.name);
      // The glossary's role: its last verified listing has lanes.
      if (l.lanes && l.lanes.role === "coordinator") name.appendChild(el("span", "work-role", "coordinator"));
      summary.appendChild(name);
      var status = el("span", "work-status");
      status.appendChild(el("span", "dot dot-" + (TONES[l.state] || "unknown")));
      status.appendChild(el("span", null, "Supervision: " + l.phrase));
      if (l.state === "finished") status.appendChild(tag("stale", "Unverified declaration"));
      if (l.lanesFailed) status.appendChild(tag("needs", l.lanesFailed));
      summary.appendChild(status);
      summary.appendChild(el("span", "sub work-observation", l.lanes && l.lanes.summary ? "Lanes: " + l.lanes.summary : "Lane evidence: " + ((l.children && l.children.phrase) || "Unknown")));
      // Age in plain words, as for usage readings; see lastCheck().
      summary.appendChild(el("span", "sub work-age", (l.lastCheck && l.lastCheck.text) || "Last check unknown"));
      var mr = l.migrationReview;
      if (mr && mr.availability !== "not_recorded" && (!mr.counts || mr.counts.unfinished > 0)) {
        summary.appendChild(el("span", "sub work-migration", "Migration review: " + mr.text + (mr.observedAt ? " · historical evidence from " + fmt(dayTimeYearFmt, mr.observedAt) : "")));
      }
      details.appendChild(summary);
      var body = el("div", "work-body");
      body.appendChild(el("p", "detail", l.detail));
      var facts = el("dl", "work-facts");
      facts.appendChild(fact("Last decision", l.decision ? say(l.decision.phrase) : "Unknown"));
      facts.appendChild(fact("Next check", nextCheck(l.nextCheck || { availability: "unknown" })));
      body.appendChild(facts);
      body.appendChild(laneBox(l, true));
      var more = body.querySelector("details.lane-more"), moreKey = l.key + "/lanes";
      if (more) {
        more.open = !!state.treeOpen[moreKey];
        retainedOpen[moreKey] = more.open;
        more.addEventListener("toggle", function () {
          if (more.isConnected) state.treeOpen[moreKey] = more.open;
        });
      }
      body.appendChild(link("#/loop/" + l.key, "Inspect " + l.name, "work-inspect"));
      details.appendChild(body);
      details.addEventListener("toggle", function () {
        if (details.isConnected) state.treeOpen[l.key] = details.open;
      });
      li.appendChild(details);
      ul.appendChild(li);
    });
    state.treeOpen = retainedOpen;
    p.appendChild(ul);
    p.appendChild(el("p", "sub work-evidence", "Each loop and its own lanes, as of its last check."));
    if (!d.complete || d.registry.omitted) p.appendChild(el("p", "more", "This read does not include every registered loop. Missing loops are not assumed stopped or clear."));
    return p;
  }

  // Merge only the bounded, already-projected events. This is a recent
  // sample, not a complete chronology or a persisted "while away" cursor.
  function recentAcrossLoops(d) {
    var p = panel("Recent activity across loops");
    var intro = el("p", "sub", "The newest few events from each loop.");
    p.appendChild(intro);
    var rows = [], missing = false, pruned = false;
    list(d.loops).forEach(function (l) {
      if (!l.events || l.events.availability === "unknown") missing = true;
      if (l.events && l.events.pruned) pruned = true;
      list(l.events && l.events.events).forEach(function (e) {
        var at = Date.parse(e.at);
        if (!isNaN(at)) rows.push({ loop: l, event: e, at: at });
      });
    });
    rows.sort(function (a, b) { return b.at - a.at || a.loop.key.localeCompare(b.loop.key); });
    var ol = el("ol", "events overview-events");
    rows.slice(0, 8).forEach(function (r) {
      var x = r.event, li = el("li", "event is-" + x.tone);
      var t = el("time", null, say(x.when));
      t.dateTime = x.at;
      li.appendChild(t);
      li.appendChild(el("span", "mark"));
      var body = el("div");
      body.appendChild(link("#/loop/" + r.loop.key, r.loop.name));
      body.appendChild(el("div", null, x.phrase));
      if (x.detail) body.appendChild(el("span", "sub", x.detail));
      if (list(x.foldedText).length) body.appendChild(el("span", "sub", say(x.foldedText)));
      li.appendChild(body);
      ol.appendChild(li);
    });
    if (rows.length) p.appendChild(ol);
    else p.appendChild(el("p", "sub", missing ? "Recent activity is unavailable for one or more loops." : "No recent activity is recorded in this read."));
    if (rows.length > 8) p.appendChild(el("p", "more", "Showing the newest 8 of " + rows.length + ". Open a loop for more."));
    if (pruned) intro.textContent = "The newest few events from each loop; older ones are no longer kept.";
    if ((rows.length && missing) || !d.complete || d.registry.omitted) p.appendChild(el("p", "more", "Some loops or their history could not be read, so this list is missing events."));
    return p;
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
    // The no-progress flag informs; it is never a need.
    if (l.noProgress && l.noProgress.availability === "flagged") card("Progress", "No progress", l.noProgress.text.replace(/^No progress: /, ""));
    return dl;
  }

  // Each lane of the last verified listing: its state, then what it is (its
  // title when one is recorded, otherwise its number).
  function laneBox(l, overview) {
    var c = l.children, lanes = l.lanes, rows = list(lanes && lanes.lanes);
    var b = el("div", "lanes is-wide");
    b.appendChild(el("span", "lanes-label", (lanes && lanes.heading) || "This loop's lanes"));
    if (!rows.length) {
      // An unknown answer carries its reason in the phrase: "Unknown: …".
      b.appendChild(!c || !c.phrase ? unknown() : c.availability === "unknown" ? unknown(c.phrase) : el("span", "sub", c.phrase));
      return b;
    }
    function row(x) {
      var li = el("li", "lane");
      li.appendChild(el("span", "dot " + (x.state === "working" ? "dot-good" : x.state === "waiting" ? "dot-attn" : x.state === "failed" ? "dot-warn" : "dot-unknown")));
      li.appendChild(el("span", "lane-state", x.phrase));
      var what = el("span", "lane-what");
      what.appendChild(el("span", "lane-title", x.title || "Lane " + x.number));
      // A recorded title is the person's own text: shown with identifiers only.
      if (!x.title && x.titleEvidence === "withheld") what.appendChild(el("span", "sub", " · title withheld"));
      if (x.loop) {
        what.appendChild(document.createTextNode(" · also a registered loop: "));
        what.appendChild(link("#/loop/" + x.loopKey, x.loop));
      }
      if (x.key && state.identifiers) {
        what.appendChild(document.createTextNode(" · "));
        what.appendChild(el("code", null, x.key));
      }
      li.appendChild(what);
      return li;
    }
    function rowList(xs) {
      var ul = el("ul", "lane-list");
      xs.forEach(function (x) { ul.appendChild(row(x)); });
      return ul;
    }
    // On the overview, a long listing shows the lanes waiting for an answer
    // and folds the rest into one details element.
    var shown = rows, rest = [];
    if (overview && rows.length > 8) {
      shown = rows.filter(function (x) { return x.state === "waiting"; });
      rest = rows.filter(function (x) { return x.state !== "waiting"; });
    }
    if (shown.length) b.appendChild(rowList(shown));
    if (rest.length) {
      var fold = el("details", "lane-more");
      fold.appendChild(el("summary", null, (shown.length ? "Show the other " + rest.length + " of " : "Show all ") + rows.length + (lanes.omitted ? " listed" : "") + " lanes"));
      fold.appendChild(rowList(rest));
      b.appendChild(fold);
    }
    if (overview) {
      if (lanes.omitted) b.appendChild(el("p", "more", lanes.omitted + " more lanes are not listed."));
      b.appendChild(el("p", "more", "Lane titles appear only with identifiers. Routes for individual lanes are not recorded."));
    } else if (lanes && lanes.footer) b.appendChild(el("p", "more", lanes.footer));
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
    if (ev.footer) p.appendChild(el("p", "more", ev.footer));
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
    var pills = st;
    // A failed lane is said beside the supervision state, in words.
    if (l.lanesFailed) {
      pills = el("span", "pane-pills");
      pills.appendChild(st);
      pills.appendChild(tag("needs", l.lanesFailed));
    }
    pane.appendChild(paneHead(l.name, pills));
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
    var change = changeBox(l);
    if (change) pane.appendChild(change);
    pane.appendChild(migrationReviewPanel(l.migrationReview));
    pane.appendChild(assignmentPanel(l.assignments));

    // The ordered preferred routes are the main structure.
    var c = l.chain, entries = list(c.entries);
    var brief = !c.brief ? null : c.availability !== "none_configured" && c.brief.indexOf("unknown") >= 0 ? unknown(c.brief) :
      el("span", c.availability === "none_configured" ? "sub" : "chain-ready" + (c.brief.indexOf(CURRENT_UNAFFECTED) >= 0 ? " is-calm" : c.short ? " is-short" : ""), c.brief);
    var routing = panel("Preferred routes", [brief]);
    routing.appendChild(el("p", "sub chain-how", c.how || "Unknown."));
    var ol = el("ol", "route-list is-ordered");
    entries.forEach(function (e) {
      var bound = boundAccount(e.catalogAccount);
      var item = routeItem({
        route: e.route, position: e.position, badge: String(e.position + 1), title: e.title, titleAccount: bound || e.titleAccount, active: e.inUse, use: e.use, off: e.disabled,
        readiness: e.position > 0 && e.readiness !== "current" ? e.readiness : "", phrase: e.phrase, notes: notesOf(e.notes),
        after: list(e.after).concat(e.catalogAccount && e.catalogAccount.assigned === "none" ? ["Not assigned to an account."] : []),
        meta: e.meta, named: e.named, usage: e.usage, boxes: e.current ? [laneBox(l)] : []
      });
      item.setAttribute("aria-label", "Choice " + (e.position + 1) + ": " + e.title + (e.use ? ", " + e.use : ""));
      ol.appendChild(item);
    });
    routing.appendChild(ol);
    if (c.more) routing.appendChild(el("p", "sub", c.more));
    if (!entries.some(function (e) { return e.current; })) routing.appendChild(laneBox(l));
    // Beside the order: how to add a route and change the order, to copy.
    if (c.availability !== "unknown") {
      var how = el("div", "how-group");
      how.appendChild(addRouteBox(l));
      var order = reorderBox(l);
      if (order) how.appendChild(order);
      routing.appendChild(how);
    }
    pane.appendChild(routing);

    pane.appendChild(eventsPanel(l.events));

    var checks = panel("Checks and limits");
    var dl = el("dl", "facts"), dev = l.device || {};
    dl.appendChild(fact("Last check", lastCheck(l.lastCheck)));
    dl.appendChild(fact("Next check", nextCheck(l.nextCheck)));
    dl.appendChild(fact("Limits", limits(l.limits, l.limitWait)));
    if (l.build && l.build.text) dl.appendChild(fact("Build", l.build.availability === "unknown" || l.build.availability === "not_recorded" ? unknown(l.build.text) : el("span", null, l.build.text)));
    if (l.buildChange && l.buildChange.text) dl.appendChild(fact("Its file", el("span", l.buildChange.pending ? "c-warn" : null, l.buildChange.text)));
    if (l.noProgress) {
      var np = l.noProgress, pf = frag();
      pf.appendChild(np.availability === "not_available" ? unknown(np.text) : el("span", null, np.text));
      list(np.lines).forEach(function (s) { pf.appendChild(el("div", "sub", s)); });
      dl.appendChild(fact("Progress", pf));
    }
    if (l.refusals && (l.refusals.text || l.refusals.question)) {
      var rf = frag();
      if (l.refusals.text) rf.appendChild(el("span", null, l.refusals.text));
      if (l.refusals.question) rf.appendChild(el("div", "sub", "Question: " + l.refusals.question));
      dl.appendChild(fact("Refusals", rf));
    }
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
        var open = threadCommand(l.identifiers.thread);
        if (open) {
          ids.appendChild(el("dt", null, "Open it in BB"));
          ids.appendChild(open);
        }
      }
      checks.appendChild(ids);
    }
    pane.appendChild(checks);
    pane.appendChild(usageBrief(key));
  }

  function assignmentPanel(review) {
    var p = panel("Assignment handoffs");
    p.classList.add("assignment-review");
    p.appendChild(el("p", null, review ? review.text : "Assignment evidence unavailable. Outcomes are unknown."));
    if (review && review.availability === "available" && review.counts) {
      var c = review.counts, dl = el("dl", "facts");
      dl.appendChild(fact("Evidence read", fmt(dayTimeYearFmt, review.observedAt)));
      dl.appendChild(fact("Review recorded", fmt(dayTimeYearFmt, review.recordedAt)));
      dl.appendChild(fact("Coverage", review.coverage === "observed_direct_children" ? "Observed direct children" : "Partial — coverage unknown"));
      dl.appendChild(fact("Unknown / unacknowledged", c.unknown + " / " + c.unacknowledged));
      dl.appendChild(fact("Unfinished assignments", String(c.unfinished)));
      dl.appendChild(fact("Person-blocked / dependency-waiting", c.personBlocked + " / " + c.dependencyWaiting));
      p.appendChild(dl);
    }
    p.appendChild(el("p", "sub", "Dated direct-lane evidence; descendants are excluded. BB running or idle is separate from declared outcome and availability. Missing handoffs need explicit coordinator reconciliation. This view does not dispatch work, grant permission or clear holds."));
    return p;
  }

  function migrationReviewPanel(review) {
    var p = panel("Migration review");
    p.classList.add("migration-review");
    if (!review) {
      p.appendChild(el("p", null, "Migration review unavailable in this read. Unfinished work is unknown."));
      return p;
    }
    p.appendChild(el("p", null, review.text));
    if (review.availability === "available" && review.counts) {
      var c = review.counts, dl = el("dl", "facts");
      dl.appendChild(fact("Evidence read", fmt(dayTimeYearFmt, review.observedAt)));
      dl.appendChild(fact("Declared assignments", String(c.assignments)));
      dl.appendChild(fact("Dormant / unresolved", c.dormant + " / " + c.unresolved));
      dl.appendChild(fact("Observed direct lanes", String(c.directLanes)));
      dl.appendChild(fact("Assignments absent from listing", String(c.absentAssignments)));
      p.appendChild(dl);
      p.appendChild(el("p", "sub", "Historical checklist of predecessor direct lanes only; descendants are excluded. Declared assignment state is separate from observed activity. This does not show which lanes are working now."));
      p.appendChild(el("p", "sub", "Review metadata does not establish migration execution, completion or permission. Dormant work may be intentional. Nothing is resumed or dispatched from this view."));
    }
    return p;
  }

  // ---- Usage: what looptender did, in counted sentences ----
  // The server words every sentence from the loop's recorded figures; the
  // page only lays them out. Nothing here is a warning.
  function usageFor(key) { return state.usage ? list(state.usage.loops).filter(function (x) { return x.key === key; })[0] : null; }
  function usagePending() {
    return state.usageFailed ? "What looptender did could not be read. It is read again with the next update." : "Reading what looptender did…";
  }
  function sentences(items, keys) {
    var ul = el("ul", "usage-lines");
    list(items).forEach(function (s) {
      var li = el("li");
      // A configuration key in the sentence is set as code.
      if (keys) String(s).split(/\b([a-z]+(?:_[a-z]+)+(?:\.[a-z_]+)?)\b/).forEach(function (part, i) { li.appendChild(i % 2 ? el("code", null, part) : document.createTextNode(part)); });
      else li.textContent = s;
      ul.appendChild(li);
    });
    return ul;
  }
  function usageBrief(key) {
    var p = panel("What looptender did this week");
    p.classList.add("usage-brief");
    var x = usageFor(key);
    if (x) p.appendChild(sentences(x.brief));
    else p.appendChild(el("p", "sub", state.usage ? "Not counted yet. It is read again with the next update." : usagePending()));
    p.appendChild(link("#/usage", "Every count, on the Usage page", "work-inspect"));
    return p;
  }
  var DAY_HEADS = ["Day (UTC)", "Lowest", "Highest", "Last", "Window", "Source"];
  function daysTable(x) {
    var id = "usage-days-" + x.key;
    var wrap = el("div", "table-wrap");
    // Keyboard users can scroll it when it is wider than the page.
    wrap.tabIndex = 0;
    wrap.setAttribute("role", "region");
    wrap.setAttribute("aria-labelledby", id);
    var t = el("table", "usage-days");
    var cap = el("caption", null, "Context readings by day, in tokens");
    cap.id = id;
    t.appendChild(cap);
    var head = el("thead"), hr = el("tr");
    DAY_HEADS.forEach(function (h) { var th = el("th", null, h); th.scope = "col"; hr.appendChild(th); });
    head.appendChild(hr);
    t.appendChild(head);
    var body = el("tbody");
    list(x.days).forEach(function (day) {
      var tr = el("tr");
      var th = el("th", null, day.day);
      th.scope = "row";
      tr.appendChild(th);
      [day.lowest, day.highest, day.last, day.window].forEach(function (v) { tr.appendChild(el("td", "num", v)); });
      tr.appendChild(el("td", null, day.estimated ? "estimated by BB" : "reported by BB"));
      body.appendChild(tr);
    });
    t.appendChild(body);
    wrap.appendChild(t);
    return wrap;
  }
  function renderUsage(d, pane) {
    pane.appendChild(back());
    pane.appendChild(paneHead("Usage"));
    var u = state.usage;
    if (!u) {
      pane.appendChild(emptyBox(usagePending(), ""));
      return;
    }
    pane.appendChild(el("p", "lede", u.lede));
    pane.appendChild(el("p", "detail", u.period));
    var all = panel("Across loops");
    all.appendChild(sentences(u.total));
    all.appendChild(el("p", "usage-how", u.mechanism));
    pane.appendChild(all);
    var ctx = panel("What looptender does about context");
    ctx.appendChild(sentences(u.context, true));
    ctx.appendChild(el("p", "usage-how", u.chat));
    pane.appendChild(ctx);
    list(u.loops).forEach(function (x) {
      var p = el("section", "panel usage-loop");
      var h = el("h2");
      h.appendChild(link("#/loop/" + x.key, x.name));
      p.appendChild(h);
      p.appendChild(sentences(x.did));
      p.appendChild(el("h3", "usage-sub", "Context window"));
      p.appendChild(sentences(x.window));
      if (list(x.days).length) p.appendChild(daysTable(x));
      pane.appendChild(p);
    });
  }
  function wantsUsage(r) { return r.view === "usage" || r.view === "loop"; }
  // Read on its own after the report, at low priority on a loop's page
  // where it is not the main content. A failure leaves the last answer.
  // A read that does not answer within usageTimeout() is abandoned and
  // counts as failed, so a stalled request never stops later reads. The
  // page is drawn again only when the answer (apart from its readAt) or
  // the failed state changed, so an unchanged answer does not redraw a
  // loop's page a second time after each read.
  function usageTimeout() { return Math.min(20000, Math.max(3000, refreshSeconds() * 1000 - 2000)); }
  function loadUsage() {
    if (state.usageBusy) return;
    state.usageBusy = true;
    var abort = new AbortController();
    var timer = setTimeout(function () { abort.abort(); }, usageTimeout());
    var wasText = state.usageText, wasFailed = state.usageFailed;
    fetch(USAGE, { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" }, priority: route().view === "usage" ? "auto" : "low", signal: abort.signal })
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.text(); })
      .then(function (text) {
        var u = JSON.parse(text);
        if (!u || u.schemaVersion !== SCHEMA || !Array.isArray(u.loops)) throw new Error("schema");
        // Every read has its own readAt, which the page does not show; the
        // rest of the answer is what is compared.
        var same = JSON.stringify(Object.assign({}, u, { readAt: null }));
        if (same !== state.usageText) { state.usage = u; state.usageText = same; }
        state.usageFailed = false;
      })
      .catch(function () { state.usageFailed = true; })
      .then(function () {
        clearTimeout(timer);
        state.usageBusy = false;
        var changed = state.usageText !== wasText || state.usageFailed !== wasFailed;
        if (changed && state.data && wantsUsage(route())) render(false);
      });
  }

  // The command that opens a loop's BB thread, to copy and run yourself.
  // Nothing is sent or run from the page; BB documents no link format, so
  // there is no link. The command stays visible and selectable, and the
  // button appears only where the browser can copy.
  var THREAD_ID = /^thr_[A-Za-z0-9_]+$/;
  function threadCommand(id) {
    if (typeof id !== "string" || !THREAD_ID.test(id)) return null;
    return copyCommand("dd", "bb thread open " + id, "copy-thread-command");
  }
  // A command to copy and run yourself, with a copy button where the
  // browser can copy. The page runs nothing.
  // describedBy names what the command is for when a page has several.
  function copyCommand(tagName, text, focus, describedBy) {
    var dd = el(tagName, "copy");
    dd.appendChild(el("code", "command", text));
    if (navigator.clipboard && navigator.clipboard.writeText) {
      var b = el("button", "btn", "Copy the command");
      b.type = "button";
      b.dataset.focus = focus;
      if (describedBy) b.setAttribute("aria-describedby", describedBy);
      b.addEventListener("click", function () {
        // Cleared first, so a second press is announced again.
        state.said = "";
        $("live").textContent = "";
        navigator.clipboard.writeText(text).then(function () {
          announce("Copied. Paste it in a terminal.");
        }, function () {
          announce("Could not copy. Select the command instead.");
        });
      });
      dd.appendChild(b);
    }
    return dd;
  }

  // ---- Changes: pause, stop and resume, on a panel started with --allow-changes ----
  // Every sentence comes from the server. Each change is prepared, shown,
  // and only then confirmed; nothing is retried by itself.
  var ACTION_LABEL = { pause: "Pause", stop: "Stop", resume: "Resume" };
  function changesOn() { return !!(state.server && state.server.changes === "on" && state.server.form); }
  function post(path, form, body) {
    return fetch(path, {
      method: "POST", cache: "no-store", credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "application/json", "X-Keepgoing-Form": form }, body: JSON.stringify(body)
    })
      .then(function (r) {
        if (!r.ok) { var e = new Error("status " + r.status); e.status = r.status; throw e; }
        return r.json();
      })
      .then(function (d) { if (!d || d.schemaVersion !== 1) throw new Error("schema"); return d; });
  }
  function focusOn(name) {
    var n = document.querySelector('[data-focus="' + name + '"]');
    if (n) n.focus();
  }
  function prepareChange(key, action) {
    if (!changesOn()) return;
    state.change = { key: key, action: action, busy: true };
    render(false, true);
    post("api/v1/loops/" + key + "/prepare", state.server.form, { action: action })
      .then(function (d) { state.change = d.preview ? { key: key, action: action, preview: d.preview } : { key: key, action: action, result: d.result }; })
      .catch(function () {
        state.change = { key: key, action: action, result: { outcome: "refused", retry: true, text: "The review could not be prepared: the panel did not answer. Nothing was changed." } };
      })
      .then(function () {
        render(false, true);
        if (state.change.preview) focusOn("change-cancel");
        else { focusOn("change-result"); announce(state.change.result.text); }
      });
  }
  // A stop's review may offer options (stop the lanes too, carry it out at
  // once). Each is one button, in the server's words; the body says which.
  function confirmChange(option) {
    var c = state.change, p = c && c.preview;
    if (!p || c.busy) return;
    state.change = { key: c.key, action: c.action, preview: p, busy: true };
    render(false, true);
    var body = { action: p.action };
    if (option && p.stop) {
      if (option.lanes) body.lanes = true;
      if (option.lanes && option.dropQueued) body.dropQueued = true;
      if (p.stop.immediately) body.immediately = true;
    }
    post("api/v1/loops/" + c.key + "/confirm", p.form, body)
      .then(function (d) { state.change = { key: c.key, action: c.action, result: d.result }; })
      // A refusal (4xx) comes before anything is done. Anything else, or no
      // reply: whether it was saved is not known, and it is not sent again.
      .catch(function (err) {
        var refused = err && err.status >= 400 && err.status < 500;
        state.change = { key: c.key, action: c.action, result: refused ? { outcome: "refused", retry: true, text: p.refused } : { outcome: "uncertain", text: p.lost } };
      })
      .then(function () {
        render(false, true);
        focusOn("change-result");
        announce(state.change.result.text);
        clearTimeout(state.timer);
        load(true);
      });
  }
  function cancelChange() {
    var c = state.change;
    state.change = null;
    render(false, true);
    if (c) focusOn("act-" + c.action);
  }
  function button(label, focus, cls, onClick, disabled) {
    var b = el("button", "btn" + (cls ? " " + cls : ""), label);
    b.type = "button";
    b.setAttribute("data-focus", focus);
    if (disabled) b.disabled = true;
    b.addEventListener("click", onClick);
    return b;
  }
  // all: the change to every loop at once (key "all"), on the overview.
  function changeBox(l, all) {
    var c = state.change && state.change.key === l.key ? state.change : null;
    if (!changesOn() && !c) return null;
    var box = el("section", "panel change");
    if (c && c.preview) {
      var p = c.preview;
      box.classList.add("is-review");
      box.setAttribute("aria-labelledby", "change-title");
      var h = el("h2", null, p.title);
      h.id = "change-title";
      box.appendChild(h);
      list(p.lines).forEach(function (line) { box.appendChild(el("p", "change-line", say(line))); });
      var row = el("div", "change-buttons");
      row.appendChild(button("Cancel", "change-cancel", "", cancelChange, c.busy));
      var options = p.stop ? list(p.stop.options) : [];
      if (options.length) {
        options.forEach(function (o, i) {
          row.appendChild(button(c.busy ? "Saving…" : o.label, i ? "change-confirm-" + i : "change-confirm", i === options.length - 1 ? "is-primary" : "", function () { confirmChange(o); }, c.busy));
        });
      } else {
        row.appendChild(button(c.busy ? "Saving…" : p.confirm, "change-confirm", "is-primary", function () { confirmChange(null); }, c.busy));
      }
      box.appendChild(row);
      return box;
    }
    if (c && c.result) {
      var r = el("p", "change-result is-" + c.result.outcome, c.result.text);
      r.tabIndex = -1;
      r.setAttribute("data-focus", "change-result");
      box.appendChild(r);
      // A change to every loop: each loop's outcome, those not changed first.
      if (list(c.result.lines).length) {
        var ul = el("ul", "change-lines");
        list(c.result.lines).forEach(function (line) { ul.appendChild(el("li", "change-line", line)); });
        box.appendChild(ul);
      }
      var again = el("div", "change-buttons");
      if (c.result.retry && changesOn()) again.appendChild(button("Review again", "change-retry", "", function () { prepareChange(l.key, c.action); }));
      again.appendChild(button("Close", "change-close", "", function () { state.change = null; render(false, true); focusOn("act-" + c.action); }));
      box.appendChild(again);
      if (!changesOn()) return box;
    }
    if (c && c.busy) {
      box.appendChild(el("p", "change-line", "Preparing the review…"));
      return box;
    }
    var can = list(l.can);
    // The build change, for one loop only, labelled by the server.
    var bc = !all && l.buildChange && l.buildChange.action ? l.buildChange : null;
    if (!can.length && !bc) return c ? box : null;
    var actions = el("div", "change-buttons");
    actions.setAttribute("role", "group");
    actions.setAttribute("aria-label", all ? "Change every loop" : "Change this loop");
    can.forEach(function (a) {
      if (ACTION_LABEL[a]) actions.appendChild(button(ACTION_LABEL[a] + (all ? " all…" : "…"), "act-" + a, "", function () { prepareChange(l.key, a); }));
    });
    if (bc) actions.appendChild(button(bc.label + "…", "act-" + bc.action, "", function () { prepareChange(l.key, bc.action); }));
    box.appendChild(actions);
    return box;
  }

  // ---- Routes across loops ----
  // Only consistent explicit catalog assignments combine headings. Each
  // route retains its own reading and windows; other routes remain separate
  // under their configured group, with no inferred account identity.
  function allowancesPanel(d) {
    var items = list(d.allowances);
    var p = panel("Allowances", [el("span", "sub", "route readings; grouped by your assignments · not live")]);
    if (!items.length) { p.appendChild(el("p", "sub chain-how", "No route readings to show.")); return p; }
    var ul = el("ul", "allow-list");
    items.forEach(function (a) {
      var li = el("li", "allow" + (a.assigned ? " is-account" : ""));
      var head = el("div", "allow-head");
      head.appendChild(el("h3", "allow-name", a.title));
      var lines = list(a.lines);
      // Said once per block: for BB threads in its own words when every
      // route is one, else as for a headless route.
      var bbOnly = lines.length > 0 && lines.every(function (r) { return r.bbThread; });
      if (a.assigned) head.appendChild(el("span", "sub", bbOnly ? ASSIGNED_BB : ASSIGNED));
      head.appendChild(el("span", "sub", a.sub));
      if (a.assignment) head.appendChild(el("span", "sub", a.assignment));
      li.appendChild(head);
      var body = el("div", "allow-body");
      if (a.assigned) {
        var routes = el("ul", "account-routes allow-routes");
        lines.forEach(function (r) {
          var rl = el("li", "account-route");
          var name = el("p", "account-route-name");
          name.appendChild(link("#/routes/" + r.route, r.title));
          rl.appendChild(name);
          if (r.bbThread && !bbOnly) rl.appendChild(el("p", "sub", ASSIGNED_BB));
          notesOf(r.notes).forEach(function (n) { rl.appendChild(el("p", "route-note is-" + n.tone, n.text)); });
          // The route's own reading: one plain sentence with its age, then
          // its windows. Being out is information here, not a warning.
          var reading = el("p", "allow-reading", say(r.reading));
          var u = routeUsage(d, r.route);
          if (u && u.observedAt) reading.title = fmt(dayTimeFmt, u.observedAt);
          rl.appendChild(reading);
          if (u && u.observedAt) rl.appendChild(el("p", "sub", "Source: recorded route probe · " + fmt(dayTimeFmt, u.observedAt)));
          if (u && u.availability !== "unavailable") {
            var box = el("div", "route-usage");
            if (u.needsPerson && u.ageNote) box.appendChild(el("p", "route-note is-attn usage-age", u.ageNote));
            box.appendChild(meters(u));
            rl.appendChild(box);
          }
          routes.appendChild(rl);
        });
        body.appendChild(routes);
        if (a.linesOmitted) body.appendChild(el("p", "more", a.linesOmitted + (a.linesOmitted === 1 ? " more route is" : " more routes are") + " not shown."));
      } else {
        notesOf(a.notes).forEach(function (n) { body.appendChild(el("p", "route-note is-" + n.tone, n.text)); });
        if (a.usage && a.usage.observedAt) body.appendChild(el("p", "sub", "Source: recorded route probe · " + fmt(dayTimeFmt, a.usage.observedAt)));
        var blk = usageBlock(a.usage);
        if (list(a.next).length) blk.appendChild(el("div", "sub allow-next", say(a.next)));
        body.appendChild(blk);
      }
      li.appendChild(body);
      ul.appendChild(li);
    });
    p.appendChild(ul);
    if (d.allowancesOmitted) p.appendChild(el("p", "more", d.allowancesOmitted + (d.allowancesOmitted === 1 ? " more block is" : " more blocks are") + " not shown."));
    return p;
  }

  // ---- Versions: where each build runs ----
  var versionDots = { current: "dot-good", same_file: "dot-good", newer: "dot-good", older: "dot-limit", different: "dot-limit", different_file: "dot-limit", stale: "dot-calm" };
  // Age is information: "Checked 3 h ago" in plain words, never a badge.
  // An observation older than a day adds a quiet hint to check again.
  // sameCheck leaves out the age and source when the login above already
  // says them for the same check.
  function accountEvidence(e, sameCheck) {
    e = e || {};
    var wrap = el("div", "account-evidence");
    wrap.appendChild(el("span", null, e.text || "Unavailable"));
    if (sameCheck) return wrap;
    if (e.ageSeconds != null) {
      var age = el("span", "sub account-age", "Checked " + span(e.ageSeconds) + " ago" + (e.checkAgain ? " · check again to confirm" : ""));
      if (e.observedAt) age.title = fmt(dayTimeFmt, e.observedAt);
      wrap.appendChild(age);
    } else if (e.observedAt) wrap.appendChild(el("span", "sub", "Checked at " + fmt(dayTimeFmt, e.observedAt)));
    if (e.source) wrap.appendChild(el("span", "sub", e.source));
    return wrap;
  }
  var failedChecks = {
    context_changed: "could not be compared: the context differs from the selection",
    timeout: "timed out",
    cancelled: "was cancelled",
    version_unavailable: "could not read the CLI version",
    command_unavailable: "could not run the selected CLI",
    status_unavailable: "returned no recognized login result"
  };
  // When and why the latest check failed. The login and plan shown are then
  // the last good check, or unavailable when there was none.
  function failedCheckLine(c) {
    return "Last check" + (c.failedCheckAt ? " " + fmt(secFmt, c.failedCheckAt) : "") + " " + (failedChecks[c.reason] || "did not complete") + "." +
      (c.attention === "context_changed" ? " Review and update the selection before checking again." : "");
  }
  // What on a card needs a person, in words; the warning style is kept for
  // these and never used for age alone. A failed check or a changed context
  // is said once, by failedCheckLine.
  var ACCOUNT_ATTENTION = {
    no_login: "No login reported. Sign in with the provider's own tool, then check again."
  };
  // The connection ID appears only with identifiers, as other identifiers on
  // the page do; otherwise a placeholder to replace.
  function accountId(c) { return state.identifiers && c ? c.id : "CONNECTION_ID"; }
  // The explicit check command to copy, for card c, or with the placeholder.
  function accountCheckCommand(c) {
    return el("code", "account-command", "looptender accounts check --id " + accountId(c) + " --json");
  }
  function accountCheckHint(c) {
    var p = el("p", "sub account-hint");
    p.appendChild(document.createTextNode("To check again, run "));
    p.appendChild(accountCheckCommand(c));
    p.appendChild(document.createTextNode(state.identifiers ? "" : " (Show identifiers to fill in the ID)"));
    return p;
  }
  // Whether a route names the entry, from the routes the report read; the
  // account card that lists them is later work.
  function accountRoutes(c) {
    if (c.routes === "named") return "A route names this account. See Routes.";
    if (c.routes !== "none") return "Not known: not every route could be read.";
    var f = frag();
    f.appendChild(document.createTextNode("No route names this account yet. Add "));
    f.appendChild(el("code", "account-command", "account: " + accountId(c)));
    f.appendChild(document.createTextNode(" to a route to link it (see docs/accounts.md)."));
    return f;
  }
  // A route bound to a catalog entry, as the loop page names it: the
  // person's label and that the binding is theirs, never a verified identity.
  var ASSIGNED = "assigned by you · identity not verified";
  var ASSIGNED_BB = "assigned by you · not verified for BB threads (account set per project)";
  function dockCapital(t) { return t ? t.charAt(0).toUpperCase() + t.slice(1) : t; }
  function boundAccount(a) {
    if (!a || a.assigned !== "by_you" || !a.label || (a.check !== "consistent" && a.check !== "not_verified")) return "";
    return a.label + " · " + (a.check === "not_verified" ? ASSIGNED_BB : ASSIGNED);
  }
  var SELECTIONS = { inherited: "Inherited context", explicit_directory: "Selected directory", bb_thread: "BB thread (account set per project)", externally_managed: "External context" };
  // The reading of a route by its key: the routes view's, else the first
  // loop's chain entry on it (a route past the routes view's cap).
  function routeUsage(d, key) {
    var r = list(d.routes).filter(function (x) { return x.key === key; })[0];
    if (r) return r.usage;
    var u = null;
    list(d.loops).some(function (l) {
      return list(l.chain && l.chain.entries).some(function (e) { if (e.route === key) { u = e.usage; return true; } return false; });
    });
    return u;
  }
  // The routes that use an account, each with the loops that use it and its
  // own reading. Readings are never merged.
  function accountRouteList(d, use, named) {
    var names = Object.create(null);
    list(d.loops).forEach(function (l) { names[l.key] = l.name; });
    var wrap = frag();
    // A route whose check found a mismatch says what the check says in
    // place of "assigned by you".
    if (list(use.routes).some(function (r) { return !r.bbThread && !r.checkText; }) && named) wrap.appendChild(el("p", "sub", ASSIGNED));
    var ul = el("ul", "account-routes");
    list(use.routes).forEach(function (r) {
      var li = el("li", "account-route");
      var head = el("p", "account-route-name");
      head.appendChild(link("#/routes/" + r.route, r.title));
      li.appendChild(head);
      if (r.checkText) li.appendChild(el("p", "sub", dockCapital(r.checkText) + ". Its reading is not counted for this account."));
      else if (r.bbThread && named) li.appendChild(el("p", "sub", ASSIGNED_BB));
      var used = el("p", "sub");
      used.appendChild(document.createTextNode(list(r.loops).length === 1 && !r.omitted ? "Used by the loop " : "Used by the loops "));
      list(r.loops).forEach(function (k, i) {
        if (i) used.appendChild(document.createTextNode(", "));
        used.appendChild(link("#/loop/" + k, names[k] || "a loop"));
      });
      if (r.omitted) used.appendChild(document.createTextNode(" and " + r.omitted + " more"));
      li.appendChild(used);
      li.appendChild(usageBlock(routeUsage(d, r.route)));
      ul.appendChild(li);
    });
    wrap.appendChild(ul);
    if (use.omitted) wrap.appendChild(el("p", "more", use.omitted + (use.omitted === 1 ? " more route is" : " more routes are") + " not shown."));
    return wrap;
  }
  // The newest reading among an account's routes, in the summary. Being out
  // is information here, not a warning.
  function accountReading(use) {
    return list(use && use.reading).length ? el("span", "sub account-reading", say(use.reading)) : null;
  }
  // Missing route bindings are not evidence of additional accounts.
  function unknownAccountSection(d, contexts) {
    var section = el("section", "unknown-account-routes");
    var heading = el("h2", null, "Routes with unknown accounts");
    heading.id = "unknown-account-routes";
    section.setAttribute("aria-labelledby", heading.id);
    section.appendChild(heading);
    section.appendChild(el("p", "sub", "These routes do not name a selected account. They may use an account above; separate routes do not mean separate subscriptions. Check which account each route uses before assigning it."));
    contexts.forEach(function (u) {
      var context = el("div", "unknown-route-context");
      context.appendChild(el("p", "sub", SELECTIONS[u.selection] || "Context as configured"));
      if (u.directory) context.appendChild(el("p", "sub command", u.directory));
      context.appendChild(accountRouteList(d, u, false));
      section.appendChild(context);
    });
    return section;
  }
  // Explicit subscription-quota reads have their own review and never run from
  // a page poll. This state cannot be mistaken for a loop or route action.
  function accountQuotaReply(d, id, step, recovery) {
    function text(v) { return typeof v === "string" && v.trim().length > 0 && v.length <= 16384; }
    if (!d || d.schemaVersion !== 1 || (!!d.preview === !!d.result)) throw new Error("quota reply");
    if (d.result) {
      var outcomes = step === "prepare" ? ["refused", "review_again"] : ["done", "refused", "review_again"];
      if (outcomes.indexOf(d.result.outcome) < 0 || !text(d.result.text)) throw new Error("quota outcome");
      return d;
    }
    var p = d.preview;
    if (step !== "prepare" || p.action !== (recovery ? "review_selection" : "subscription_quota") || p.key !== id ||
        !text(p.title) || !text(p.confirm) || !text(p.lost) || !text(p.refused) ||
        typeof p.form !== "string" || !/^[a-f0-9]{64}$/.test(p.form) ||
        typeof p.expiresAt !== "string" || !isFinite(Date.parse(p.expiresAt)) ||
        !Array.isArray(p.lines) || !p.lines.length || p.lines.length > 32 ||
        !p.lines.every(function (line) { return Array.isArray(line) && line.length > 0 && line.length <= 32 && line.every(function (part) { return part && text(part.t) && !part.at; }); })) throw new Error("quota review");
    return d;
  }
  function accountQuotaPost(id, step, form, recovery) {
    var abort = new AbortController(), timer = setTimeout(function () { abort.abort(); }, 15000);
    return fetch((recovery ? "api/v1/account-selections/" : "api/v1/accounts/") + encodeURIComponent(id) + "/" + step, {
      method: "POST", cache: "no-store", credentials: "same-origin", signal: abort.signal,
      headers: { "Content-Type": "application/json", Accept: "application/json", "X-Keepgoing-Form": form },
      body: JSON.stringify({ action: recovery ? "review_selection" : "subscription_quota" })
    }).then(function (r) {
      if (!r.ok) { var e = new Error("status " + r.status); e.status = r.status; throw e; }
      return r.json();
    }).then(function (d) { return accountQuotaReply(d, id, step, recovery); })
      .finally(function () { clearTimeout(timer); });
  }
  function accountQuotaSupported(c) {
    var providers = state.server.accountQuotaProviders;
    return !!state.server.accountQuota && (Array.isArray(providers) ? providers.indexOf(c.provider) >= 0 : c.provider === "codex");
  }
  function accountQuotaSummary(c) {
    var s = c.quota && c.quota.summary;
    if (!s || s.state !== "reported") return null;
    function date(v) { return typeof v === "string" && isFinite(Date.parse(v)); }
    if (c.provider === "codex" && s.source === "codex account/rateLimits/read" && date(s.observedAt) && !s.retrievedAt && (s.freshness === "fresh" || s.freshness === "stale")) return s;
    if (c.provider === "claude-code" && s.source === "claude:get_usage" && date(s.retrievedAt) && !s.observedAt && s.freshness === "provider_unknown") return s;
    return null;
  }
  function accountQuotaRequest(c, confirm, recovery) {
    if (!changesOn() || !(recovery ? state.server.accountSelectionReview : accountQuotaSupported(c))) return;
    var old = state.accountQuotaChange, p = confirm && old && old.id === c.id && !!old.recovery === !!recovery && old.preview;
    if (confirm && !p) return;
    var request = { id: c.id, recovery: !!recovery, busy: true, preview: p || null };
    state.accountQuotaChange = request;
    render(false, true);
    accountQuotaPost(c.id, confirm ? "confirm" : "prepare", confirm ? p.form : state.server.form, recovery)
      .then(function (d) { request.preview = d.preview; request.result = d.result; })
      .catch(function (err) {
        var refused = err && err.status >= 400 && err.status < 500;
        request.preview = null;
        request.result = { text: !confirm ? (recovery ? "The selection review could not be prepared. Nothing was updated." : "The quota review could not be prepared. Nothing was refreshed.") : refused ? p.refused : p.lost };
      }).then(function () {
        if (state.accountQuotaChange !== request) return;
        request.busy = false;
        render(false, true);
        focusOn(request.preview ? "quota-cancel-" + c.id : "quota-result-" + c.id);
        if (request.result) announce(request.result.text);
        if (confirm) { clearTimeout(state.timer); load(true); }
      });
  }
  function accountQuotaReason(reason) {
    return ({ not_checked: "No subscription quota reading is saved for this selection.", selection_review_required: "Review this unchanged selection for the panel, then refresh quota. Saved quota was not reused.", context_changed: "The selected context changed. Review it before refreshing quota.", cache_unavailable: "Private quota evidence could not be read. No usage is assumed.",
      time_unavailable: "The reading's time cannot be established.", unsupported_provider: "Subscription quota reads are available for selected Codex contexts only.", unsupported: "This Codex CLI does not support subscription quota reads.",
      timeout: "The quota read timed out.", cancelled: "The quota read was cancelled.", status_unavailable: "Codex could not provide a quota reading.", schema_unavailable: "The quota response was not recognized.",
      no_windows: "Codex reported no subscription windows.", unknown_bucket: "Codex did not report the supported subscription bucket.", unknown_windows: "Codex reported an unsupported quota window shape." })[reason] || "Subscription quota is unavailable.";
  }
  function accountQuotaBox(c) {
    var q = c.quota || {}, box = el("div", "account-quota");
    box.appendChild(el("h3", null, "Account quota"));
    box.appendChild(el("p", "sub", "Saved for this selection; separate from route readings. Live thread account binding is not verified."));
    var saved = accountQuotaSummary(c) || {};
    if (saved.state === "reported") {
      box.appendChild(el("p", "quota-stamp", saved.retrievedAt ? "Retrieved " + when({ at: saved.retrievedAt }) + " · Provider freshness unknown" : "Read " + when({ at: saved.observedAt }) + (saved.freshness === "stale" ? " · Older reading" : " · Saved snapshot")));
      if (saved.retrievedAt) box.appendChild(el("p", "sub", "Retrieval may return cached or seeded usage. The provider's observation time is unavailable."));
      var rows = el("ul", "quota-windows");
      list(saved.rows).forEach(function (w) {
        var row = el("li", "quota-window" + (w.ended ? " is-ended" : ""));
        row.appendChild(el("h4", null, w.name));
        row.appendChild(el("p", "quota-value", w.text));
        if (typeof w.usedPercent === "number" && !w.ended) {
          var meter = el("meter", "quota-meter"); meter.min = 0; meter.max = 100; meter.value = w.usedPercent;
          meter.setAttribute("aria-label", w.name + ": " + w.text); meter.textContent = w.text; row.appendChild(meter);
        }
        if (w.resetsAt) row.appendChild(el("p", "sub", (w.ended ? "Ended " : "Resets ") + when({ at: w.resetsAt })));
        else if (typeof w.usedPercent === "number" || typeof w.remainingPercent === "number") row.appendChild(el("p", "sub", "Reset time unavailable."));
        rows.appendChild(row);
      });
      box.appendChild(rows);
      if (saved.partial || saved.omitted) box.appendChild(el("p", "sub quota-incomplete", "Partial evidence: some quota detail is unavailable" + (saved.omitted ? "; " + saved.omitted + " additional buckets not shown" : "") + ". Missing values are not zero."));
      box.appendChild(el("p", "sub", "Source: " + saved.source));
    } else box.appendChild(el("p", "sub", (q.summary && q.summary.state !== "reported" && q.summary.text) || accountQuotaReason(q.reason || "schema_unavailable")));
    if (saved.failedRefreshAt) box.appendChild(el("p", "sub", "Refresh failed " + when({ at: saved.failedRefreshAt }) + ". " + saved.failedRefreshText + " Any earlier reading retains its original time."));
    if (c.provider !== "codex" && c.provider !== "claude-code") return box;
    var change = state.accountQuotaChange, current = change && change.id === c.id ? change : null;
    if (current) {
      var review = el("div", "account-quota-review");
      if (current.preview) {
        review.appendChild(el("h4", null, current.preview.title));
        list(current.preview.lines).forEach(function (line) { review.appendChild(el("p", null, say(line))); });
        var controls = el("div", "account-quota-actions");
        controls.appendChild(button("Cancel", "quota-cancel-" + c.id, "", function () { state.accountQuotaChange = null; render(false, true); focusOn(current.recovery ? "selection-review-" + c.id : "quota-refresh-" + c.id); }, current.busy));
        controls.appendChild(button(current.busy ? (current.recovery ? "Updating…" : "Reading…") : current.preview.confirm, "quota-confirm-" + c.id, "is-primary", function () { accountQuotaRequest(c, true, current.recovery); }, current.busy));
        review.appendChild(controls);
      } else if (current.result) {
        var result = el("p", null, current.result.text); result.tabIndex = -1; result.setAttribute("data-focus", "quota-result-" + c.id); review.appendChild(result);
        review.appendChild(button("Close", "quota-close-" + c.id, "", function () { state.accountQuotaChange = null; render(false, true); focusOn("account-" + c.id); }));
      } else review.appendChild(el("p", "sub", current.recovery ? "Preparing selection review…" : "Preparing quota review…"));
      box.appendChild(review);
    }
    var needsSelectionReview = q.reason === "selection_review_required";
    if (needsSelectionReview) {
      if (changesOn() && state.server.accountSelectionReview) {
        if (!current) box.appendChild(button("Review selection for this panel…", "selection-review-" + c.id, "", function () { accountQuotaRequest(c, false, true); }));
      } else box.appendChild(el("p", "sub", "Open a panel with changes allowed, then return to Accounts to review this selection. The native menu's Open Panel with Controls… provides that entry point."));
      return box;
    }
    if (changesOn() && accountQuotaSupported(c)) {
      if (!current) box.appendChild(button(c.provider === "claude-code" ? "Retrieve usage…" : "Refresh subscription quota…", "quota-refresh-" + c.id, "", function () { accountQuotaRequest(c, false); }));
    } else box.appendChild(el("p", "sub", c.provider === "claude-code" ? "Claude usage retrieval requires a panel with changes allowed on Darwin arm64 and the inspected native Claude Code 2.1.326 image. Each selection is checked before a request; other images remain unavailable." : "Review a refresh in a panel with changes allowed."));
    return box;
  }
  function renderAccounts(d, pane) {
    var a = d.accounts || {};
    pane.appendChild(back());
    pane.appendChild(paneHead("Accounts"));
    pane.appendChild(el("p", "lede", "Your selected accounts, with the evidence available on this device."));
    pane.appendChild(el("p", "sub", "Reading this page uses saved evidence. Subscription quota refresh is an explicit, reviewed read; it calls no model."));
    var usage = el("p", "account-usage-link");
    usage.appendChild(link("#/routes", "View recorded usage", "work-inspect"));
    usage.appendChild(el("span", "sub", ". Readings come from configured routes and are not live. Each account shows the readings of the routes that use it."));
    pane.appendChild(usage);
    if (d.accountsReference && (savedAccounts.reference !== d.accountsReference || savedAccounts.status !== "ready")) {
      var failed = savedAccounts.reference === d.accountsReference && savedAccounts.status === "failed";
      var pending = emptyBox(failed ? "Saved accounts unavailable" : "Reading saved accounts", failed ? "This saved snapshot could not be read. Use Read now to try the current panel snapshot. Missing quota is not zero; no provider refresh was made." : "Reading the saved selections and quota evidence for this panel snapshot. No provider is queried.");
      pending.setAttribute("role", "status"); pane.appendChild(pending);
      // A saved-only read after confirmation temporarily replaces the cards.
      // Keep its non-sensitive outcome and focus target through that gap;
      // old selection fields and quota rows remain withheld.
      var outcome = state.accountQuotaChange;
      if (outcome && outcome.result) {
        var feedback = el("p", null, outcome.result.text);
        feedback.tabIndex = -1; feedback.setAttribute("data-focus", "quota-result-" + outcome.id);
        pane.appendChild(feedback);
      }
      return;
    }
    if (!a.complete || a.state !== "available") {
      pane.appendChild(emptyBox("Accounts unavailable", "The private account catalog could not be read completely. This does not mean no accounts are selected."));
      return;
    }
    var cards = list(a.cards), uses = list(d.accountUses), byCard = Object.create(null);
    uses.forEach(function (u) { if (u.card) byCard[u.card] = u; });
    var unnamed = uses.filter(function (u) { return u.context; });
    if (!cards.length && !unnamed.length) {
      pane.appendChild(emptyBox("No accounts selected here yet", FIRST_RUN));
      pane.appendChild(addAccountBox(true));
      return;
    }
    pane.appendChild(el("p", "account-count", cards.length + (cards.length === 1 ? " selected account" : " selected accounts")));
    pane.appendChild(addAccountBox(!cards.length));
    if (!d.complete || (d.registry || {}).availability !== "available") pane.appendChild(el("p", "sub", "Not every loop could be read, so some routes may be missing here."));
    var ul = el("ul", "accounts-list"), retained = Object.create(null);
    cards.forEach(function (c) {
      var li = el("li", "account-card" + (c.needsPerson ? " is-attention" : "")), details = el("details");
      details.open = !!state.accountOpen[c.id]; retained[c.id] = details.open;
      var summary = el("summary", "account-summary");
      summary.setAttribute("data-focus", "account-" + c.id);
      var name = el("span", "account-name", c.label);
      var provider = el("span", "sub", c.provider === "claude-code" ? "Claude Code" : c.provider === "codex" ? "Codex" : c.provider);
      var identity = el("span", "account-title"); identity.appendChild(name); identity.appendChild(provider);
      identity.appendChild(el("span", "sub", c.selection === "inherited" ? "Inherited context" : c.selection === "explicit_directory" ? "Selected context" : "External context"));
      summary.appendChild(identity);
      var status = el("span", "account-status"); status.appendChild(accountEvidence(c.auth));
      // The login and plan shown are the last good check; say when and why the
      // latest one failed, once, with the warning when it needs a person.
      var failed = c.attention === "check_failed" || c.attention === "context_changed" || !!c.failedCheckAt;
      if (c.needsPerson) {
        var need = el("span", "account-attention");
        need.appendChild(tag("needs", "Needs you"));
        need.appendChild(el("span", null, failed ? failedCheckLine(c) : ACCOUNT_ATTENTION[c.attention] || "Something on this account needs a look."));
        status.appendChild(need);
      } else if (failed) status.appendChild(el("span", "sub", failedCheckLine(c)));
      var reading = accountReading(byCard[c.id]);
      if (reading) status.appendChild(reading);
      summary.appendChild(status);
      var plan = el("span", "account-plan");
      plan.appendChild(el("span", "sub", "Reported plan"));
      var auth = c.auth || {}, sameCheck = !!(c.plan && c.plan.observedAt && c.plan.observedAt === auth.observedAt && c.plan.source === auth.source);
      plan.appendChild(accountEvidence(c.plan, sameCheck)); summary.appendChild(plan);
      var qs = accountQuotaSummary(c);
      var compact = el("span", "quota-compact");
      if (qs && qs.state === "reported") {
        list(qs.rows).filter(function (w) { return typeof w.usedPercent === "number"; }).slice(0, 2).forEach(function (w) {
          var item = el("span", "quota-chip"); item.appendChild(el("span", "sub", w.name));
          item.appendChild(el("strong", null, w.ended ? "Window ended" : w.usedPercent + "% used")); compact.appendChild(item);
        });
        if (!compact.childNodes.length) compact.appendChild(el("span", "sub", qs.partial ? "Partial quota evidence" : "Quota details recorded"));
        compact.appendChild(el("span", "sub", qs.freshness === "provider_unknown" ? "Provider freshness unknown" : qs.freshness === "stale" ? "Older quota reading" : "Saved quota reading"));
        compact.title = "Saved account quota; expand for all recorded detail";
      } else if (c.provider === "codex" || c.provider === "claude-code") compact.appendChild(el("span", "sub", "Account quota unavailable"));
      summary.appendChild(compact);
      details.appendChild(summary);
      var body = el("div", "account-body");
      var facts = el("dl", "work-facts");
      if (state.identifiers) facts.appendChild(fact("Connection ID", c.id));
      facts.appendChild(fact("Account identity", "Not verified. The name is your selected label."));
      var use = byCard[c.id];
      facts.appendChild(fact("Routes", use && list(use.routes).length ? accountRouteList(d, use, true) : accountRoutes(c)));
      body.appendChild(facts);
      if (c.provider === "codex" || c.provider === "claude-code") body.appendChild(accountQuotaBox(c));
      // The catalog records no model: a route chooses it, and a check never
      // calls one. Said once, quietly, in place of two empty rows.
      if (c.configuredModel && c.configuredModel.missing === "not_recorded" && c.observedModel && c.observedModel.missing === "not_recorded") body.appendChild(el("p", "sub", "Models are not recorded here: a route chooses the model, and a check never calls one."));
      else {
        facts.appendChild(fact("Configured model", accountEvidence(c.configuredModel)));
        facts.appendChild(fact("Actual response model", accountEvidence(c.observedModel)));
      }
      var reasons = {
        context_changed: "The effective context differs from the selection. Review and update it before checking again.",
        unsupported: "Authentication is externally managed or unsupported here. Use the provider's own controls.",
        timeout: "The latest status check timed out. It did not establish a current login.",
        cancelled: "The latest status check was cancelled.",
        version_unavailable: "The selected CLI version could not be established.",
        status_unavailable: "The latest status command did not establish a recognized login result."
      };
      if (c.reason) body.appendChild(el("p", "account-note", reasons[c.reason] || "The latest account check is unavailable."));
      details.appendChild(body);
      details.addEventListener("toggle", function () { if (details.isConnected) state.accountOpen[c.id] = details.open; });
      li.appendChild(details);
      // A changed context is updated before it is checked again, so it gets no check command.
      if (c.attention !== "context_changed" && (c.needsPerson || (c.auth && c.auth.checkAgain) || (c.plan && c.plan.checkAgain))) li.appendChild(accountCheckHint(c));
      ul.appendChild(li);
    });
    state.accountOpen = retained;
    if (cards.length) pane.appendChild(ul);
    if (unnamed.length) pane.appendChild(unknownAccountSection(d, unnamed));
    if (d.accountUsesOmitted) pane.appendChild(el("p", "more", d.accountUsesOmitted + " more account entries or route contexts are not shown."));
    // One footnote for the page, naming the command. With one card its ID
    // fills in as on the card's own hint.
    var note = el("p", "sub account-footnote");
    note.appendChild(document.createTextNode("Login and plan reports do not verify quota, model access or continuing availability. Nothing checks again on its own: the age shown is when the last explicit check ran, and after a day the card suggests checking again. To record another observation, run "));
    if (!cards.length) return;
    note.appendChild(accountCheckCommand(cards.length === 1 ? cards[0] : null));
    note.appendChild(document.createTextNode(cards.length === 1 && state.identifiers ? "." : state.identifiers ? " with the card's connection ID." : " with the card's connection ID (Show identifiers to see it)."));
    pane.appendChild(note);
  }

  // ---- How to add an account, add a route and change the order ----
  // Text and commands to copy, for a person to run in Terminal. The page
  // runs nothing and changes nothing. A value the page does not know is a
  // placeholder in capitals, with one line saying what to put there.
  var IN_TERMINAL = "Run in Terminal, not in a BB agent thread.";
  var NAMES_ONLY = "This names the account. It does not put it in any loop. To use it, add it to a loop's order.";
  var FIRST_RUN = "An account is a sign-in you already have, with a name you choose. A route is one model on one of your accounts. Each loop works down its own order of routes: it uses the first that has room, moves to the next when one runs out, and moves back up when it can.";
  var ROUTE_ADD_FIELDS = "Put a new ID in place of ROUTE_ID (lowercase letters, digits and hyphens), claude-code or codex for PROVIDER, the model for MODEL, the usage limit it draws on for GROUP, and same_conversation, new_conversation or headless for ENTER.";
  // Whether each disclosure is open, by its key, kept across re-renders.
  var howOpen = Object.create(null);
  function howBox(key, title, open) {
    var d = el("details", "how");
    d.open = key in howOpen ? howOpen[key] : !!open;
    var s = el("summary", "how-summary", title);
    s.setAttribute("data-focus", key);
    d.appendChild(s);
    d.addEventListener("toggle", function () { if (d.isConnected) howOpen[key] = d.open; });
    return d;
  }
  // One command: what it does, which also describes its copy button, the
  // command, and the line that says what to put in place of a placeholder.
  function howStep(focus, what, command, fill, describedBy) {
    var w = el("div", "how-step");
    var p = el("p", null, what);
    p.id = focus + "-what";
    w.appendChild(p);
    w.appendChild(copyCommand("div", command, focus, (describedBy ? describedBy + " " : "") + p.id));
    if (fill) w.appendChild(el("p", "sub", fill));
    return w;
  }
  // Every block ends with where to run its commands.
  function howEnd(box, body) {
    body.appendChild(el("p", "how-terminal", IN_TERMINAL));
    box.appendChild(body);
    return box;
  }
  // "--loop NAME" with the name the server quoted for a shell, or a
  // placeholder when the name is too long to put in a command.
  function loopArg(l) { return l.loopArg || "--loop LOOP_NAME"; }
  var LOOP_NAME_FILL = "Put this loop's name in place of LOOP_NAME, in quotes if it has a space.";
  function withLoopFill(l, fill) { return l.loopArg ? fill : [fill, LOOP_NAME_FILL].filter(Boolean).join(" "); }
  var ROUTE_ID = /^[a-z0-9-]+$/;

  function addAccountBox(open) {
    var box = howBox("how-account", "Add an account", open), body = el("div", "how-body");
    var cases = [
      ["folder", "Another subscription of a provider you use",
        "Sign in with the provider's own tool in a new folder, then name that folder here.",
        "looptender accounts add --label ACCOUNT_LABEL --provider PROVIDER --selection explicit_directory --account-dir FOLDER",
        "Put a name you choose in place of ACCOUNT_LABEL (in quotes if it has a space), claude-code or codex for PROVIDER, and the folder you signed in to for FOLDER."],
      ["managed", "A provider that manages its own login",
        "Cursor and BB keep their own login. Name the account here; looptender does not check it.",
        "looptender accounts add --label ACCOUNT_LABEL --provider PROVIDER --selection externally_managed",
        "Put a name you choose in place of ACCOUNT_LABEL, and cursor or bb for PROVIDER."],
      ["key", "An API key",
        "Give the key its own entry. If you use it through the provider's tool, sign in with it in its own folder and name that folder as above. Otherwise:",
        "looptender accounts add --label ACCOUNT_LABEL --provider other --selection externally_managed",
        "Put a name you choose in place of ACCOUNT_LABEL. looptender never takes the key."]
    ];
    cases.forEach(function (c) {
      var h = el("h3", "how-case", c[1]);
      h.id = "how-account-" + c[0];
      body.appendChild(h);
      body.appendChild(howStep("copy-account-" + c[0], c[2], c[3], c[4], h.id));
    });
    // Said once, after the three cases.
    body.appendChild(el("p", null, NAMES_ONLY));
    return howEnd(box, body);
  }

  // The loop page: add a route to this loop, and change its order. A loop
  // that follows the standard route list changes that list instead.
  function addRouteBox(l) {
    var c = l.chain || {}, arg = loopArg(l);
    var box = howBox("how-route-" + l.key, "Add a route to this loop"), body = el("div", "how-body");
    if (c.standard) {
      body.appendChild(el("p", null, "This loop follows your standard route list, which every loop that follows it uses. Add the route to that list."));
      body.appendChild(howStep("copy-route-add", "Add a route to the standard list.",
        "looptender routes add --id ROUTE_ID --provider PROVIDER --model MODEL --group GROUP --enter ENTER", ROUTE_ADD_FIELDS));
      body.appendChild(el("p", null, "A new route is used only after a check of it passes."));
      body.appendChild(howStep("copy-route-check", "Check it. This is one real request and uses this account's quota.",
        "looptender routes check ROUTE_ID --exact " + arg, withLoopFill(l, "Put the new route's ID in place of ROUTE_ID.")));
      if (!list(c.handoffOn).length) body.appendChild(el("p", null, "Automatic handoff is off for this loop, so it does not move to another route by itself. For a loop that follows the standard list, it is set by errors.failover_on in the loop's configuration file."));
    } else {
      body.appendChild(howStep("copy-route-add", "Add a route. A guide asks for the provider, the account, the model and its place in the order, and shows a preview before it changes anything.",
        "looptender fallbacks add " + arg, withLoopFill(l, "")));
      body.appendChild(el("p", null, "A new route is used only after a check of it passes."));
      body.appendChild(howStep("copy-route-check", "Check it. This is one real request and uses this account's quota.",
        "looptender fallbacks check --id ROUTE_ID --exact " + arg, withLoopFill(l, "Put the new route's ID in place of ROUTE_ID; the guide shows it in its preview.")));
      if (!list(c.handoffOn).length) body.appendChild(howStep("copy-route-handoff", "Automatic handoff is off for this loop, so it does not move to another route by itself. To turn it on:",
        "looptender fallbacks handoff on " + arg, withLoopFill(l, "")));
    }
    return howEnd(box, body);
  }
  function reorderBox(l) {
    var c = l.chain || {}, rest = list(c.entries).filter(function (e) { return e.position > 0; });
    if (rest.length + (c.omitted || 0) < 2) return null;
    var box = howBox("how-order-" + l.key, "Change the order"), body = el("div", "how-body");
    var move = c.standard ? function (id) { return "looptender routes move " + id + " --to PLACE"; } :
      function (id) { return "looptender fallbacks move " + id + " --to PLACE " + loopArg(l); };
    body.appendChild(el("p", null, c.standard ?
      "This loop follows your standard route list, so the order is that list's, and every loop that follows it gets the new order. Put the route's new place in the list in place of PLACE: 1 is the top." :
      "The first choice stays first. Put the route's new place after it in place of PLACE: 1 is the next choice."));
    var ids = rest.filter(function (e) { return e.id && ROUTE_ID.test(e.id); });
    if (state.identifiers && ids.length === rest.length) {
      rest.forEach(function (e) {
        body.appendChild(howStep("copy-move-" + e.position, "Choice " + (e.position + 1) + ": " + e.title, move(e.id), ""));
      });
      if (!c.standard && !l.loopArg) body.appendChild(el("p", "sub", LOOP_NAME_FILL));
    } else {
      var fill = "Put the route's ID in place of ROUTE_ID.";
      if (!state.identifiers) fill = "Put the route's ID in place of ROUTE_ID. Show identifiers to see each route's ID.";
      body.appendChild(howStep("copy-move", "Move a route.", move("ROUTE_ID"), c.standard ? fill : withLoopFill(l, fill)));
    }
    return howEnd(box, body);
  }
  // The Routes page: add a route to the standard list, then have a loop
  // follow it.
  // Route configuration is kept apart from background report refreshes.
  // Draft values are captured on input; a review is immutable server state.
  var retainedRouteEditor = null;
  var routeEdit = { data: null, draft: {}, models: [], busy: false, reply: null, message: "" };
  var ROUTE_DOCS = "routes-help.html";
  function routeButton(text, id, run) {
    var b = el("button", "btn", text); b.type = "button"; b.dataset.focus = id; b.disabled = routeEdit.busy;
    b.addEventListener("click", run); return b;
  }
  function routeRequest(step, input, form) {
    routeEdit.busy = true; routeEdit.message = ""; render(false, true);
    return post("api/v1/routes/" + step, form || state.server.form, input)
      .then(function (r) {
        if (r.editor) routeEdit.data = r.editor;
        else if (step === "models") { routeEdit.models = list(r.models); routeEdit.message = r.text || ""; }
        else routeEdit.reply = r;
      }).catch(function () {
        routeEdit.message = step === "confirm" ? "The save result is unknown. Read the current list before preparing another change; this save will not be retried." : "The panel did not answer. Nothing was saved. Try reading the editor again.";
        routeEdit.reply = null;
      }).then(function () {
        routeEdit.busy = false; render(false, true); focusOn("route-feedback");
        announce(routeEdit.message || (routeEdit.reply && routeEdit.reply.text) || "Route editor loaded.");
      });
  }
  function routeField(form, name, label, options, required) {
    var wrap = el("div", "route-field"), id = "route-field-" + name, control;
    wrap.appendChild(el("label", null, label)); wrap.firstChild.htmlFor = id;
    if (options) {
      control = el("select");
      options.forEach(function (o) { var opt = el("option", null, o[1]); opt.value = o[0]; control.appendChild(opt); });
    } else { control = el("input"); control.type = "text"; control.maxLength = 128; control.autocomplete = "off"; }
    control.id = id; control.name = name; control.dataset.focus = id; control.required = !!required;
    control.value = routeEdit.draft[name] || ""; control.disabled = routeEdit.busy;
    control.addEventListener("input", function () {
      if (name === "model" && routeEdit.draft.model !== control.value) { routeEdit.draft.effort = ""; var effort = $("route-field-effort"); if (effort) effort.value = ""; }
      routeEdit.draft[name] = control.value;
    });
    control.addEventListener("change", function () { routeEdit.draft[name] = control.value; if (name === "account") { routeEdit.models = []; routeEdit.draft.model = ""; routeEdit.draft.effort = ""; routeEdit.draft.behavior = ""; routeEdit.draft.newConversation = false; } if (name === "model") routeEdit.draft.effort = ""; if (options) render(false, true); });
    wrap.appendChild(control); form.appendChild(wrap); return control;
  }
  function routeCheck(parent, name, label) {
    var wrap = el("label", "route-check"), input = el("input"); input.type = "checkbox"; input.name = name;
    input.checked = !!routeEdit.draft[name]; input.disabled = routeEdit.busy;
    input.addEventListener("change", function () { routeEdit.draft[name] = input.checked; });
    wrap.appendChild(input); wrap.appendChild(el("span", null, label)); parent.appendChild(wrap); return input;
  }
  function routeProvider(provider) { return provider === "claude-code" ? "Claude Code" : provider === "codex" ? "Codex" : provider || "Configured provider"; }
  function routeTitle(row) { var label = routeEdit.data && routeEdit.data.accountLabels && routeEdit.data.accountLabels[row.id]; if (!label && routeEdit.reply && routeEdit.reply.accountLabels) label = routeEdit.reply.accountLabels[row.id]; return (row.model || "Configured default model") + (row.effort ? " · " + row.effort : "") + " · " + routeProvider(row.provider) + " · " + (label || "Account unknown") + (row.disabled ? " · Off" : ""); }
  function routeEffects(parent, r) {
    var labels = {}, beforeLabels = {};
    list(routeEdit.data && routeEdit.data.standard && routeEdit.data.standard.routes).forEach(function (row) { beforeLabels[row.id] = routeTitle(row); });
    list(routeEdit.data && routeEdit.data.loops).forEach(function (entry) { if (entry.view.loop === r.loop) list(entry.view.routes).forEach(function (row) { beforeLabels[row.id] = routeTitle(row); }); });
    list(r.routes).forEach(function (row) { labels[row.id] = routeTitle(row); });
    function names(ids, source) { return list(ids).map(function (id) { return source[id] || (id === "primary" ? "First route" : "Configured route"); }).join(" → ") || "No standby routes"; }
    var before = el("p", "sub", "Before: " + names(r.before, beforeLabels)), after = el("p", null, "After: " + names(r.after, labels));
    parent.appendChild(before); parent.appendChild(after);
    list(r.loops).forEach(function (l) {
      var item = el("div", "route-effect"); item.appendChild(el("h4", null, l.loop));
      item.appendChild(el("p", null, "Standby routes after saving: " + names(l.after, labels)));
      if (l.problem) item.appendChild(el("p", "c-warn", "Configuration unavailable; effects are unknown."));
      if (l.currentLeavesList) item.appendChild(el("p", "c-warn", "The current route leaves this list. The loop stays there until its ordinary rules move it."));
      if (l.noRouteApplies) item.appendChild(el("p", "c-warn", "No route in this list applies to this loop."));
      if (l.belowMinHealthy) item.appendChild(el("p", "c-warn", "Recorded healthy routes are below this loop's minimum."));
      list(l.newlySkipped).forEach(function (skip) { item.appendChild(el("p", "sub", (labels[skip.id] || "A route") + ": " + skip.reason)); });
      parent.appendChild(item);
    });
    list(r.warnings).forEach(function (text) { parent.appendChild(el("p", "c-warn", text)); });
    if (r.refusal) parent.appendChild(el("p", "c-warn", r.refusal.text));
  }
  function routeReview(parent) {
    var r = routeEdit.reply;
    if (!r && !routeEdit.message && !routeEdit.busy) return false;
    var box = el("section", "route-feedback"); box.id = "route-feedback"; box.tabIndex = -1; box.dataset.focus = "route-feedback";
    box.setAttribute("aria-labelledby", "route-review-title");
    var title = el("h3", null, r && r.form ? "Review route change" : "Route update"); title.id = "route-review-title"; box.appendChild(title);
    box.appendChild(el("p", null, routeEdit.busy ? "Waiting for the panel…" : routeEdit.message || (r && r.text) || ""));
    if (r) list(r.lines).forEach(function (line) { box.appendChild(el("p", null, line)); });
    if (r) list(r.changes).forEach(function (change) {
      if (change.loop) box.appendChild(el("h4", null, change.loop + (r.form ? "" : ": " + change.outcome.replace(/_/g, " "))));
      routeEffects(box, change);
    });
    if (r && r.form) {
      box.appendChild(routeButton("Save reviewed change", "route-save", function () {
        var op = r.changes[0].op;
        // Spend the local confirmation before sending; a lost response is
        // an unknown outcome, never an automatic retry.
        var form = r.form; routeEdit.reply = null;
        routeRequest("confirm", { op: op }, form).then(function () { routeEdit.data = null; render(false, true); });
      }));
      box.appendChild(routeButton("Cancel review", "route-cancel", function () { routeEdit.reply = null; render(false, true); focusOn("route-edit-refresh"); }));
    }
    parent.appendChild(box); return !!(r && r.form);
  }
  function routeEditorBox() {
    if (retainedRouteEditor) return retainedRouteEditor;
    var box = panel("Configure preferred routes"); box.classList.add("route-editor");
    box.appendChild(el("p", "sub", "One standard order for loops that follow it. Saving a list does not enroll other loops."));
    if (!changesOn() || !state.server || state.server.routeChanges !== "person") {
      box.appendChild(el("p", null, changesOn() && state.server.routeChanges === "bb_caller" ?
        "This panel was started by a BB thread. Only a person can edit the standard list. Stop it and open a panel outside BB with changes allowed; reopening this window keeps its original caller." :
        "Route editing requires a panel started outside BB with changes allowed. This window is read-only for route configuration."));
      box.appendChild(link(ROUTE_DOCS, "How to open an owner panel")); return box;
    }
    var reviewing = routeReview(box);
    box.appendChild(routeButton("Read current configuration", "route-edit-refresh", function () { routeEdit.reply = null; routeRequest("read", { op: "read" }); }));
    var data = routeEdit.data;
    if (!data || reviewing || routeEdit.busy) { box.appendChild(link(ROUTE_DOCS, "Route configuration help", "more")); return box; }
    if (data.reason) { box.appendChild(el("p", "c-warn", data.reason)); return box; }
    var standard = data.standard || {}, rows = list(standard.routes);
    box.appendChild(el("h3", null, "Preferred order"));
    var order = el("ol", "route-edit-order");
    rows.forEach(function (row, i) {
      var li = el("li"), name = routeTitle(row);
      li.appendChild(el("p", null, name));
      function action(label, op, to) { return routeButton(label, "route-" + op + "-" + row.id + "-" + to, function () { routeRequest("prepare", { op: op, id: row.id, to: to || 0 }); }); }
      var controls = el("div", "route-actions");
      if (i) controls.appendChild(action("Move earlier", "move", i));
      if (i < rows.length - 1) controls.appendChild(action("Move later", "move", i + 2));
      controls.appendChild(action(row.disabled ? "Turn on" : "Turn off", row.disabled ? "on" : "off"));
      controls.appendChild(action("Remove", "remove"));
      Array.prototype.forEach.call(controls.querySelectorAll("button"), function (b) { b.setAttribute("aria-label", b.textContent + ": " + name); });
      li.appendChild(controls); order.appendChild(li);
    });
    box.appendChild(order);
    if (!rows.length) box.appendChild(el("p", "sub", standard.state === "unreadable" ? "The standard list cannot be read. Resolve it before editing." : "No standard routes yet. Add your first route below."));
    if (list(standard.can).indexOf("add") >= 0) {
      var form = el("form", "route-add-form"); form.method = "post"; form.action = "api/v1/routes/prepare"; form.setAttribute("aria-labelledby", "route-add-title");
      var heading = el("h3", null, "Add a route"); heading.id = "route-add-title"; form.appendChild(heading);
      var accounts = list(data.accounts), chosen = accounts.filter(function (a) { return a.id === routeEdit.draft.account; })[0];
      routeField(form, "account", "Existing account selection", [["", "Choose an account"]].concat(accounts.map(function (a) { return [a.id, a.label + " · " + routeProvider(a.provider)]; })), true);
      form.appendChild(el("p", "sub", "You state which account this route uses. Selection does not verify a live thread's identity, model access or available quota."));
      if (!accounts.length) form.appendChild(link("#/accounts", "No supported account selections. View Accounts."));
      if (chosen) {
        if (routeEdit.models.length) form.appendChild(routeButton("Enter model names instead", "route-model-manual", function () { routeEdit.models = []; render(false, true); }));
        form.appendChild(routeButton("Load models reported by BB", "route-models", function () { routeRequest("models", { op: "models", provider: chosen.provider }); }));
        form.appendChild(el("p", "sub", "Model listing is optional and read-only. Without a listing, enter names supported by your provider."));
        if (routeEdit.models.length) {
          var offered = routeEdit.models.filter(function (m) { return m.model === routeEdit.draft.model; })[0];
          if (!offered) { routeEdit.draft.model = ""; routeEdit.draft.effort = ""; }
          else if (!list(offered.supportedReasoningEfforts).some(function (e) { return e.reasoningEffort === routeEdit.draft.effort; })) routeEdit.draft.effort = "";
        }
        var modelOptions = routeEdit.models.length ? [["", "Choose a model"]].concat(routeEdit.models.map(function (m) { return [m.model, m.displayName || m.model]; })) : null;
        routeField(form, "model", "Model", modelOptions, true);
        var model = routeEdit.models.filter(function (m) { return m.model === routeEdit.draft.model; })[0];
        var efforts = model ? list(model.supportedReasoningEfforts).map(function (e) { return [e.reasoningEffort, e.reasoningEffort]; }) : null;
        if (chosen.provider === "codex" && chosen.selection !== "externally_managed") form.appendChild(el("p", "sub", "Codex CLI routes have no effort override."));
        else routeField(form, "effort", "Effort (optional)", efforts ? [["", "Provider default"]].concat(efforts) : null, false);
        var details = el("details", "route-settings"); details.appendChild(el("summary", null, "How conversation and limit choices work"));
        var behavior = chosen.selection === "externally_managed" ? [["", "Choose conversation behavior"], ["same_conversation", "Continue the loop's BB conversation"], ["new_conversation", "Start a new BB conversation at handoff"]] : [["", "Choose execution behavior"], ["headless", "Run the selected CLI without a conversation window"]];
        routeField(form, "behavior", "How this route runs", behavior, true);
        if (routeEdit.draft.behavior === "new_conversation") routeCheck(form, "newConversation", "Allow this route to start a conversation at handoff. Each loop must also opt in; its existing creation cap and spacing still apply.").required = true;
        details.appendChild(el("p", "sub", "A conversation route uses that conversation's existing login; it does not switch credentials. A CLI route keeps each loop's executable and working directory, using its inherited login or the selected existing directory."));
        var limits = [];
        rows.forEach(function (r) { if (limits.indexOf(r.group) < 0) limits.push(r.group); });
        routeField(form, "limitChoice", "Which recorded limit does it share?", [["", "Choose a limit relationship"], ["new", "New label (relationship checked)"]].concat(limits.map(function (g) { return [g, "Share the limit labelled " + g]; })), true);
        if (routeEdit.draft.limitChoice === "new") routeField(form, "limit", "New limit label", null, true);
        details.appendChild(el("p", "sub", "Labels record your configuration. They do not prove separate subscriptions or independent quota. Routes sharing limits must share a label."));
        form.appendChild(details);
        var submit = el("button", "btn", "Review new route"); submit.type = "submit"; submit.dataset.focus = "route-add-review"; form.appendChild(submit);
      }
      form.addEventListener("submit", function (ev) {
        ev.preventDefault(); if (!form.reportValidity()) return;
        var values = new FormData(form), limitChoice = values.get("limitChoice");
        routeRequest("prepare", { op: "add", account: values.get("account"), model: values.get("model"), effort: values.get("effort") || "", behavior: values.get("behavior"), newConversation: values.get("newConversation") === "on", limit: limitChoice === "new" ? values.get("limit") : limitChoice });
      }); box.appendChild(form);
    }
    if (rows.length) {
      var follow = el("form", "route-follow-form"); follow.method = "post"; follow.action = "api/v1/routes/prepare";
      var fieldset = el("fieldset"); fieldset.appendChild(el("legend", null, "Loops following this order"));
      list(data.loops).forEach(function (entry) {
        var view = entry.view, allowed = list(view.can).indexOf("follow_standard") >= 0;
        if (view.list === "standard") { fieldset.appendChild(el("p", "sub", view.loop + " · Already follows the standard list")); return; }
        var cb = routeCheck(fieldset, "loop-" + entry.key, view.loop + (allowed ? (view.handoff === "off" ? " · Handoff is off and stays off" : "") : " · Cannot follow now: " + (view.canReason || view.problem || "not available").replace(/_/g, " ")));
        cb.disabled = !allowed;
      });
      follow.appendChild(fieldset);
      follow.appendChild(el("p", "sub", "Review and save this separately from route edits. Each loop keeps its holds, budgets, handoff settings and conversation opt-in. Saves are per loop, not atomic."));
      var submit = el("button", "btn", "Review selected loops"); submit.type = "submit"; follow.appendChild(submit);
      follow.addEventListener("submit", function (ev) { ev.preventDefault(); var selected = list(data.loops).filter(function (e) { return list(e.view.can).indexOf("follow_standard") >= 0 && routeEdit.draft["loop-" + e.key]; }).map(function (e) { return e.key; }); if (selected.length > 64) { routeEdit.message = "Select up to 64 loops per review."; render(false, true); focusOn("route-feedback"); return; } routeRequest("prepare", { op: "follow_standard", loops: selected }); });
      box.appendChild(follow);
    }
    box.appendChild(link(ROUTE_DOCS, "Route configuration help and CLI reference", "more")); return box;
  }

  function renderVersions(d, pane) {
    var v = d.versions || {};
    pane.appendChild(back());
    pane.appendChild(paneHead("Versions"));
    pane.appendChild(el("p", "lede", v.lede || ""));
    var rows = list(v.rows);
    if (!rows.length) {
      pane.appendChild(emptyBox("Nothing to show.", ""));
      return;
    }
    if (v.summary) pane.appendChild(el("p", "route-summary", v.summary));
    var wrap = panel("Where each build runs", v.clients === "unavailable" ? [el("span", "sub", "the clients' reports could not be read")] : []);
    var ul = el("ul", "allow-list");
    rows.forEach(function (x) {
      var li = el("li", "allow");
      var head = el("div", "allow-head");
      var name = el("h3", "allow-name");
      if (x.key) name.appendChild(link("#/loop/" + x.key, x.title));
      else name.textContent = x.title;
      head.appendChild(name);
      if (x.label) head.appendChild(el("code", "sub", x.label));
      li.appendChild(head);
      var body = el("div", "allow-body");
      var line = el("p", "route-status");
      line.appendChild(el("span", "dot " + (versionDots[x.availability] || "dot-unknown")));
      line.appendChild(el("span", null, x.text));
      body.appendChild(line);
      if (x.update) body.appendChild(el("p", "allow-next", x.update));
      if (x.seen) body.appendChild(el("p", "allow-next sub", x.seen));
      if (x.seenAt) body.appendChild(el("p", "allow-next sub", "Last reported " + fmt(dateYearFmt, x.seenAt) + "."));
      li.appendChild(body);
      ul.appendChild(li);
    });
    wrap.appendChild(ul);
    var none = list(v.unrecorded);
    if (none.length) {
      var p = el("p", "more", "Which build ran the last check is not recorded for " + (none.length === 1 ? "this loop" : "these " + none.length + " loops") + ": ");
      none.forEach(function (x, i) {
        if (i) p.appendChild(document.createTextNode(", "));
        p.appendChild(link("#/loop/" + x.key, x.name));
      });
      p.appendChild(document.createTextNode("."));
      wrap.appendChild(p);
    }
    pane.appendChild(wrap);
  }

  function renderRoutes(d, pane) {
    pane.appendChild(back());
    pane.appendChild(paneHead("Routes"));
    pane.appendChild(el("p", "lede", "Each model on an account that a loop runs on or keeps on standby, as configured. looptender does not verify which account a thread is using."));
    var routes = list(d.routes), rs = d.routesSummary || {};
    if (!routes.length) {
      pane.appendChild(emptyBox(d.registry.availability === "available" ? "No routes to show." : "Unknown: the registry could not be read.", ""));
      if (d.registry.availability === "available") pane.appendChild(routeEditorBox());
      return;
    }
    var sum = el("p", "route-summary");
    sum.appendChild(el("span", null, rs.total || routes.length + " routes"));
    if (rs.inUseText) sum.appendChild(el("span", "", rs.inUseText));
    if (rs.trouble && rs.first) sum.appendChild(link("#/routes/" + rs.first, rs.problem, "c-warn"));
    else if (rs.problem) sum.appendChild(el("span", "sub", rs.problem));
    pane.appendChild(sum);
    pane.appendChild(routeEditorBox());
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
    // This is when the panel read its records, not how new they are: the
    // oldest usage reading shown is named when one is past stale-after.
    var old = state.data.oldestUsageAgeSeconds;
    r.textContent = "Records read at " + fmt(secFmt, state.data.readAt) + (state.paused ? " · updates paused" : " · read again every " + (every >= 60 ? span(every) : every + " s")) +
      (old != null ? " · oldest usage reading " + span(old) + " old" : "") +
      (state.failed ? " · the last read failed" : "");
    r.classList.toggle("is-stale", state.failedLong);
    // The line keeps a fixed box; when it is cut there, its tooltip has it whole.
    if (r.scrollHeight > r.clientHeight + 1) r.title = r.textContent; else r.removeAttribute("title");
  }
  // Only what changes for a person is announced: the needs-you line, and a
  // read that failed. The time of each read is not.
  function announce(text) {
    if (text === state.said) return;
    state.said = text;
    $("live").textContent = text;
  }
  // A focusable element is found again after a redraw by a key that does
  // not change when its text does (an age, a count): its tag, its link
  // target, its id or data-focus name, and which of those it was in order.
  var FOCUSABLE = "a[href], button, input, select, textarea, summary, [tabindex]";
  function signature(n) { return n.tagName + "|" + (n.getAttribute("href") || "") + "|" + (n.id || "") + "|" + (n.getAttribute("data-focus") || ""); }
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
    if (!navigated && !force && r.view === "routes" && pane.contains(document.activeElement) && document.activeElement.matches("input, select, textarea")) { state.pending = true; renderReadline(); return; }
    var top = navigated ? 0 : window.scrollY;
    var active = document.activeElement, inPane = pane.contains(active), inPicker = $("picker").contains(active);
    var key = inPane ? focusKey(active, pane) : inPicker ? focusKey(active, $("picker")) : null;
    retainedRouteEditor = !navigated && !force && r.view === "routes" && routeEdit.data && changesOn() && state.server.routeChanges === "person" ? pane.querySelector(".route-editor") : null;
    try {
      $("layout").dataset.view = r.view;
      renderPicker(d, r);
      pane.replaceChildren();
      pane.setAttribute("aria-labelledby", "pane-title");
      if (r.view === "loop") renderLoop(d, pane, r.key);
      else if (r.view === "routes") renderRoutes(d, pane);
      else if (r.view === "versions") renderVersions(d, pane);
      else if (r.view === "usage") renderUsage(d, pane);
      else if (r.view === "accounts") renderAccounts(d, pane);
      else renderHome(d, pane);
    } catch (err) {
      retainedRouteEditor = null;
      pane.replaceChildren(emptyBox("This page could not show the reply it was sent.", "Reload the page, or restart keepgoing " + served() + "."));
      notice("render", "This page could not show the reply it was sent. Reload the page, or restart keepgoing " + served() + ".", true);
      renderReadline();
      return false;
    }
    retainedRouteEditor = null;
    renderReadline();
    var needs = list(d.needsYou).length;
    document.title = (needs ? "(" + needs + ") " : "") + "looptender";
    // The needs-you line is announced when it changes, not on every read, so
    // a read does not talk over the outcome of a change.
    var needsSaid = (d.needsLine || "Needs you: unknown") + ".";
    if (needsSaid !== state.needsSaid) { state.needsSaid = needsSaid; announce(needsSaid); }
    window.scrollTo(0, top);
    if (navigated) {
      // A link to one route lands on that route; any other lands on the
      // view's heading. A read that is not a change of view moves nothing.
      var at = r.view === "routes" && r.key ? $("route-" + r.key) : null;
      if (at) { at.focus({ preventScroll: true }); at.scrollIntoView({ block: "start" }); }
      else focusPaneTitle();
      var cur = $("picker").querySelector('.pick-scroll [aria-current="page"]');
      if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
    } else if (key) {
      // Keep the keyboard where it was: the same link or button, rebuilt.
      var again = findKey(key, inPane ? pane : $("picker"));
      if (again) again.focus({ preventScroll: true });
    }
  }

  // The banner sits over the foot of the window, so it never moves what is
  // being read. It is neutral while the page recovers on its own and turns
  // to a warning only once a person is needed (alarm). It has no live role:
  // the polite region says each episode once, when it starts and when it
  // turns into an alarm, not on every read that fails again.
  function notice(key, text, alarm) {
    var b = $("banner");
    b.hidden = !text;
    b.textContent = text || "";
    b.classList.toggle("is-alarm", !!alarm);
    document.body.classList.toggle("has-banner", !!text);
    var said = text ? key + (alarm ? "!" : "") : "";
    if (said !== state.notice) {
      state.notice = said;
      if (text) { state.said = ""; announce(text); }
    }
  }
  function refreshSeconds() {
    var n = Number(state.data && state.data.refreshSeconds);
    return n >= 5 && n <= 3600 ? n : 15;
  }

  // Which command serves this page: "dock" until the server says otherwise.
  function served() { return state.server && state.server.servedBy === "panel" ? "panel" : "dock"; }

  function put(node, parts) {
    parts.forEach(function (part) { node.appendChild(typeof part === "string" ? document.createTextNode(part) : part); });
  }

  // The footer's first line: whether this page can change anything.
  function renderServed() {
    var m = $("changes");
    if (m && state.server && list(state.server.changesText).length) m.textContent = say(state.server.changesText);
  }

  // How long one read may take: a little under the refresh interval, from 4
  // to 30 seconds, so a read that never answers cannot stop the updates. A
  // read that runs out of time fails like any other failed read.
  function readTimeoutMs() { return Math.min(30, Math.max(4, refreshSeconds() - 2)) * 1000; }
  function getJSON(url) {
    var ctl = new AbortController();
    var timer = setTimeout(function () { ctl.abort(); }, readTimeoutMs());
    return fetch(url, { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" }, signal: ctl.signal })
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .finally(function () { clearTimeout(timer); });
  }

  // A bounded companion to the current main read, only on Accounts. No
  // independent timer or provider refresh. A new snapshot or privacy view
  // invalidates both successful and delayed replies from the previous read.
  function loadSavedAccounts(retry) {
    var d = state.data;
    if (!d || !d.accountsReference || route().view !== "accounts") return;
    var ref = d.accountsReference, asked = state.identifiers;
    if (savedAccounts.reference === ref && savedAccounts.status === "ready") { d.accounts = savedAccounts.data; return; }
    if (savedAccounts.reference === ref && (savedAccounts.status === "loading" || (!retry && savedAccounts.status === "failed"))) return;
    if (savedAccounts.abort) savedAccounts.abort.abort();
    var ctl = new AbortController(), seq = ++savedAccounts.serial;
    savedAccounts.reference = ref; savedAccounts.status = "loading"; savedAccounts.data = null; savedAccounts.abort = ctl;
    var timer = setTimeout(function () { ctl.abort(); }, readTimeoutMs());
    function current() { return seq === savedAccounts.serial && state.data && state.data.accountsReference === ref && state.identifiers === asked; }
    fetch(SAVED_ACCOUNTS + "?reference=" + encodeURIComponent(ref), { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" }, signal: ctl.signal })
      .then(function (r) { if (!r.ok) throw new Error("saved accounts unavailable"); return r.text(); })
      .then(function (text) {
        if (new TextEncoder().encode(text).length > 1048576) throw new Error("saved accounts bound");
        var result = JSON.parse(text);
        if (!result || result.schemaVersion !== SCHEMA || result.reference !== ref || !result.accounts || !Array.isArray(result.accounts.cards) || result.accounts.cards.length > 64) throw new Error("saved accounts schema");
        if (!current()) return;
        savedAccounts.status = "ready"; savedAccounts.data = result.accounts; state.data.accounts = result.accounts;
      })
      .catch(function () { if (current()) savedAccounts.status = "failed"; })
      .finally(function () {
        clearTimeout(timer);
        if (!current()) return;
        savedAccounts.abort = null;
        if (route().view === "accounts") render(false, true);
      });
  }

  function loadServer() {
    getJSON(SERVER)
      .then(function (d) {
        if (!d || d.schemaVersion !== 1) return;
        var was = changesOn();
        state.server = d;
        renderServed();
        if (changesOn() !== was && state.data) render(false, true);
      })
      .catch(function () { /* the page works without it */ });
  }

  function schedule() {
    clearTimeout(state.timer);
    if (state.paused) return;
    state.timer = setTimeout(load, refreshSeconds() * 1000);
  }

  function load(byHand) {
    if (state.busy) { state.again = true; return; }
    // A hidden tab reads nothing, not even what serves the page: any read
    // keeps the panel from stopping by itself when idle. It reads once when
    // it is shown again.
    if (document.hidden && !byHand) { state.missed = true; return; }
    state.missed = false;
    // What serves the page and whether it allows changes, read again with
    // every read so the footer's ages stay current.
    if (state.data) loadServer();
    state.busy = true;
    var btn = $("refresh");
    btn.setAttribute("aria-busy", "true");
    var asked = state.identifiers;
    getJSON(API + (asked ? "?identifiers=1" : ""))
      .then(function (d) {
        if (!d || d.schemaVersion !== SCHEMA || !d.registry || !Array.isArray(d.loops)) throw new Error("schema");
        // Identifiers were turned on or off while this read was out: the
        // reply is for the other view, so it is dropped and read again.
        if (asked !== state.identifiers) { state.again = true; return; }
        if (savedAccounts.reference !== d.accountsReference && savedAccounts.abort) { savedAccounts.abort.abort(); ++savedAccounts.serial; }
        if (d.accountsReference && savedAccounts.reference === d.accountsReference && savedAccounts.status === "ready") d.accounts = savedAccounts.data;
        state.data = d; state.lastOk = Date.now(); state.failed = false; state.failedSince = 0; state.failedLong = false;
        if (wantsUsage(route())) loadUsage();
        if (render(false) !== false) notice("cut", d.complete === false ? "This read was cut off before every loop was read. Loops that were not read are listed as could not be read; it reads again shortly." : "");
        loadSavedAccounts(true);
      })
      .catch(function (err) {
        state.failed = true;
        if (!state.failedSince) state.failedSince = Date.now();
        // A read can fail for a moment and recover by itself; only reads
        // failing for FAIL_LONG_MS need a person.
        state.failedLong = Date.now() - state.failedSince >= FAIL_LONG_MS;
        var schema = err && err.message === "schema";
        var cmd = "keepgoing " + served();
        var again = served() === "panel" ? "run keepgoing panel again." : "start it again and open the new address it prints.";
        var text = schema ? "This page does not understand the reply it was sent. Reload the page, or restart " + cmd + "." :
          !state.data ? "Could not read from " + cmd + ". It may have stopped; " + again :
          state.failedLong ? "Reads have failed since " + secFmt.format(new Date(state.failedSince)) + ", so what you see is out of date. " + cmd + " may have stopped; if so, " + again :
          "The last read failed, so what you see may be out of date. " + cmd + " may have stopped; it tries again shortly.";
        notice(schema ? "schema" : "failed", text, schema || state.failedLong);
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
      // unmasked notes never stay on screen waiting for a read that is slow or failing.
      render(false, true);
      clearTimeout(state.timer);
      load(true);
    });
    // The skip link moves focus to the details without changing the view.
    document.querySelector("a.skip").addEventListener("click", function (ev) {
      ev.preventDefault();
      focusPaneTitle(true);
    });
    window.addEventListener("hashchange", function () {
      render(true);
      loadSavedAccounts(false);
      if (wantsUsage(route())) loadUsage();
    });
    document.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape" && state.change && state.change.preview && !state.change.busy) { ev.preventDefault(); cancelChange(); }
    });
    document.addEventListener("selectionchange", function () {
      if (state.pending && !selecting()) render(false);
    });
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && !state.paused && (state.missed || (state.lastOk && Date.now() - state.lastOk > 5000))) { clearTimeout(state.timer); load(true); }
    });
    loadServer();
    load(true);
  });
})();
