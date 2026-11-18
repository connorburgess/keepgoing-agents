// Run only against server.py and its fictional Go-projected reports.
const p=await browser.getPage('main');
const origin='http://127.0.0.1:51200';
const checks=[];
function check(name,ok){checks.push({name,ok});if(!ok)throw new Error(name);}
await p.goto(origin+'/before/#/routes');await p.waitForSelector('.allow-list');
check('baseline substitutes newest shared-context window',await p.$eval('.allow-list',e=>e.children.length===3&&e.textContent.includes('73%')&&!e.textContent.includes('Windows ended')&&!e.textContent.includes('No usage reading')));
await p.goto(origin+'/after/#/routes');await p.waitForSelector('.allow-list');
check('only explicit assignment combines heading',await p.$eval('.allow-list',e=>e.children.length===5&&e.querySelectorAll('.is-account').length===1));
check('assigned routes retain independent windows and missing reading',await p.$eval('.is-account',e=>e.querySelectorAll('.account-route').length===3&&e.textContent.includes('100%')&&e.textContent.includes('21%')&&e.textContent.includes('No usage reading')&&!e.textContent.includes('37%')));
check('assigned route out first and model-only title',await p.$eval('.is-account .account-route a',e=>e.textContent==='R'.repeat(80)&&e.getAttribute('href')==='#/routes/r2'));
check('readings retain own dates and sources without repeated resets',await p.$eval('.is-account',e=>e.textContent.includes('Read 2 h ago')&&e.textContent.includes('Read 20 min ago')&&e.querySelectorAll('.allow-reading[title]').length===2&&(e.textContent.match(/Source: recorded route probe/g)||[]).length===2&&(e.textContent.match(/Resets in 3 h/g)||[]).length===2));
check('unknown shared context retains stale missing recent independently',await p.$eval('.allow-list',e=>{const a=Array.from(e.children).filter(x=>x.querySelector('h3').textContent==='example-unassigned');return a.length===3&&a[0].textContent.includes('Windows ended')&&!a[0].textContent.includes('73%')&&a[1].textContent.includes('No usage reading')&&!a[1].querySelector('[role=img]')&&a[2].textContent.includes('73%')&&!a[2].textContent.includes('93%');}));
check('no numbered accounts or subscription quota on Routes',await p.$eval('.allow-list',e=>!e.textContent.match(/account \d|37%|subscription quota/i)&&e.textContent.includes('another provider')));
for(const theme of ['light','dark']){
 await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:theme}]);
 for(const width of [320,360,375,390,414,480,600,640,700,768,820,900,1000,1024,1280,1440,1600,1920]){
  await p.setViewport({width,height:1000});
  check(theme+' '+width+' no overflow',await p.$eval('body',()=>document.documentElement.scrollWidth<=innerWidth));
 }
}
await p.setViewport({width:1440,height:1000});
await p.$eval('.allow-list',e=>e.style.width='300px');
check('narrow container stacks independent of viewport',await p.$eval('.allow',e=>getComputedStyle(e).gridTemplateColumns.split(' ').length===1));
await p.reload();await p.waitForSelector('.allow-list');
// Simulate a browser without size container queries, retaining viewport fallback.
await p.$eval('body',()=>{for(const sheet of document.styleSheets){for(let i=sheet.cssRules.length-1;i>=0;i--){const r=sheet.cssRules[i];if(r.cssText.startsWith('@container'))sheet.deleteRule(i);}}});
for(const width of [320,390,700,1440]){await p.setViewport({width,height:1000});check('no-container fallback '+width,await p.$eval('body',()=>document.documentElement.scrollWidth<=innerWidth&&document.querySelector('.is-account').textContent.includes('R'.repeat(80))));}
await p.setViewport({width:390,height:1000});await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:'light'}]);await p.$eval('.allow-list',e=>e.scrollIntoView());await p.shot({type:'jpeg',maxEdge:1000,quality:70});
await p.reload();await p.waitForSelector('.allow-list');
await p.$eval('.is-account .account-route a',e=>e.focus());await p.keyboard.press('Enter');await p.waitForFunction(()=>location.hash==='#/routes/r2'&&document.activeElement.id==='route-r2');
check('keyboard link opens its exact route',await p.$eval('body',()=>location.hash==='#/routes/r2'&&document.activeElement.id==='route-r2'));
await p.goto(origin+'/after/#/accounts');await p.waitForSelector('.account-card');await p.click('.account-card summary');await p.waitForSelector('.account-quota');
check('selected quota remains independently 37 percent',await p.$eval('.account-quota',e=>e.textContent.includes('37% used')&&!e.textContent.includes('73%')&&e.textContent.includes('selected-context')));
check('only one selected account',await p.$eval('.account-count',e=>e.textContent==='1 selected account'));
for(const variant of ['empty','unavailable']){
 await p.goto(origin+'/after/'+variant+'/#/routes');await p.waitForSelector('.empty');
 check(variant+' does not invent account blocks',await p.$eval('#pane',(e,v)=>!e.querySelector('.allow-list')&&e.textContent.includes(v==='empty'?'No routes to show.':'registry could not be read'),variant));
}
await p.goto(origin+'/after/#/routes');await p.waitForSelector('.allow-list');await p.setViewport({width:1440,height:1000});await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:'dark'}]);await p.$eval('.allow-list',e=>e.scrollIntoView());await p.shot({type:'jpeg',maxEdge:1000,quality:70});
console.log(JSON.stringify(checks));
