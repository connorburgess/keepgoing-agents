const p=await browser.getPage('main');
const origin='http://127.0.0.1:60298';
const checks=[];
function check(name,ok){checks.push({name,ok});if(!ok)throw new Error(name);}
await p.goto(origin+'/owner/#/');await p.reload();await p.waitForSelector('.work-branch');
check('overview makes no saved account companion read',await p.$eval('body',()=>!performance.getEntriesByType('resource').some(r=>r.name.includes('/saved-accounts'))));
check('default overview renders nonempty loops without reply error',await p.$eval('#pane',e=>e.querySelectorAll('.work-branch').length>0&&!e.textContent.includes('could not show')));
await p.goto(origin+'/owner/#/accounts');await p.reload();await p.waitForSelector('.account-card');
check('fictional marker',await p.$eval('body',e=>e.dataset.fictionalQuotaFixture==='true'));
check('compact named windows visible before expansion',await p.$eval('.account-card summary',e=>['Codex · 5-hour','37.5% used','Codex · 7-day','82% used'].every(x=>e.textContent.includes(x))));
await p.$eval('.account-card details',e=>{e.open=false;e.querySelector('summary').focus();});await p.keyboard.press('Enter');
check('keyboard expands account',await p.$eval('.account-card details',e=>e.open));
check('independent windows and remaining values',await p.$eval('.account-quota',e=>e.textContent.includes('37.5% used · 62.5% remaining')&&e.textContent.includes('82% used · 18% remaining')&&e.textContent.includes('0% used · 100% remaining')&&!e.textContent.includes('99%')));
check('typed amounts never fabricated currency',await p.$eval('.account-quota',e=>e.textContent.includes('12.50 used of 100.00')&&e.textContent.includes('balance 12.75')&&e.textContent.includes('currency not reported')&&!e.textContent.includes('USD')&&!e.textContent.includes('$')));
check('provider availability unknown not inferred from percent',await p.$eval('.account-quota',e=>e.textContent.includes('Unknown; percentages and reset times do not establish access.')));
check('accessible bounded meter names include used and remaining',await p.$eval('.account-quota',e=>e.querySelectorAll('meter').length===3&&Array.from(e.querySelectorAll('meter')).every(m=>m.min===0&&m.max===100&&m.getAttribute('aria-label').includes('remaining'))));
check('source freshness and reset timestamps visible',await p.$eval('.account-quota',e=>['Source: codex account/rateLimits/read','Saved snapshot','Resets','Reset time unavailable','Saved for this selection'].every(t=>e.textContent.includes(t))));
check('selected quota separate from unknown-route reading',await p.$eval('#pane',e=>e.querySelector('.unknown-account-routes').textContent.includes('73%')&&!e.querySelector('.unknown-account-routes').textContent.includes('37.5%')&&!e.querySelector('.account-quota').textContent.includes('73%')));
await p.$eval('#pane',e=>e.querySelectorAll('details').forEach(x=>x.open=true));
check('Claude fixed unsupported with no refresh action',await p.$eval('#pane',e=>{const c=Array.from(e.querySelectorAll('.account-card')).find(x=>x.textContent.includes('Example Claude'));return c.textContent.includes('No supported model-free account quota source')&&!c.querySelector('.account-quota button')&&!c.querySelector('.account-quota meter');}));
check('stale failed refresh keeps ended reading rather than zero',await p.$eval('#pane',e=>{const c=Array.from(e.querySelectorAll('.account-card')).find(x=>x.textContent.includes('Example older'));return ['Older reading','Refresh failed','earlier reading retains its original time','Window ended','100% used'].every(t=>c.textContent.includes(t))&&!c.querySelector('meter');}));
check('partial missing values not zero',await p.$eval('#pane',e=>{const c=Array.from(e.querySelectorAll('.account-card')).find(x=>x.textContent.includes('Example partial'));return c.textContent.includes('Missing values are not zero')&&!c.querySelector('meter')&&!c.textContent.includes('0%');}));
check('raw personal metadata excluded',await p.$eval('#pane',e=>!['private@example.com','private-id','private-model-alias','banner'].some(t=>e.textContent.includes(t))));
for(const theme of ['light','dark']){await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:theme}]);for(const width of [320,360,390,414,600,700,760,768,900,1024,1280,1440,1920]){await p.setViewport({width,height:1000});check(theme+' '+width+' fits',await p.$eval('body',()=>document.documentElement.scrollWidth<=innerWidth));}}
await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:'light'}]);await p.setViewport({width:390,height:1000});await p.$eval('.account-quota',e=>e.scrollIntoView());await p.shot({type:'jpeg',quality:75,maxEdge:1000});
await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:'dark'}]);await p.setViewport({width:1440,height:1000});await p.$eval('#pane',e=>{e.querySelectorAll('details').forEach(x=>x.open=false);e.scrollIntoView();});await p.shot({type:'jpeg',quality:75,maxEdge:1200});
await p.goto(origin+'/readonly/#/accounts');await p.waitForSelector('.account-card');await p.click('.account-card summary');
check('readonly saved view has no refresh control',await p.$eval('.account-quota',e=>!e.querySelector('button')&&e.textContent.includes('37.5%')));
for(const variant of ['empty','unavailable']){
 await p.goto(origin+'/'+variant+'/#/accounts');
 if(variant==='empty') await p.waitForFunction(()=>document.querySelector('#pane').textContent.includes('0 selected accounts'));
 else await p.waitForFunction(()=>document.querySelector('#pane').textContent.includes('Accounts unavailable'));
 check(variant+' does not invent quota zero',await p.$eval('#pane',e=>!e.querySelector('meter')&&!e.textContent.includes('0%')));
}
await p.goto(origin+'/loading/#/accounts');
await p.waitForFunction(()=>document.querySelector('#pane').textContent.includes('Reading saved accounts'));
check('saved loading is explicit without invented zero',await p.$eval('#pane',e=>!e.querySelector('meter')&&!e.textContent.includes('0 selected accounts')));
await p.waitForSelector('.account-card');
check('loading resolves full saved windows',await p.$eval('.account-card',e=>e.textContent.includes('37.5% used')));
await p.goto(origin+'/error/#/accounts');
await p.waitForFunction(()=>document.querySelector('#pane').textContent.includes('Saved accounts unavailable'));
check('endpoint failure remains explicit and no zero',await p.$eval('#pane',e=>!e.querySelector('meter')&&!e.textContent.includes('0 selected accounts')));
await p.$eval('body',()=>{location.hash='#/';});await p.waitForSelector('.work-branch');
check('endpoint failure never blocks overview',await p.$eval('#pane',e=>!!e.querySelector('.work-branch')&&!e.textContent.includes('could not show')));
await p.goto(origin+'/legacy/#/accounts');await p.waitForSelector('.account-card');
check('legacy inline report remains usable without companion read',await p.$eval('body',()=>!performance.getEntriesByType('resource').some(r=>r.name.includes('/saved-accounts'))));
await p.goto(origin+'/owner/#/accounts');await p.waitForSelector('.account-card');
await p.$eval('body',async()=>{
 const original=window.fetch.bind(window), main=await (await original('api/v1/dock')).json(), saved=await (await original('api/v1/saved-accounts?reference='+encodeURIComponent(main.accountsReference))).json();
 const clone=x=>JSON.parse(JSON.stringify(x));
 window.quotaFixture={count:0,held:null,posts:0,original};
 window.fetch=(url,options)=>{
  if(options&&options.method==='POST')window.quotaFixture.posts++;
  if(String(url).startsWith('api/v1/dock')) {const d=clone(main);d.accountsReference=++window.quotaFixture.count===1?'older-context':'current-context';return Promise.resolve(new Response(JSON.stringify(d),{status:200}));}
  if(String(url).startsWith('api/v1/saved-accounts')) {
   const old=String(url).includes('older-context'), d=clone(saved);d.reference=old?'older-context':'current-context';d.accounts.cards[0].label=old?'Delayed older selection':'Current selection';
   if(old)return new Promise(resolve=>{window.quotaFixture.held=()=>resolve(new Response(JSON.stringify(d),{status:200}));});
   return Promise.resolve(new Response(JSON.stringify(d),{status:200}));
  }
  return original(url,options);
 };
});
await p.click('#refresh');await p.waitForFunction(()=>!!window.quotaFixture.held);
check('new reference hides previous selected quota during loading',await p.$eval('#pane',e=>e.textContent.includes('Reading saved accounts')&&!e.querySelector('.account-card')));
await p.click('#refresh');await p.waitForFunction(()=>document.querySelector('#pane').textContent.includes('Current selection'));
await p.$eval('body',()=>window.quotaFixture.held());
await p.waitForFunction(()=>document.querySelector('#pane').textContent.includes('Current selection'));
check('delayed old context cannot overwrite current selection',await p.$eval('#pane',e=>!e.textContent.includes('Delayed older selection')&&e.textContent.includes('Current selection')&&e.textContent.includes('37.5% used')));
check('companion navigation/read now never posts quota refresh',await p.$eval('body',()=>window.quotaFixture.posts===0));
await p.$eval('body',()=>{
 window.fetch=window.quotaFixture.original;
 window.quotaFixture.realTimer=window.setTimeout.bind(window);
 window.setTimeout=(fn,ms,...args)=>window.quotaFixture.realTimer(fn,ms===13000?50:ms,...args);
 const original=window.fetch;
 window.fetch=(url,options)=>String(url).startsWith('api/v1/saved-accounts')?new Promise((resolve,reject)=>options.signal.addEventListener('abort',()=>reject(new DOMException('Fictional timeout','AbortError')))):original(url,options);
});
await p.click('#refresh');await p.waitForFunction(()=>document.querySelector('#pane').textContent.includes('Saved accounts unavailable'));
check('aborted companion read becomes explicit failure',await p.$eval('#pane',e=>!e.querySelector('.account-card')&&!e.querySelector('meter')));
await p.$eval('body',()=>{window.fetch=window.quotaFixture.original;window.setTimeout=window.quotaFixture.realTimer;});
await p.click('#refresh');await p.waitForSelector('.account-card');
check('read now recovers saved endpoint after timeout',await p.$eval('.account-card',e=>e.textContent.includes('37.5% used')));
console.log(JSON.stringify(checks));
