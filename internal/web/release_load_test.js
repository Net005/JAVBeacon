const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const source=fs.readFileSync(__dirname+'/static/app.js','utf8');
const start=source.indexOf('async function loadReleases('),end=source.indexOf('// loadMoreReleases',start);
function harness(){
 const rows=[],loading=[],options=[];
 const context={AbortController,releases:[{id:1}],releasesGrandTotal:null,releasesLoadSeq:0,
  releaseCountAbort:null,releaseRowsAbort:null,releaseLibraryCount:{textContent:''},
  releaseQuery:()=>'',updateReleaseFiltersSummary(){},releaseFilterIndicatorSummary(){},missingFilterIndicatorSummary(){},
  setReleaseLibraryLoading:active=>loading.push(active),populateEntries:()=>options.push(true),
  api:()=>new Promise(()=>{}),loadMoreReleases:()=>new Promise((resolve,reject)=>rows.push({resolve,reject}))};
 vm.createContext(context);vm.runInContext(source.slice(start,end),context);
 return {context,rows,loading,options};
}
test('superseded search cannot hide the newer loading overlay or refresh suggestions',async()=>{
 const h=harness(),old=h.context.loadReleases(),current=h.context.loadReleases();
 h.rows[0].resolve();await old;
 assert.deepEqual(h.loading,[true,true]);assert.equal(h.options.length,0);
 h.rows[1].resolve();await current;
 assert.deepEqual(h.loading,[true,true,false]);assert.equal(h.options.length,1);
});
test('failed active search releases its loading overlay',async()=>{
 const h=harness(),request=h.context.loadReleases();h.rows[0].reject(new Error('Synthetic failure'));
 await assert.rejects(request,/Synthetic failure/);
 assert.deepEqual(h.loading,[true,false]);assert.equal(h.options.length,0);
});
test('background refresh taking over an active search clears its inherited overlay',async()=>{
 const h=harness(),foreground=h.context.loadReleases(),background=h.context.loadReleases(true);
 h.rows[0].resolve();await foreground;
 assert.deepEqual(h.loading,[true]);
 h.rows[1].resolve();await background;
 assert.deepEqual(h.loading,[true,false]);
});
