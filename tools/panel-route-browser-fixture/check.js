// Run against server.py only, using a DevBrowser page already at its fixture URL.
// All API responses are fictional; Go endpoint tests exercise actual writers.
const p=await browser.getPage('main');
if (!await p.$eval('body',e=>e.dataset.fictionalRouteFixture==='true')) throw new Error('This is not the fictional route-editor fixture; no controls were used.');
const origin=await p.$eval('body',()=>location.origin);
const checks=[];
function check(name,ok){console.log(name,ok);checks.push({name,ok});if(!ok)throw new Error(name);}
async function set(name,value){console.log("set",name);await p.$eval('#route-field-'+name,(e,v)=>{e.value=v;e.dispatchEvent(new Event(e.tagName==='SELECT'?'change':'input',{bubbles:true}));},value);}
await p.goto(origin+'/owner/#/routes');await p.reload();await p.waitForSelector('[data-focus="route-edit-refresh"]');await p.click('[data-focus="route-edit-refresh"]');await p.waitForSelector('#route-field-account');
await set('account','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');await p.waitForSelector('#route-field-model');
check('no automatic model listing',await p.$eval('#route-field-model',e=>e.tagName==='INPUT'));
await p.click('[data-focus="route-models"]');await p.waitForSelector('select#route-field-model');
check('explicit metadata offers a model select',await p.$eval('#route-field-model',e=>e.tagName==='SELECT'));
await set('model','example-listed');await set('effort','high');
check('efforts come from selected model metadata',await p.$eval('#route-field-effort',e=>Array.from(e.options).map(o=>o.value).join(',')===',medium,high'));
await set('behavior','new_conversation');await set('limitChoice','new');await p.waitForSelector('#route-field-limit');await set('limit','owner-confirmed-limit');
check('new conversation requires an explicit unchecked opt-in',await p.$eval('.route-add-form',e=>!e.checkValidity()&&!e.querySelector('[name=newConversation]').checked));
await p.click('[name=newConversation]');
check('labelled native fields and valid concrete draft',await p.$eval('.route-add-form',e=>e.checkValidity()&&Array.from(e.querySelectorAll('input:not([type=checkbox]),select')).every(x=>!!x.labels.length)));
await p.$eval('#route-field-limit',e=>{e.focus();e.setSelectionRange(5,5);});
await p.click('#refresh');await p.waitForFunction(()=>!document.querySelector('#refresh').hasAttribute('aria-busy'));
check('draft persists after Read now',await p.$eval('#route-field-limit',e=>e.value==='owner-confirmed-limit'));
await p.click('[data-focus="route-add-review"]');await p.waitForSelector('[data-focus="route-save"]');
check('review focused and save separate',await p.$eval('#route-feedback',e=>document.activeElement===e&&e.textContent.includes('After:')));
await p.keyboard.press('Tab');
check('keyboard reaches Save',await p.$eval('[data-focus="route-save"]',e=>document.activeElement===e));
await p.keyboard.press('Enter');await p.waitForFunction(()=>document.querySelector('#route-feedback')&&document.querySelector('#route-feedback').textContent.includes('Saved'));
check('save has no replay button',await p.$eval('#route-feedback',e=>e.textContent.includes('Saved')&&!e.querySelector('[data-focus=route-save]')));
await p.click('[data-focus="route-edit-refresh"]');await p.waitForSelector('.route-follow-form');
await p.$eval('.route-follow-form',e=>{e.querySelectorAll('input[type=checkbox]').forEach(c=>{c.click();});});
await p.click('.route-follow-form button[type=submit]');await p.waitForSelector('[data-focus="route-save"]');
await p.click('[data-focus="route-save"]');await p.waitForFunction(()=>document.querySelector('#route-feedback')&&document.querySelector('#route-feedback').textContent.includes('Saving stopped'));
check('partial save distinguishes success refusal and unattempted',await p.$eval('#route-feedback',e=>['example-loop-1: changed','example-loop-2: refused','example-loop-3: not attempted','Earlier successful saves remain'].every(t=>e.textContent.includes(t))));
await p.click('[data-focus="route-edit-refresh"]');await p.waitForSelector('#route-field-account');await set('account','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');await p.waitForSelector('#route-field-model');await set('model','R'.repeat(100));await set('behavior','same_conversation');await set('limitChoice','owner-limit-a');await p.click('[data-focus="route-add-review"]');await p.waitForSelector('[data-focus="route-save"]');
check('unbroken model name survives the rendered review',await p.$eval('#route-feedback',e=>e.textContent.includes('R'.repeat(100))));
for(const theme of ['light','dark']){await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:theme}]);for(const width of [320,360,390,414,600,700,768,900,1024,1280,1440,1600,1920]){await p.setViewport({width,height:1000});const result=await p.$eval('body',()=>({width:innerWidth,scroll:document.documentElement.scrollWidth,labels:Array.from(document.querySelectorAll('.route-editor button')).every(e=>e.getBoundingClientRect().height>=43)}));check(theme+' width '+width,result.scroll<=width&&result.labels);}}
await p.setViewport({width:390,height:1000});await p.emulateMediaFeatures([{name:'prefers-color-scheme',value:'light'}]);await p.$eval('#route-feedback',e=>e.scrollIntoView());await p.shot({type:'jpeg',maxEdge:1000,quality:70});
for(const mode of ['agent','readonly']){await p.goto(origin+'/'+mode+'/#/routes');await p.waitForSelector('.route-editor');check(mode+' limitation precedes form',await p.$eval('.route-editor',(e,m)=>!e.querySelector('form')&&e.textContent.includes(m==='agent'?'BB thread':'read-only'),mode));}
await p.goto(origin+'/owner/#/routes');await p.waitForSelector('[data-focus="route-edit-refresh"]');await p.click('[data-focus="route-edit-refresh"]');await p.waitForSelector('#route-field-account');await set('account','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb');await p.waitForSelector('#route-field-model');
check('Codex CLI does not offer an unsupported effort override',await p.$eval('.route-add-form',e=>!e.querySelector('[name=effort]')&&e.textContent.includes('no effort override')));
console.log(JSON.stringify(checks));
