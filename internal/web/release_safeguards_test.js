const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const html=fs.readFileSync(path.join(__dirname,'static/index.html'),'utf8');
const source=fs.readFileSync(path.join(__dirname,'static/app.js'),'utf8');
const vm=require('node:vm');
test('blacklisted releases area and configurable safeguard fields exist',()=>{
 for(const id of ['blacklistedReleasesPanel','blacklistedReleaseForm','blacklistedReleaseList','blacklistedReleaseSearch'])assert.ok(html.includes(`id="${id}"`),id);
 for(const name of ['download_min_duration_minutes','download_min_filesize_mb']){
  assert.ok(html.includes(`name="${name}"`),name);
  assert.equal((source.match(new RegExp(`'${name}'`,'g'))||[]).length,2,'setting must load and save');
 }
});
test('blacklisted search result cannot offer force download',()=>{
 const line=source.split('\n').find(line=>line.startsWith('function searchResultCard('));
 const context={searchDialog:{},settings:{},safe:String,esc:String,attr:String,formatBytes:()=>'',actionIcon:()=>'',searchSwarmLabel:()=>'',searchMatchLabel:()=>'',searchFilesMarkup:()=>''};
 vm.createContext(context);vm.runInContext(line,context);
 const html=context.searchResultCard(1,{title:'Synthetic',transport:'torrent',accepted:false,blacklisted_release_match:true},0);
 assert.ok(html.includes('⛔ Blacklisted'));assert.ok(!html.includes('onclick="forceDownload'));
});
