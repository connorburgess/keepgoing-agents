// DevBrowser check: run only on server.py's marked, fictional page.
const p=await browser.getPage('main');
if(!await p.$eval('body',e=>e.dataset.fictionalCaveatsFixture==='true'))throw new Error('Not the fictional caveats fixture');
const origin=await p.$eval('body',()=>location.origin),results=[];
for(const site of ['before','after'])for(const theme of ['light','dark'])for(const view of ['home','loop'])for(const width of [320,390,700,1000,1440,1920]){
 await p.setViewport({width,height:900});await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:theme}]);
 await p.goto(origin+'/'+site+'/normal/'+(view==='loop'?'#/loop/exampleloop':'#/'));await p.waitForSelector(view==='home'?'.work-tree':'.migration-review');
 const r=await p.$eval('body',(e,args)=>{
  const [site,view]=args,vs=e.querySelector('a[href="#/versions"] .pick-state');
  const wraps=[];
  e.querySelectorAll('.tools button,.btn,.state').forEach(b=>{if(!b.getBoundingClientRect().width)return;const walker=document.createTreeWalker(b,NodeFilter.SHOW_TEXT),tops=[];let n;while(n=walker.nextNode()){if(!n.textContent.trim())continue;const r=document.createRange();r.selectNodeContents(n);for(const x of r.getClientRects())if(x.width)tops.push(Math.round(x.top));}if(new Set(tops).size>1)wraps.push(b.textContent.trim());});
  const h=[...e.querySelectorAll('h2')].find(h=>h.textContent==='Loops and lanes');
  const first=h&&h.parentElement.children[1];
  return {overflow:document.documentElement.scrollWidth>innerWidth,wraps,versionsCut:vs.scrollWidth>vs.clientWidth+1,first:first&&first.className,footer:e.querySelectorAll('footer > p').length,migration:!!e.querySelector('.work-migration,.migration-review'),calm:view==='loop'&&!!e.querySelector('.chain-ready.is-calm')};
 },[site,view]);
 const ok=!r.overflow&&!r.wraps.length&&(site==='before'||(!r.versionsCut&&r.footer===2&&r.migration&&(view==='loop'?r.calm:r.first==='work-tree')));
 results.push({site,theme,view,width,ok,...r});if(!ok)throw new Error(JSON.stringify(results.at(-1)));
}
console.log(JSON.stringify(results));
// G8: full labels survive mouse, keyboard, touch-style opening and feature loss.
for(const width of [390,1440]){
 await p.emulateMediaFeatures([{name:'prefers-reduced-motion',value:'no-preference'}]);await p.setViewport({width,height:900});await p.goto(origin+'/after/normal/#/');await p.reload();await p.waitForSelector('.work-tree');
 const measure=()=>p.$eval('.pick-list .pick',e=>{const s=e.querySelector('.pick-state'),n=s.firstElementChild;return {height:e.getBoundingClientRect().height,right:n.getBoundingClientRect().right,box:s.getBoundingClientRect().right,chevron:e.querySelector('svg.go').getBoundingClientRect().left,focus:e.matches(':focus-visible'),translate:getComputedStyle(n).translate,overflow:getComputedStyle(s).textOverflow};});
 const before=await measure();await(await p.$('.pick-list .pick')).hover();await new Promise(r=>setTimeout(r,2300));const hover=await measure();
 await(await p.$('#refresh')).hover();await p.keyboard.press('Tab');await p.$eval('.pick-list .pick',e=>e.focus());await new Promise(r=>setTimeout(r,2300));const focus=await measure();
 for(const [mode,r]of [['hover',hover],['focus',focus]]){if(r.right>r.box+1.1||r.right>=r.chevron||r.height!==before.height||(mode==='focus'&&!r.focus))throw new Error('G8 '+mode+' '+JSON.stringify(r));}console.log('G8 reveal',width,{before,hover,focus});
 await p.emulateMediaFeatures([{name:'prefers-reduced-motion',value:'reduce'}]);await new Promise(r=>setTimeout(r,100));const reduced=await measure();if(reduced.translate!=='none'||reduced.overflow!=='ellipsis')throw new Error('G8 reduced motion '+JSON.stringify(reduced));
 await p.keyboard.press('Enter');await p.waitForFunction(()=>document.querySelector('#pane h1').textContent.includes('visible-tail'));console.log('G8 reduced-motion Enter opens full heading',width);
 await p.goto(origin+'/after/normal/#/');await p.reload();await p.waitForSelector('.work-tree');await p.emulateMediaFeatures([{name:'prefers-reduced-motion',value:'no-preference'}]);
 await p.$eval('body',()=>{function remove(rules,owner){for(let i=rules.length-1;i>=0;i--){const r=rules[i];if(r.conditionText&&(/animation-timeline|style\(--truncated/.test(r.conditionText))){owner.deleteRule(i);}else if(r.cssRules&&r.deleteRule)remove(r.cssRules,r);}}for(const s of document.styleSheets)remove(s.cssRules,s);});
 await(await p.$('.pick-list .pick')).hover();const fallback=await measure();if(fallback.translate!=='none'||fallback.overflow!=='ellipsis')throw new Error('G8 feature fallback '+JSON.stringify(fallback));
 await p.keyboard.press('Tab');await p.$eval('.pick-list .pick',e=>e.focus());await p.keyboard.press('Enter');await p.waitForFunction(()=>document.querySelector('#pane h1').textContent.includes('visible-tail'));console.log('G8 unsupported-feature Enter opens full heading',width);
}
await p.setViewport({width:320,height:900});await p.goto(origin+'/after/normal/#/');await p.reload();await p.waitForSelector('.work-tree');
for(const text of ['12 places need a look','1 place on another build','12 places on another build','1 place cannot be compared','12 places cannot be compared','All on this build','All on v0.10.12']){
 await p.$eval('a[href="#/versions"] .pick-state > span',(e,t)=>{e.textContent=t;},text);
 const fits=await p.$eval('a[href="#/versions"] .pick-state',e=>e.scrollWidth<=e.clientWidth+1);if(!fits)throw new Error('Versions label cut: '+text);
}console.log('All7Versions forms fit at320');
await p.goto(origin+'/after/unbroken/#/loop/exampleloop');await p.waitForFunction(()=>document.querySelector('#pane h1').textContent==='R'.repeat(100));
if(!await p.$eval('body',()=>document.documentElement.scrollWidth<=innerWidth))throw new Error('Unbroken detail heading overflow');console.log('G8 unbroken full heading wraps at320');
