"use strict";
// keepgoing dock page. Reads one versioned JSON endpoint relative to this
// page and renders it with DOM text nodes only; no data is parsed as HTML.
// Ages come from the server's read time, never this browser's clock, so the
// page reads the same wherever it is opened.
(function () {
  var SCHEMA = 1;
  var API = "api/v1/dock";
  var SERVER = "api/v1/server";
  var state = { data: null, identifiers: false, timer: 0, busy: false, lastOk: null, failed: false, route: "" };

  var TONES = {
    supervising: "good", waiting_for_you: "attn", waiting_on_limit: "limit", checks_failing: "warn",
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
    chevron: '<path d="m7.5 5 5 5-5 5"/>',
    route: '<rect x="4" y="4" width="12" height="12" rx="3"/><circle cx="10" cy="10" r="1.6" fill="currentColor" stroke="none"/>'
  };
  var STATE_ICON = {
    supervising: "good", waiting_for_you: "attn", waiting_on_limit: "limit", checks_failing: "warn", not_checked_recently: "warn",
    outside_hours: "limit", paused: "paused", stopped: "stopped", finished: "finished", off: "off", unknown: "unknown"
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

  var READY_TONE = { ready: "good", not_ready: "warn", not_probed: "unknown", disabled: "calm", unknown: "unknown" };

  // Routing: "#/" is the overview, "#/loop/<key>" one loop, "#/routes" every route.
  function route() {
    var h = location.hash.replace(/^#\/?/, "");
    if (h === "routes") return { view: "routes" };
    var m = /^loop\/([a-z0-9]+)$/.exec(h);
    if (m) return { view: "loop", key: m[1] };
    return { view: "home" };
  }
  function loopByKey(key) { return state.data.loops.filter(function (l) { return l.key === key; })[0]; }
  function needFor(key) { return state.data.needsYou.filter(function (n) { return n.key === key; })[0]; }

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


  // The route component. One route (a model on an account): a badge, a name, whether it is in
  // use, its readiness, what it is, its usage and reset, and what runs on it.
  // A loop's routing and the all-loops route view both render it.
  function routeItem(o) {
    var li = el("li", "route" + (o.active ? " is-active" : "") + (o.off ? " is-off" : ""));
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
    if (o.readiness) top.appendChild(el("span", "ready tone-" + (READY_TONE[o.readiness] || "unknown"), o.phrase));
    body.appendChild(top);
    var meta = el("div", "sub route-meta");
    meta.appendChild(joinParts(o.meta.concat(o.probeAgeSeconds != null ? ["last probed " + ago(o.probeAgeSeconds)] : [])));
    body.appendChild(meta);
    var usage = el("div", "route-usage");
    var u = o.usage;
    if (u.availability === "unavailable") {
      usage.appendChild(el("div", "sub unknown usage-none", "No usage reading"));
    } else {
      var uh = el("div", "sub usage-read");
      uh.appendChild(document.createTextNode("Usage read " + ago(u.ageSeconds)));
      if (u.availability === "stale") uh.appendChild(tag("stale", "Stale"));
      usage.appendChild(uh);
      usage.appendChild(meters(u));
    }
    body.appendChild(usage);
    (o.lanes || []).forEach(function (l) { body.appendChild(l); });
    li.appendChild(body);
    return li;
  }
  function lanesBox(label, content) {
    var box = el("div", "lanes");
    box.appendChild(el("span", "lanes-label", label));
    box.appendChild(content);
    return box;
  }
  function routeName(e) { return e.model || (e.tool === "BB thread" ? "The thread's own model" : e.tool || "Unknown model"); }
  function routeMeta(e) { return [e.model ? e.tool : "", e.provider !== e.account ? e.provider : ""]; }

  function needCard(n, link) {
    var li = el("li", "need");
    var head = el("div", "need-head");
    if (link) head.appendChild(el("h3", null, n.loop));
    if (n.kind === "unreadable") head.appendChild(el("span", "waited", "Could not be read"));
    else head.appendChild(el("span", "waited", n.waitedSeconds == null ? "Waiting time unknown" : (n.sinceEvidence === "hold_recorded" ? "Waiting " : "Waiting at least ") + span(n.waitedSeconds)));
    li.appendChild(head);
    li.appendChild(el("p", "reason", n.reason));
    if (n.note && state.identifiers) {
      var note = el("div", "note");
      note.appendChild(el("span", "note-label", "Note left with the hold"));
      note.appendChild(document.createTextNode("\u201C" + n.note + "\u201D"));
      li.appendChild(note);
    } else if (n.noteWithheld || n.note) {
      li.appendChild(el("p", "sub", "A note was left with the hold. It is shown with identifiers and notes."));
    }
    var todo = el("p", "todo");
    todo.appendChild(el("span", "todo-label", "What to do"));
    todo.appendChild(document.createTextNode(n.action));
    li.appendChild(todo);
    if (link) {
      var a = el("a", "jump", "Open " + n.loop);
      a.href = "#/loop/" + n.key;
      li.appendChild(a);
    }
    return li;
  }

  // Level 1: the picker.
  function renderPicker(d, r) {
    var nav = $("picker");
    nav.replaceChildren();
    var needs = el("a", "pick pick-needs" + (d.needsYou.length ? "" : " is-clear"));
    needs.setAttribute("aria-label", "Needs you");
    needs.href = "#/";
    if (r.view === "home") needs.setAttribute("aria-current", "page");
    var held = d.needsYou.length - d.unread;
    if (d.registry.availability !== "available") {
      needs.appendChild(icon("unknown"));
      needs.appendChild(el("span", null, "Needs you: unknown"));
    } else if (d.needsYou.length) {
      needs.appendChild(el("span", "count", String(d.needsYou.length)));
      var words = [];
      if (held) words.push(held === 1 ? "loop needs you" : "loops need you");
      if (d.unread) words.push((held ? d.unread + " " : "") + "could not be read");
      needs.appendChild(el("span", null, words.join(", ")));
    } else if (d.allClear) {
      needs.appendChild(icon("check"));
      needs.appendChild(el("span", null, "Nothing needs you"));
    } else {
      // Not every loop was read, so an all-clear cannot be given.
      needs.appendChild(icon("unknown"));
      needs.appendChild(el("span", null, "Needs you: not fully read"));
    }
    needs.appendChild(icon("chevron"));
    nav.appendChild(needs);

    var group = el("div");
    var label = el("p", "pick-label", "Loops");
    label.id = "pick-loops";
    group.appendChild(label);
    var ul = el("ul", "pick-list");
    ul.setAttribute("aria-labelledby", "pick-loops");
    d.loops.forEach(function (l) {
      var li = el("li");
      var a = el("a", "pick");
      a.href = "#/loop/" + l.key;
      if (r.view === "loop" && r.key === l.key) a.setAttribute("aria-current", "page");
      a.appendChild(el("span", "dot dot-" + (TONES[l.state] || "unknown")));
      a.appendChild(el("span", "pick-name", l.name));
      a.appendChild(el("span", "pick-state", l.phrase));
      var go = icon("chevron");
      go.setAttribute("class", "go");
      a.appendChild(go);
      li.appendChild(a);
      ul.appendChild(li);
    });
    if (!d.loops.length) ul.appendChild(el("li", "sub pick-state", d.registry.availability === "available" ? "No loops registered yet" : "Unknown"));
    group.appendChild(ul);
    if (d.registry.omitted) group.appendChild(el("p", "sub pick-label", d.registry.omitted + " more not shown"));
    nav.appendChild(group);

    var views = el("div");
    var vl = el("p", "pick-label", "Across loops");
    views.appendChild(vl);
    var routes = el("a", "pick");
    routes.href = "#/routes";
    if (r.view === "routes") routes.setAttribute("aria-current", "page");
    routes.appendChild(el("span", "dot dot-calm"));
    routes.appendChild(el("span", "pick-name", "Routes"));
    routes.appendChild(el("span", "pick-state", d.routes.length + (d.routes.length === 1 ? " model on an account" : " models and accounts")));
    var go = icon("chevron");
    go.setAttribute("class", "go");
    routes.appendChild(go);
    views.appendChild(routes);
    nav.appendChild(views);
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
    var a = el("a", "back");
    a.href = "#/";
    a.appendChild(icon("chevron"));
    a.appendChild(document.createTextNode("All loops"));
    return a;
  }
  function panel(title, extra) {
    var p = el("section", "panel");
    var h = el("h2", null, title);
    (extra || []).forEach(function (x) { if (x) h.appendChild(x); });
    p.appendChild(h);
    return p;
  }
  function emptyBox(title, body) {
    var box = el("div", "empty");
    box.appendChild(el("p", null, title));
    if (body) box.appendChild(el("p", "sub", body));
    return box;
  }

  // Overview pane: what needs you, in full.
  function renderHome(d, pane) {
    pane.appendChild(paneHead("Needs you"));
    if (d.registry.availability !== "available") {
      pane.appendChild(el("p", "lede", "Unknown."));
      pane.appendChild(emptyBox("The registry could not be read.", "Nothing is shown rather than guessing. Check it with keepgoing registry list."));
      return;
    }
    var n = d.loops.length, sup = d.loops.filter(function (l) { return l.state === "supervising"; }).length;
    pane.appendChild(el("p", "lede", n + (n === 1 ? " loop" : " loops") + (sup ? ", " + sup + " supervising" : "") + (d.unread ? ", " + d.unread + " could not be read" : "") + ". Read from keepgoing's own records; nothing here is a live view of a conversation."));
    if (!n) {
      pane.appendChild(emptyBox("No loops are registered yet.", "Register one with keepgoing registry add -c path/to/keepgoing.yaml, and it appears here on the next read."));
      return;
    }
    if (!d.needsYou.length && !d.allClear) {
      pane.appendChild(emptyBox("Not every loop was read.", "So this cannot say that nothing needs you. It reads again shortly."));
      return;
    }
    if (!d.needsYou.length) {
      var p = el("p", "clear panel");
      p.appendChild(icon("check"));
      p.appendChild(document.createTextNode("Nothing needs you: every registered loop was read and none is held. Pick a loop to see its routes."));
      pane.appendChild(p);
      return;
    }
    var list = el("ol", "need-list");
    d.needsYou.forEach(function (x) { list.appendChild(needCard(x, true)); });
    var box = el("div", "panel is-need");
    box.appendChild(list);
    pane.appendChild(box);
  }

  // Level 2: one loop, shown through its routes.
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

    // The ordered preferred routes are the main structure.
    var c = l.chain, f = l.fallbacks;
    var brief = null;
    if (c.availability === "none_configured") brief = el("span", "sub", "No other routes set up");
    else if (f.availability === "known") brief = el("span", "chain-ready" + (f.eligible > 0 && !f.enough ? " is-short" : ""), f.eligible ? f.ready + " of " + f.eligible + " other routes ready" : "No other route is eligible to take over");
    else brief = unknown("Readiness of other routes unknown");
    var routing = panel("Preferred routes", [brief]);
    var count = Math.max(0, c.entries.length - 1) + c.omitted;
    var how = !c.entries.length ? "Unknown: its configuration could not be read." :
      c.availability === "none_configured" ? "Nothing takes over if the first route cannot continue." :
      c.handoffOn.length ? "In order of preference. Moves automatically on " + c.handoffOn.map(function (x) { return x.replace(/_/g, " ") + "s"; }).join(", ") + ", to the first ready route below." :
      "In order of preference. Automatic moves are off; " + count + (count === 1 ? " other route is" : " other routes are") + " set up for a manual move.";
    routing.appendChild(el("p", "sub chain-how", how));
    var ol = el("ol", "route-list is-ordered");
    c.entries.forEach(function (e) {
      var lanes = [];
      if (e.current) lanes.push(lanesBox(e.inUse ? "Lanes on this route" : "Lanes, as last listed", children(l.children)));
      var item = routeItem({
        id: e.id, position: e.position, badge: String(e.position + 1), name: routeName(e), account: e.account, active: e.inUse, use: e.use, off: e.disabled,
        readiness: e.position > 0 && e.readiness !== "current" ? e.readiness : "", phrase: e.phrase,
        meta: ["configured as " + e.id].concat(routeMeta(e)), probeAgeSeconds: e.probeAgeSeconds, usage: e.usage, lanes: lanes
      });
      item.setAttribute("aria-label", "Route " + (e.position + 1) + ": " + routeName(e) + (e.use ? ", " + e.use : ""));
      ol.appendChild(item);
    });
    routing.appendChild(ol);
    if (c.omitted) routing.appendChild(el("p", "sub", c.omitted + " more not shown"));
    if (!c.entries.length || !c.entries.some(function (e) { return e.current; })) {
      routing.appendChild(lanesBox("Lanes", children(l.children)));
    }
    pane.appendChild(routing);

    var checks = panel("Checks and limits");
    var dl = el("dl", "facts");
    dl.appendChild(fact("Last check", lastCheck(l.lastCheck)));
    dl.appendChild(fact("Next check", nextCheck(l.nextCheck)));
    dl.appendChild(fact("Limits", limits(l.limits, l.limitWait)));
    checks.appendChild(dl);
    if (l.issues.length) {
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

  // The same route component, summed across loops.
  function renderRoutes(d, pane) {
    pane.appendChild(back());
    pane.appendChild(paneHead("Routes"));
    pane.appendChild(el("p", "lede", "Each model on an account that a loop runs on or keeps on standby, as configured. keepgoing does not verify which account a thread is using."));
    if (!d.routes.length) {
      pane.appendChild(emptyBox(d.registry.availability === "available" ? "No routes to show." : "Unknown: the registry could not be read.", ""));
      return;
    }
    var inUse = d.routes.filter(function (n) { return n.runningNow.length > 0; }).length;
    var trouble = d.routes.filter(function (n) {
      return n.readiness === "not_ready" || n.usage.windows.some(function (w) { return !w.stale && w.usedPercent >= 90; });
    }).length;
    var sum = el("p", "route-summary");
    sum.appendChild(el("span", null, d.routes.length + (d.routes.length === 1 ? " route" : " routes")));
    sum.appendChild(el("span", "c-good", inUse + " in use"));
    sum.appendChild(el("span", trouble ? "c-warn" : "sub", trouble ? trouble + " at a limit or not ready" : "none at a limit or failing"));
    pane.appendChild(sum);
    var box = el("section", "panel");
    var ol = el("ul", "route-list");
    d.routes.forEach(function (n) {
      // Loops on the route now, one per line with their state.
      function running(list) {
        var ul = el("ul");
        list.forEach(function (x) {
          var li = el("li");
          var a = el("a", null, x.loop);
          a.href = "#/loop/" + x.key;
          li.appendChild(a);
          li.appendChild(el("span", "sub", " · " + x.phrase + (x.working ? " · " + x.working + " " + (x.working === 1 ? "lane" : "lanes") + " working" : "")));
          ul.appendChild(li);
        });
        return ul;
      }
      // Loops that only keep it on standby, as one wrapping line of names.
      function standby(list) {
        var p = el("p", "standby");
        list.forEach(function (x, i) {
          if (i) p.appendChild(document.createTextNode(", "));
          var a = el("a", null, x.loop);
          a.href = "#/loop/" + x.key;
          a.title = "Route " + (x.position + 1) + " of " + x.loop;
          p.appendChild(a);
        });
        return p;
      }
      var lanes = [];
      if (n.runningNow.length) lanes.push(lanesBox("Running on it now", running(n.runningNow)));
      if (n.recordedFor.length) lanes.push(lanesBox("Current route of loops that are not in use now", running(n.recordedFor)));
      if (n.standbyFor.length) lanes.push(lanesBox("On standby for " + n.standbyFor.length + (n.standbyFor.length === 1 ? " loop" : " loops"), standby(n.standbyFor)));
      if (n.unknownFor.length) lanes.push(lanesBox("Listed by " + n.unknownFor.length + (n.unknownFor.length === 1 ? " loop" : " loops") + " whose current route is unknown", standby(n.unknownFor)));
      if (n.omitted) lanes.push(el("p", "sub", n.omitted + " more loops not shown"));
      ol.appendChild(routeItem({
        name: routeName(n), account: n.account, active: n.runningNow.length > 0,
        use: n.runningNow.length ? n.runningNow.length + (n.runningNow.length === 1 ? " loop on it now" : " loops on it now") : "",
        readiness: n.readiness === "current" || (n.readiness === "unknown" && n.probeAgeSeconds == null) ? "" : n.readiness, phrase: n.phrase, meta: routeMeta(n), probeAgeSeconds: n.probeAgeSeconds, usage: n.usage, lanes: lanes
      }));
    });
    box.appendChild(ol);
    pane.appendChild(box);
  }

  function renderReadline() {
    var r = $("readline");
    if (!state.lastOk) { r.textContent = state.failed ? "Could not read yet" : "Reading…"; return; }
    var every = state.data.refreshSeconds;
    r.textContent = "Read at " + new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit", second: "2-digit" }).format(new Date(state.data.readAt)) +
      " · reads again every " + (every >= 60 ? span(every) : every + " s");
    r.classList.toggle("is-stale", state.failed);
  }

  function render(navigated) {
    var d = state.data;
    if (!d) return;
    var r = route(), pane = $("pane");
    var top = navigated ? 0 : window.scrollY;
    $("layout").dataset.view = r.view;
    renderPicker(d, r);
    pane.replaceChildren();
    pane.setAttribute("aria-labelledby", "pane-title");
    if (r.view === "loop") renderLoop(d, pane, r.key);
    else if (r.view === "routes") renderRoutes(d, pane);
    else renderHome(d, pane);
    renderReadline();
    document.title = (d.needsYou.length ? "(" + d.needsYou.length + ") " : "") + "keepgoing";
    window.scrollTo(0, top);
    if (navigated) pane.focus({ preventScroll: true });
  }

  function banner(text) {
    var b = $("banner");
    b.hidden = !text;
    b.textContent = text || "";
  }

  // Which command serves this page: "dock" until the server says otherwise.
  function served() { return state.server && state.server.servedBy === "panel" ? "panel" : "dock"; }

  function span(seconds) {
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
      put(p, ["Served to this computer only by ", el("code", null, "keepgoing panel"), ", which stops by itself after " + span(state.server.idleSeconds || 1800) + " with no page reading from it. ", el("code", null, "keepgoing panel stop"), " ends it now."]);
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
    var every = state.data ? state.data.refreshSeconds : 15;
    state.timer = setTimeout(load, Math.max(5, every) * 1000);
  }

  function load() {
    if (state.busy) return;
    // A hidden tab does not poll; it reads again when it is shown.
    if (document.hidden && state.data) { schedule(); return; }
    state.busy = true;
    var btn = $("refresh");
    btn.setAttribute("aria-busy", "true");
    fetch(API + (state.identifiers ? "?identifiers=1" : ""), { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .then(function (d) {
        if (!d || d.schemaVersion !== SCHEMA) throw new Error("schema");
        state.data = d; state.lastOk = Date.now(); state.failed = false;
        banner(d.complete ? "" : "This read was cut off before every loop was read. Loops that were not read are listed as could not be read; it reads again shortly.");
        render(false);
      })
      .catch(function (err) {
        state.failed = true;
        var schema = err && err.message === "schema";
        var again = served() === "panel" ? "run keepgoing panel again." : "start it again and open the new address it prints.";
        banner(schema ? "This page does not understand the reply it was sent. Reload the page, or restart keepgoing " + served() + "." :
          state.data ? "The last read failed, so what you see may be out of date. keepgoing " + served() + " may have stopped; it tries again shortly." :
          "Could not read from keepgoing " + served() + ". It may have stopped; " + again);
        renderReadline();
      })
      .then(function () {
        state.busy = false;
        btn.removeAttribute("aria-busy");
        if (state.again) { state.again = false; load(); return; }
        schedule();
      });
  }

  document.addEventListener("DOMContentLoaded", function () {
    $("refresh").addEventListener("click", function () { clearTimeout(state.timer); load(); });
    $("identifiers").addEventListener("click", function () {
      state.identifiers = !state.identifiers;
      this.setAttribute("aria-pressed", String(state.identifiers));
      this.querySelector("span").textContent = state.identifiers ? "Hide identifiers and notes" : "Show identifiers and notes";
      this.title = this.querySelector("span").textContent;
      // Hide (or keep hidden) at once from the data in hand: identifiers and
      // notes never stay on screen waiting for a read that is slow or failing.
      render(false);
      clearTimeout(state.timer);
      if (state.busy) state.again = true; else load();
    });
    // The skip link moves focus to the details without changing the view.
    document.querySelector("a.skip").addEventListener("click", function (ev) {
      ev.preventDefault();
      $("pane").focus();
    });
    window.addEventListener("hashchange", function () { render(true); });
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && state.lastOk && Date.now() - state.lastOk > 5000) { clearTimeout(state.timer); load(); }
    });
    loadServer();
    load();
  });
})();
