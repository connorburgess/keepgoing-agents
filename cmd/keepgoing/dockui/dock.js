"use strict";
// keepgoing panel page. Reads one versioned JSON endpoint relative to this
// page and renders it with DOM text nodes only; no data is parsed as HTML.
// Ages come from the report's read time, never this browser's clock, so the
// page reads the same wherever it is opened. Every sentence about a loop or a
// route is either sent by the report or assembled here from its fields and a
// date; nothing is inferred.
(function () {
  var SCHEMA = 1;
  var API = "api/v1/dock";
  var state = { data: null, identifiers: false, paused: false, timer: 0, busy: false, again: false, lastOk: null, failed: false, pending: false, said: "" };

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

  // Time, relative to the report's read time. A date that does not parse is
  // said to be unknown; it never throws.
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
  function ago(seconds) {
    if (seconds == null || isNaN(seconds)) return "at an unknown time";
    if (seconds < -60) return "in the future (check this computer's clock)";
    if (seconds < 45) return "just now";
    return span(seconds) + " ago";
  }
  var timeFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
  var dayTimeFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  var dateFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short" });
  var secFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit", second: "2-digit" });
  function fmt(f, iso, missing) { var t = Date.parse(iso); return isNaN(t) ? (missing || "an unknown time") : f.format(new Date(t)); }
  function clock(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) return "an unknown time";
    return (Math.abs(t - readTime()) < 18 * 3600 * 1000 ? timeFmt : dayTimeFmt).format(new Date(t));
  }
  function day(iso) { return fmt(dateFmt, iso, "an unknown date"); }
  function dayTime(iso) { return fmt(dayTimeFmt, iso); }
  function countdown(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var s = Math.round((t - readTime()) / 1000);
    return s <= 0 ? "passed" : "in " + span(s);
  }
  function until(iso) {
    var c = countdown(iso);
    if (!c) return "at an unknown time";
    return c === "passed" ? clock(iso) + " (passed)" : c + " (" + clock(iso) + ")";
  }
  function agoOf(iso) { var t = Date.parse(iso); return isNaN(t) ? "at an unknown time" : ago(Math.round((readTime() - t) / 1000)); }
  var ORDINALS = ["first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth"];
  function ordinal(n) {
    if (ORDINALS[n - 1]) return ORDINALS[n - 1];
    var r = n % 100, suffix = r >= 11 && r <= 13 ? "th" : ["th", "st", "nd", "rd"][n % 10] || "th";
    return n + suffix;
  }
  function capital(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }

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

  // Routing: "#/" is the overview, "#/loop/<key>" one loop, "#/routes" every route.
  function route() {
    var h = location.hash.replace(/^#\/?/, "");
    if (h === "routes") return { view: "routes" };
    var m = /^loop\/([a-z0-9]+)$/.exec(h);
    if (m) return { view: "loop", key: m[1] };
    return { view: "home" };
  }
  function loopByKey(key) { return list(state.data.loops).filter(function (l) { return l.key === key; })[0]; }
  function needFor(key) { return list(state.data.needsYou).filter(function (n) { return n.key === key; })[0]; }

  // ---- Small pieces shared by the views ----

  function laneCounts(c) {
    return [[c.working, "working"], [c.stuck, "waiting for an answer"], [c.failed, "failed"]]
      .filter(function (x) { return x[0]; }).map(function (x) { return x[0] + " " + x[1]; }).join(", ");
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
    if (n.availability === "known" && n.at) return document.createTextNode(capital(until(n.at)));
    if (n.availability === "in_progress") return document.createTextNode(n.phrase);
    return joinParts([unknown("Unknown"), n.detail ? el("span", "sub", n.detail) : null]);
  }
  function limits(items, wait) {
    var f = frag();
    if (wait) {
      var w = el("div");
      w.appendChild(document.createTextNode((wait.passed ? "Provider limit reset " : "Provider limit resets ") + (wait.resetsAt ? until(wait.resetsAt) : "at an unknown time")));
      if (!wait.passed) w.appendChild(tag("reached", "Reached"));
      f.appendChild(w);
    }
    list(items).forEach(function (l) {
      var row = el("div");
      if (l.label) row.appendChild(el("span", "sub", l.label + ": "));
      row.appendChild(l.unknown ? unknown(l.phrase) : document.createTextNode(l.label === "Deadline" ? dayTime(l.phrase) : l.phrase));
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
      if (w.resetsAt) row.appendChild(el("span", "meter-reset", w.stale ? "That window ended " + clock(w.resetsAt) : "Resets " + until(w.resetsAt)));
      f.appendChild(row);
    });
    return f;
  }
  function usageBlock(u, label) {
    var box = el("div", "route-usage");
    if (u.availability === "unavailable") {
      box.appendChild(el("div", "sub unknown usage-none", "No usage reading" + (u.reason ? ": " + u.reason : "")));
      return box;
    }
    var uh = el("div", "sub usage-read");
    uh.appendChild(document.createTextNode((label || "Usage read ") + ago(u.ageSeconds) + (u.fromAnotherLoop ? ", by another loop on this route" : "")));
    if (u.availability === "stale") uh.appendChild(tag("stale", "Stale"));
    box.appendChild(uh);
    box.appendChild(meters(u));
    return box;
  }
  // The first window that is used up, as a sentence: the reading's own facts.
  function usedUp(u) {
    var w = list(u.windows).filter(function (x) { return !x.stale && x.usedPercent >= 100; })[0];
    if (!w) return "";
    return capital(w.name) + " allowance used up when read " + ago(u.ageSeconds) + (w.resetsAt ? "; resets " + until(w.resetsAt) : "; reset time not reported");
  }

  // A route's title says what is known: its model, or the tool and that no
  // model is set. The account group joins the title only when it is a group
  // of its own (not the provider's name) and repeats no word already there;
  // it is always on the detail line.
  function routeName(e) {
    if (e.model) return e.model;
    if (e.tool === "BB thread") return "BB thread · model not set";
    return e.tool ? e.tool + " · default model" : "Model not set";
  }
  function titleAccount(e) {
    if (!e.account || e.account === e.provider) return "";
    var words = routeName(e).toLowerCase().split(/[^a-z0-9]+/);
    var repeats = e.account.toLowerCase().split(/[^a-z0-9]+/).some(function (w) { return w && words.indexOf(w) >= 0; });
    return repeats ? "" : e.account;
  }
  var ENTER = {
    first: "the loop's first route", same_conversation: "continues in the same conversation", other_conversation: "moves to another conversation",
    new_conversation: "starts a new conversation", headless: "runs without a window", command: "runs a command"
  };
  function routeMeta(e) {
    return [e.model ? e.tool : "", e.account ? "account group " + e.account : "", e.provider && e.provider !== e.account ? "provider " + e.provider : "",
      e.effort ? "effort " + e.effort : "effort not set", e.enter && e.enter !== "first" ? ENTER[e.enter] : "",
      state.identifiers && e.id ? "named " + e.id + " in its configuration" : ""];
  }

  // What a loop's state records about a route it moved off: when, why, the
  // reset the provider reported, and what is recorded about going back.
  function limitSentence(a, who) {
    var s = who ? who + " " : "";
    s += a.reason ? (who ? "hit " : "Hit ") + a.reason + " here" : (who ? "moved off this route" : "The loop moved off this route");
    s += (a.since ? " on " + day(a.since) : "") + (a.reason ? (who ? " and moved on" : " and the loop moved on") : "; the reason is not recorded") + ". ";
    if (!a.resetsAt) s += "The provider reported no reset time";
    else if (a.resetPassed) s += "The reset the provider reported, " + dayTime(a.resetsAt) + ", has passed";
    else s += "The provider reported a reset for " + dayTime(a.resetsAt) + " (" + countdown(a.resetsAt) + ")";
    if (a.return === "off") s += "; automatic return is off";
    else if (a.return === "stopped") s += "; automatic return has stopped after refused attempts";
    else if (a.return === "on" && a.resetsAt && !a.resetPassed) s += "; keepgoing returns after that, once a test passes";
    else if (a.return === "on" && a.lastReturnTry) s += "; a return was last tried " + agoOf(a.lastReturnTry);
    else if (a.return === "on") s += "; no return has been tried yet";
    return s + ".";
  }
  function atLimit(a) { return a.reason === "a usage limit" && !a.resetPassed; }
  function awayShort(a) {
    var s = "On its " + ordinal(a.choice) + " choice" + (a.since ? " since " + day(a.since) : "");
    var why = a.reason ? "first choice left on " + a.reason : "first choice left, reason not recorded";
    if (a.reason === "a usage limit") {
      why = !a.resetsAt ? "first choice hit a usage limit; no reset time reported" :
        a.resetPassed ? "first choice hit a usage limit; its reported reset, " + day(a.resetsAt) + ", has passed" : "first choice at a usage limit until " + day(a.resetsAt);
    }
    return [s, why];
  }

  // The route component: one route (a model on an account at an effort
  // level). It leads with the worst recorded fact, then readiness as a
  // sentence tied to its test, then what the route is. A loop's routes and
  // the view across loops both render it.
  var READY_TONE = { ready: "good", ready_overdue: "calm", not_ready: "warn", not_probed: "unknown", disabled: "calm", unknown: "unknown" };
  function routeItem(o) {
    var li = el("li", "route" + (o.active ? " is-active" : "") + (o.off ? " is-off" : ""));
    if (o.anchor) li.id = o.anchor;
    if (o.id) li.dataset.id = o.id;
    if (o.position != null) li.dataset.position = String(o.position);
    if (o.badge) {
      var pos = el("span", "pos", o.badge);
      pos.setAttribute("aria-hidden", "true");
      li.appendChild(pos);
    }
    var body = el("div", "route-body");
    var top = el("div", "route-top");
    var name = el("h3", "route-name", o.name);
    if (o.account) name.appendChild(el("span", "route-account", " on " + o.account));
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
    meta.appendChild(joinParts(o.meta));
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
    if (n.kind === "unreadable") head.appendChild(el("span", "waited", "Could not be read"));
    else head.appendChild(el("span", "waited", n.waitedSeconds == null ? "Waiting time unknown" : (n.sinceEvidence === "hold_recorded" ? "Waiting " : "Waiting at least ") + span(n.waitedSeconds)));
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
  function needsLine(d) {
    if (d.registry.availability !== "available") return "Needs you: unknown";
    var needs = list(d.needsYou).length, held = needs - (d.unread || 0);
    if (!needs) return d.allClear ? "Nothing needs you" : "Needs you: not fully read";
    var words = [];
    if (held) words.push(held + (held === 1 ? " loop needs you" : " loops need you"));
    if (d.unread) words.push(d.unread + " could not be read");
    return words.join(", ");
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
    var line = needsLine(d);
    top.appendChild(el("span", "pick-needs-text", needs ? line.replace(/^\d+ /, "") : line));
    if (needs) top.setAttribute("aria-label", line);
    top.appendChild(icon("chevron"));
    fixed.appendChild(top);
    var routes = list(d.routes), trouble = routes.filter(function (x) { return x.attention; }).length;
    fixed.appendChild(pickRow("#/routes", trouble ? "dot-limit" : "dot-calm", "Routes",
      routes.length + (routes.length === 1 ? " route" : " routes") + (trouble ? ", " + trouble + " at a limit or not ready" : ""), r.view === "routes"));
    nav.appendChild(fixed);

    var label = el("p", "pick-label", "Loops");
    label.id = "pick-loops";
    nav.appendChild(label);
    var scroll = el("div", "pick-scroll");
    var ul = el("ul", "pick-list");
    ul.setAttribute("aria-labelledby", "pick-loops");
    list(d.loops).forEach(function (l) {
      var li = el("li");
      li.appendChild(pickRow("#/loop/" + l.key, "dot-" + (TONES[l.state] || "unknown"), l.name,
        l.phrase + (l.away ? " · on its " + ordinal(l.away.choice) + " choice" : ""), r.view === "loop" && r.key === l.key));
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
    var loops = list(d.loops), needs = list(d.needsYou);
    var n = loops.length, sup = loops.filter(function (l) { return l.state === "supervising"; }).length;
    pane.appendChild(el("p", "lede", n + (n === 1 ? " loop" : " loops") + (sup ? ", " + sup + " supervising" : "") + (d.unread ? ", " + d.unread + " could not be read" : "") +
      ". Read from keepgoing's own records; nothing here is a live view of a conversation."));
    if (!n) {
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
    var quiet = { supervising: 1, paused: 1, off: 1, stopped: 1, finished: 1, waiting_for_you: 1, unknown: 1 };
    var worth = loops.filter(function (l) { return !quiet[l.state] || l.away; });
    if (worth.length) {
      var sec = panel("Worth knowing", [el("span", "sub", "not waiting on you")]);
      var ul = el("ul", "worth");
      worth.forEach(function (l) {
        var li = el("li");
        li.appendChild(el("span", "dot dot-" + (TONES[l.state] || "unknown")));
        var t = el("div");
        t.appendChild(link("#/loop/" + l.key, l.name));
        var parts = [];
        if (!quiet[l.state]) parts.push(l.detail);
        if (l.away) { var aw = awayShort(l.away); parts.push(aw[0] + ": " + aw[1] + "."); }
        t.appendChild(el("span", "sub", parts.join(" ")));
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
    else card("Last decision", dec.phrase, ago(dec.ageSeconds) + (dec.since ? " · this result since " + clock(dec.since) : ""));
    var h = l.hold;
    if (h.state === "unknown") card("Hold", unknown());
    else if (h.state === "none") card("Hold", "None");
    else card("Hold", h.phrase, h.sinceSeconds == null ? "since an unknown time" : (h.sinceEvidence === "hold_recorded" ? "for " : "for at least ") + span(h.sinceSeconds));
    if (l.away) { var aw = awayShort(l.away); card("Route", aw[0], capital(aw[1])); }
    return dl;
  }

  // Each lane of the last verified listing: its state, then what it is (its
  // title when one is recorded, otherwise its number).
  function laneBox(l, inUse) {
    var c = l.children, lanes = l.lanes, rows = list(lanes && lanes.lanes);
    var b = el("div", "lanes is-wide");
    var head = el("span", "lanes-label", "This loop's lanes");
    b.appendChild(head);
    if (!rows.length) {
      b.appendChild(c.availability === "unknown" ? unknown() : el("span", "sub", c.availability === "stale" ? "Stale: the last listing is too old to show as current" : c.phrase));
      return b;
    }
    head.textContent = "This loop's lanes · " + laneCounts(c) + " · " + (c.availability === "held" ? "as of the check that held it, " : "listed ") + ago(c.ageSeconds);
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
      if (x.key) {
        what.appendChild(document.createTextNode(" · "));
        what.appendChild(el("code", null, x.key));
      }
      li.appendChild(what);
      ul.appendChild(li);
    });
    b.appendChild(ul);
    if (lanes.omitted) b.appendChild(el("p", "more", lanes.omitted + " more not listed"));
    b.appendChild(el("p", "more", "keepgoing records each lane's state, not its title or which route it is on" + (inUse ? "; they are listed under the loop's current route." : ".")));
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
      var t = el("time", null, clock(x.at));
      t.dateTime = x.at;
      li.appendChild(t);
      li.appendChild(el("span", "mark"));
      var body = el("div", null, x.phrase);
      if (x.detail) body.appendChild(el("span", "sub", x.detail));
      if (x.folded) body.appendChild(el("span", "sub", x.folded + (x.folded === 1 ? " more check" : " more checks") + " with this result since " + clock(x.foldedSince)));
      li.appendChild(body);
      ol.appendChild(li);
    });
    p.appendChild(ol);
    var more = el("p", "more", events.length + " of " + ev.retained + (ev.retained === 1 ? " retained entry." : " retained entries.") + (ev.pruned ? " Older history has been pruned." : "") + " More with ");
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
    var c = l.chain, f = l.fallbacks, entries = list(c.entries);
    var brief;
    if (c.availability === "none_configured") brief = el("span", "sub", "No other routes set up");
    else if (f.availability !== "known") brief = unknown("Readiness of other routes unknown");
    else if (!f.ready) brief = el("span", "chain-ready is-short", f.eligible ? "No other route is ready" : "No other route is eligible to take over");
    else brief = el("span", "chain-ready" + (f.enough ? "" : " is-short"), f.ready + " of " + f.eligible + " other routes ready");
    var routing = panel("Preferred routes", [brief]);
    var count = Math.max(0, entries.length - 1) + (c.omitted || 0);
    var on = list(c.handoffOn);
    var how = !entries.length ? "Unknown: its configuration could not be read." :
      c.availability === "none_configured" ? "Nothing takes over if the first route cannot continue." :
      on.length ? "This loop's own list, in order of preference. It moves automatically on " + on.map(function (x) { return x.replace(/_/g, " ") + "s"; }).join(", ") + ", to the first ready route below." :
      "This loop's own list, in order of preference. Automatic moves are off; " + count + (count === 1 ? " other route is" : " other routes are") + " set up for a manual move.";
    routing.appendChild(el("p", "sub chain-how", how));
    var ol = el("ol", "route-list is-ordered");
    entries.forEach(function (e) {
      var notes = [];
      if (e.position === 0 && l.away) notes.push({ tone: atLimit(l.away) ? "limit" : "plain", text: limitSentence(l.away) });
      var full = usedUp(e.usage);
      if (full && !(e.position === 0 && l.away && atLimit(l.away))) notes.push({ tone: "limit", text: full + "." });
      var after = [];
      if (e.probeNote) after.push(e.probeNote + ".");
      if (e.otherTest) after.push("Another loop's newer test of this route: " + e.otherTest.charAt(0).toLowerCase() + e.otherTest.slice(1) + ".");
      var item = routeItem({
        id: e.id, position: e.position, badge: String(e.position + 1), name: routeName(e), account: titleAccount(e), active: e.inUse, use: e.use, off: e.disabled,
        readiness: e.position > 0 && e.readiness !== "current" ? e.readiness : "", phrase: e.phrase, notes: notes, after: after,
        meta: routeMeta(e), usage: e.usage, boxes: e.current ? [laneBox(l, true)] : []
      });
      item.setAttribute("aria-label", capital(ordinal(e.position + 1)) + " choice: " + routeName(e) + (e.use ? ", " + e.use : ""));
      ol.appendChild(item);
    });
    routing.appendChild(ol);
    if (c.omitted) routing.appendChild(el("p", "sub", c.omitted + " more not shown"));
    if (!entries.some(function (e) { return e.current; })) routing.appendChild(laneBox(l, false));
    pane.appendChild(routing);

    pane.appendChild(eventsPanel(l.events));

    var checks = panel("Checks and limits");
    var dl = el("dl", "facts");
    dl.appendChild(fact("Last check", lastCheck(l.lastCheck)));
    dl.appendChild(fact("Next check", nextCheck(l.nextCheck)));
    dl.appendChild(fact("Limits", limits(l.limits, l.limitWait)));
    dl.appendChild(fact("Device", joinParts([l.device.hostname ? l.device.hostname + " (" + l.device.name.toLowerCase() + ")" : l.device.name, l.device.role])));
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
  // One line per account group: the newest recorded reading and its resets.
  function allowancesPanel(items) {
    var p = panel("Allowances", [el("span", "sub", "the newest reading recorded for each account group; not a live reading")]);
    if (!items.length) { p.appendChild(el("p", "sub chain-how", "No account groups to show.")); return p; }
    var ul = el("ul", "allow-list");
    items.forEach(function (a) {
      var li = el("li", "allow");
      var head = el("div", "allow-head");
      head.appendChild(el("h3", "allow-name", a.account || a.provider || "Unnamed account group"));
      head.appendChild(el("span", "sub", [a.provider && a.provider !== a.account ? "provider " + a.provider : "", a.routes + (a.routes === 1 ? " route" : " routes")].filter(Boolean).join(" · ")));
      li.appendChild(head);
      var body = el("div", "allow-body");
      if (a.limit) {
        var who = a.limit.loop || "A loop";
        body.appendChild(el("p", "route-note is-limit", who + " hit a usage limit on a route in this group" + (a.limit.since ? " on " + day(a.limit.since) : "") + "; " +
          (a.limit.resetsAt ? "the provider reported a reset for " + dayTime(a.limit.resetsAt) + " (" + countdown(a.limit.resetsAt) + ")." : "the provider reported no reset time.")));
      }
      var u = a.usage;
      var full = a.limit ? "" : usedUp(u);
      if (full) body.appendChild(el("p", "route-note is-limit", full + "."));
      if (u.availability === "unavailable") {
        body.appendChild(el("div", "sub unknown usage-none", "No reading"));
      } else {
        var blk = usageBlock(u, "Read ");
        body.appendChild(blk);
        var future = list(u.windows).filter(function (w) { return !w.stale && w.resetsAt && countdown(w.resetsAt) !== "passed"; });
        if (future.length > 1) {
          var next = future.map(function (w) { return w.resetsAt; }).sort(function (x, y) { return Date.parse(x) - Date.parse(y); })[0];
          blk.appendChild(el("div", "sub allow-next", "Next reset " + until(next)));
        }
      }
      li.appendChild(body);
      ul.appendChild(li);
    });
    p.appendChild(ul);
    return p;
  }

  function renderRoutes(d, pane) {
    pane.appendChild(back());
    pane.appendChild(paneHead("Routes"));
    pane.appendChild(el("p", "lede", "Each model on an account that a loop runs on or keeps on standby, as configured. keepgoing does not verify which account a thread is using."));
    var routes = list(d.routes);
    if (!routes.length) {
      pane.appendChild(emptyBox(d.registry.availability === "available" ? "No routes to show." : "Unknown: the registry could not be read.", ""));
      return;
    }
    var inUse = routes.filter(function (n) { return list(n.runningNow).length > 0; }).length;
    var trouble = routes.filter(function (n) { return n.attention; });
    var sum = el("p", "route-summary");
    sum.appendChild(el("span", null, routes.length + (routes.length === 1 ? " route" : " routes")));
    sum.appendChild(el("span", "c-good", inUse + " in use"));
    if (trouble.length) sum.appendChild(link("#" + trouble[0].key, trouble.length + " at a limit or not ready, listed first", "c-warn"));
    else sum.appendChild(el("span", "sub", "none at a limit or failing its test"));
    pane.appendChild(sum);
    pane.appendChild(allowancesPanel(list(d.allowances)));

    var wrap = panel("All routes", [el("span", "sub", "those at a limit or not ready first")]);
    var ul = el("ul", "route-list");
    routes.forEach(function (n) {
      // Loops on the route, one per line with their state.
      function loopLines(items) {
        var u = el("ul");
        items.forEach(function (x) {
          var li = el("li");
          li.appendChild(link("#/loop/" + x.key, x.loop));
          li.appendChild(el("span", "sub", " · " + x.phrase + (x.lanesWorking ? " · " + x.lanesWorking + (x.lanesWorking === 1 ? " lane" : " lanes") + " working" : "")));
          u.appendChild(li);
        });
        return u;
      }
      // Loops that only keep it on standby, grouped by which choice it is for
      // them, since the order is the point of preferred routes.
      function byChoice(items) {
        var w = el("div"), by = {};
        items.forEach(function (x) { (by[x.position] = by[x.position] || []).push(x); });
        Object.keys(by).map(Number).sort(function (a, b) { return a - b; }).forEach(function (pos) {
          var p = el("p", "standby", capital(ordinal(pos + 1)) + " choice for ");
          by[pos].forEach(function (x, i) {
            if (i) p.appendChild(document.createTextNode(", "));
            p.appendChild(link("#/loop/" + x.key, x.loop));
          });
          w.appendChild(p);
        });
        return w;
      }
      var running = list(n.runningNow), recorded = list(n.recordedFor), standby = list(n.standbyFor), unk = list(n.unknownFor);
      var boxes = [];
      if (running.length) boxes.push(box("Running on it now", loopLines(running)));
      if (recorded.length) boxes.push(box("Their current route, but not running now", loopLines(recorded)));
      if (standby.length) boxes.push(box("On standby", byChoice(standby)));
      if (unk.length) boxes.push(box("Listed by loops whose current route could not be read", byChoice(unk)));
      if (n.omitted) boxes.push(el("p", "sub", n.omitted + " more loops not shown"));
      var notes = [];
      list(n.leftBy).forEach(function (x) { notes.push({ tone: atLimit(x) ? "limit" : "plain", text: limitSentence(x, x.loop) }); });
      var full = list(n.leftBy).some(atLimit) ? "" : usedUp(n.usage);
      if (full) notes.push({ tone: "limit", text: full + "." });
      if (n.sharedLimit) {
        notes.push({ tone: "limit", text: "Shares its account group with a route where " + (n.sharedLimit.loop || "a loop") + " hit a usage limit" +
          (n.sharedLimit.since ? " on " + day(n.sharedLimit.since) : "") + (n.sharedLimit.resetsAt ? ", reported to reset " + day(n.sharedLimit.resetsAt) : ", with no reset time reported") +
          ". The test below is this route's own." });
      }
      var hide = n.readiness === "current" || n.readiness === "main" || (n.readiness === "unknown" && n.probeAgeSeconds == null);
      ul.appendChild(routeItem({
        anchor: n.key, name: routeName(n), account: titleAccount(n), active: running.length > 0,
        use: running.length ? running.length + (running.length === 1 ? " loop on it now" : " loops on it now") : "",
        readiness: hide ? "" : n.readiness, phrase: n.phrase, notes: notes, meta: routeMeta(n), usage: n.usage, boxes: boxes
      }));
    });
    wrap.appendChild(ul);
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
  function focusKey(node) {
    if (!node || node === document.body) return null;
    if (node.id) return "#" + node.id;
    if (node.tagName === "A" && node.getAttribute("href")) return 'a[href="' + node.getAttribute("href").replace(/"/g, '\\"') + '"]';
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
    var active = document.activeElement, key = focusKey(active), inPane = pane.contains(active), inPicker = $("picker").contains(active);
    try {
      $("layout").dataset.view = r.view;
      renderPicker(d, r);
      pane.replaceChildren();
      pane.setAttribute("aria-labelledby", "pane-title");
      if (r.view === "loop") renderLoop(d, pane, r.key);
      else if (r.view === "routes") renderRoutes(d, pane);
      else renderHome(d, pane);
    } catch (err) {
      pane.replaceChildren(emptyBox("This page could not show the reply it was sent.", "Reload the page, or restart keepgoing dock."));
      banner("This page could not show the reply it was sent. Reload the page, or restart keepgoing dock.");
    }
    renderReadline();
    var needs = list(d.needsYou).length;
    document.title = (needs ? "(" + needs + ") " : "") + "keepgoing";
    announce(needsLine(d) + ".");
    window.scrollTo(0, top);
    if (navigated) {
      pane.focus({ preventScroll: true });
      var cur = $("picker").querySelector('.pick-scroll [aria-current="page"]');
      if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
    } else if (key && (inPane || inPicker)) {
      // Keep the keyboard where it was: the same link or button, rebuilt.
      var again = (inPane ? pane : $("picker")).querySelector(key) || document.querySelector(key);
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
        var text = schema ? "This page does not understand the reply it was sent. Reload the page, or restart keepgoing dock." :
          state.data ? "The last read failed, so what you see may be out of date. keepgoing dock may have stopped; it tries again shortly." :
          "Could not read from keepgoing dock. It may have stopped; start it again and open the new address it prints.";
        banner(text);
        announce(text);
        if (!state.data) {
          $("picker").replaceChildren(el("p", "placeholder", "Nothing could be read."));
          $("pane").replaceChildren(paneHead("Could not read"), emptyBox("Nothing could be read from keepgoing dock.", "It may have stopped. Start it again and open the new address it prints."));
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
    load(true);
  });
})();
