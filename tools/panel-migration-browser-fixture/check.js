// Run only on server.py's marked fictional page with Browser Automation.
const p = await browser.getPage("main");
if (!await p.$eval("body", e => e.dataset.fictionalMigrationFixture === "true")) throw new Error("Not the fictional migration fixture");
const origin = await p.$eval("body", () => location.origin);
const checks = [];
function check(name, ok) { checks.push({name, ok}); if (!ok) throw new Error(name); }
await p.waitForSelector(".work-tree");
check("overview retains historical unfinished work", await p.$eval(".work-tree", e => e.textContent.includes("3 unfinished assignments declared") && e.textContent.includes("historical evidence from")));
const key = await p.$eval(".work-tree", e => Array.from(e.querySelectorAll("details[data-loop]")).find(x => x.querySelector(".work-name").firstChild.textContent === "docs-site-refresh").dataset.loop);
await p.$eval('details[data-loop="' + key + '"]', e => { e.open = false; e.querySelector("summary").focus(); });
await p.keyboard.press("Enter");
check("keyboard expands loop without changing supervision", await p.$eval('details[data-loop="' + key + '"]', e => e.open && e.textContent.includes("Supervision: Supervising")));
await p.$eval('details[data-loop="' + key + '"] a.work-inspect', e => e.focus());
await p.keyboard.press("Enter");
await p.waitForSelector(".migration-review");
check("historical direct-lane limits are visible", await p.$eval(".migration-review", e => ["3 unfinished", "2029", "Dormant / unresolved", "1 / 1", "Assignments absent from listing", "predecessor direct lanes only", "descendants are excluded", "working now", "permission", "Dormant work may be intentional"].every(s => e.textContent.includes(s))));
check("migration section adds no action", await p.$eval(".migration-review", e => !e.querySelector("button,input,a")));
check("migration heading follows main loop heading", await p.$eval(".migration-review", e => e.querySelector("h2").textContent === "Migration review" && !!document.querySelector("#pane h1")));
for (const theme of ["light", "dark"]) {
    await p.emulateMediaFeatures([{name:"prefers-color-scheme", value:theme}]);
    for (const width of [320,390,768,1280]) {
        await p.setViewport({width,height:1000});
        check(theme + " no horizontal overflow at " + width, await p.$eval(".migration-review", e => document.documentElement.scrollWidth <= innerWidth && e.getBoundingClientRect().width <= innerWidth));
    }
}
for (const [name, expected, counts] of [
    ["billing-api-migration", "different loop binding", false],
    ["mobile-onboarding", "Unfinished work is unknown", false],
    ["release-notes-bot", "No migration review is saved", false],
    ["search-indexer", "0 unfinished assignments declared", true]
]) {
    await p.goto(origin + "/#/");
    await p.waitForSelector(".work-tree");
    const k = await p.$eval(".work-tree", (e, name) => Array.from(e.querySelectorAll("details[data-loop]")).find(x => x.querySelector(".work-name").firstChild.textContent === name).dataset.loop, name);
    await p.goto(origin + "/#/loop/" + k);
    await p.waitForSelector(".migration-review");
    check(name + " preserves evidence state", await p.$eval(".migration-review", (e, v) => e.textContent.includes(v.expected) && !!e.querySelector("dl") === v.counts, {expected,counts}));
}
await p.goto(origin + "/#/loop/" + key);
await p.waitForSelector(".migration-review");
await p.setViewport({width:390,height:1000});
await p.emulateMediaFeatures([{name:"prefers-color-scheme",value:"light"}]);
await p.$eval(".migration-review", e => e.scrollIntoView());
await p.shot({type:"jpeg",maxEdge:1000,quality:75});
console.log(JSON.stringify(checks));
