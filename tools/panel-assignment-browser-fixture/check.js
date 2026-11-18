const p = await browser.getPage("main");
if (!await p.$eval("body", e => e.dataset.fictionalAssignmentFixture === "true")) throw new Error("Not the fictional assignment fixture");
const origin = await p.$eval("body", () => location.origin);
const checks = [];
function check(name, ok) { checks.push({name, ok}); if (!ok) throw new Error(name); }
await p.waitForSelector(".work-tree");
const key = await p.$eval(".work-tree", e => Array.from(e.querySelectorAll("details[data-loop]")).find(x => x.querySelector(".work-name").firstChild.textContent === "docs-site-refresh").dataset.loop);
await p.$eval('details[data-loop="' + key + '"]', e => { e.open = false; e.querySelector("summary").focus(); });
await p.keyboard.press("Enter");
check("keyboard expands loop", await p.$eval('details[data-loop="' + key + '"]', e => e.open));
await p.$eval('details[data-loop="' + key + '"] a.work-inspect', e => e.focus());
await p.keyboard.press("Enter");
await p.waitForSelector(".assignment-review");
check("unknown and unacknowledged evidence remains visible", await p.$eval(".assignment-review", e => ["2 assignments need reconciliation", "2 / 1", "Partial", "2029", "2030", "Unfinished assignments", "3", "Person-blocked / dependency-waiting", "1 / 1"].every(s => e.textContent.includes(s))));
check("scope and declaration limits visible", await p.$eval(".assignment-review", e => ["descendants are excluded", "running or idle", "declared outcome and availability", "permission", "clear holds"].every(s => e.textContent.includes(s))));
check("no new action or form", await p.$eval(".assignment-review", e => !e.querySelector("button,input,a,form")));
check("semantic section heading", await p.$eval(".assignment-review", e => e.querySelector("h2").textContent === "Assignment handoffs"));
for (const theme of ["light", "dark"]) {
 await p.emulateMediaFeatures([{name:"prefers-color-scheme",value:theme}]);
 for (const width of [320,390,768,1280]) {
  await p.setViewport({width,height:1000});
  check(theme + " section fits " + width, await p.$eval(".assignment-review", e => e.getBoundingClientRect().width <= innerWidth && e.scrollWidth <= e.clientWidth));
 }
}
for (const [name, expected, counts] of [["billing-api-migration","earlier binding",false],["mobile-onboarding","Outcomes are unknown",false],["release-notes-bot","No assignment review is saved",false],["search-indexer","0 assignments need reconciliation",true]]) {
 await p.goto(origin + "/#/");await p.waitForSelector(".work-tree");
 const k = await p.$eval(".work-tree", (e,n) => Array.from(e.querySelectorAll("details[data-loop]")).find(x => x.querySelector(".work-name").firstChild.textContent === n).dataset.loop,name);
 await p.goto(origin + "/#/loop/" + k);await p.waitForSelector(".assignment-review");
 check(name + " has correct evidence state", await p.$eval(".assignment-review", (e,v) => e.textContent.includes(v.expected) && !!e.querySelector("dl") === v.counts,{expected,counts}));
}
await p.goto(origin + "/#/loop/" + key);await p.waitForSelector(".assignment-review");
await p.setViewport({width:390,height:1000});await p.emulateMediaFeatures([{name:"prefers-color-scheme",value:"light"}]);
await p.$eval(".assignment-review", e => e.scrollIntoView());
await p.shot({type:"jpeg",maxEdge:1000,quality:75});
console.log(JSON.stringify(checks));
