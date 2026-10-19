const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
const viewerFunctions=source.slice(source.indexOf('class PreviewBuffer {'),source.indexOf('function stopVideo()'));
const stepFunction=source.slice(source.indexOf('async function stepPhoto('),source.indexOf('async function updateStatus()'));
const photo=id=>({id,name:`photo${id}.jpg`,kind:'image'});
const page=(number,ids,total=ids.length,limit=60)=>({page:number,photos:ids.map(photo),total,limit});
async function flush(){for(let n=0;n<8;n++)await Promise.resolve();}
function setup(initial=page(1,[1,2,3]),loader){
  const elements=new Map(),created=[],requests=[],pages=[],timers=[];
  class Image{
    constructor(){this.isConnected=false;this.hidden=true;this._src='';created.push(this);}
    set src(value){this._src=value;if(value)requests.push(value);}
    get src(){return this._src;}
    removeAttribute(name){if(name==='src')this._src='';}
    replaceWith(other){this.isConnected=false;other.isConnected=true;elements.set('viewer-image',other);}
    async decode(){}
    async finish(){await this.onload?.();await flush();}
  }
  const thumb=new Image();thumb.isConnected=true;
  elements.set('viewer-image',thumb);elements.set('viewer',{open:true});elements.set('viewer-loading',{hidden:false});elements.set('viewer-error',{hidden:true});
  const context=vm.createContext({Image,AbortController,URLSearchParams,$:id=>elements.get(id),imageURL:(p,size='thumb')=>`/api/image?id=${p.id}&size=${size}`,params:o=>new URLSearchParams(o),setTimeout:fn=>timers.push(fn),api:async(url,options)=>{pages.push(url);return loader?loader(url,options):Promise.reject(Error('Unexpected page request'));}});
  vm.runInContext(`let listing=${JSON.stringify(initial)},viewSequence=1,viewerSession=0,pageBusy=false,gridDirty=false,photoIndex=-1;const state={page:listing.page};const viewerPages=new Map(); ${viewerFunctions} ${stepFunction}
    function openViewer(photo,index){photoIndex=index;++viewSequence;rememberViewerPage(listing);resetViewerImage(photo);loadViewerStill(photo,index,viewSequence);}
    rememberViewerPage(listing);`,context);
  const run=code=>vm.runInContext(code,context);
  const entry=id=>run(`previews.entries.get('/api/image?id=${id}&size=preview')`);
  const open=index=>run(`openViewer(listing.photos[${index}],${index})`);
  return {run,entry,open,elements,created,requests,pages,timers,thumb};
}

test('paint a thumbnail first, decode detail, then preload previous and next',async()=>{
  const s=setup();s.open(1);
  const thumb=s.elements.get('viewer-image');assert.match(thumb.src,/id=2&size=thumb/);
  await thumb.finish();assert.equal(thumb.hidden,false);
  assert.equal(s.entry(2).image.fetchPriority,'high');
  await s.entry(2).image.finish();
  assert.equal(s.elements.get('viewer-image'),s.entry(2).image);
  assert.equal(s.elements.get('viewer-loading').hidden,true);
  for(const id of [1,3]){assert.match(s.entry(id).image.src,/priority=background/);assert.equal(s.entry(id).image.fetchPriority,'low');}
  assert.equal(s.run('previews.entries.size'),3);
});
test('reuse decoded neighbours immediately, without another request or thumbnail flash',async()=>{
  const s=setup();s.open(1);await s.entry(2).image.finish();
  await s.entry(1).image.finish();await s.entry(3).image.finish();
  const next=s.entry(3).image,previous=s.entry(2).image,count=s.requests.length;
  s.open(2);assert.equal(s.elements.get('viewer-image'),next);assert.equal(s.requests.length,count);
  s.open(1);assert.equal(s.elements.get('viewer-image'),previous);
  assert.equal(s.requests.filter(url=>url.includes('id=2&size=preview')).length,1);
  assert.equal(previous.src,'/api/image?id=2&size=preview');
  assert.ok(s.run('previews.entries.size')<=3);
});
test('promote an unfinished background render and ignore its old load callback',async()=>{
  const s=setup();s.run('previews.acquire(listing.photos[1],true)');
  const entry=s.entry(2),oldLoad=entry.image.onload;s.open(1);
  assert.equal(s.entry(2),entry);assert.equal(entry.image.fetchPriority,'high');
  assert.equal(entry.image.src,'/api/image?id=2&size=preview');
  await oldLoad();assert.equal(entry.ready,false);
  await entry.image.finish();assert.equal(s.elements.get('viewer-image'),entry.image);
});
test('a request completing after navigation cannot replace the current photo',async()=>{
  const s=setup();s.open(0);const old=s.entry(1).image;
  s.open(1);await old.finish();assert.notEqual(s.elements.get('viewer-image'),old);
  await s.entry(2).image.finish();assert.equal(s.elements.get('viewer-image').alt,'photo2.jpg');
});
test('closing or locking clears loaded images, pending requests, and page fetches',async()=>{
  let signal;
  const s=setup(page(2,[3,4],6,2),(url,options)=>{signal=options.signal;return new Promise(()=>{});});
  s.open(0);await s.entry(3).image.finish();const images=[s.entry(3).image,s.entry(4).image];
  s.run('++viewSequence;clearViewerPreloads()');await flush();
  assert.equal(signal.aborted,true);assert.equal(s.run('viewerPages.size'),0);assert.equal(s.run('previews.entries.size'),0);
  for(const image of images){assert.equal(image.src,'');assert.equal(image.onload,null);}
});
for(const direction of [-1,1])test(`prefetch and reuse across the ${direction<0?'previous':'next'} page boundary`,async()=>{
  const target=direction<0?1:3,id=direction<0?2:5,index=direction<0?0:1;
  const s=setup(page(2,[3,4],6,2),async url=>page(Number(new URLSearchParams(url.split('?')[1]).get('page')),target===1?[1,2]:[5,6],6,2));
  s.open(index);await s.entry(index===0?3:4).image.finish();await flush();
  assert.equal(s.pages.length,1);assert.match(s.pages[0],new RegExp('page='+target));
  const image=s.entry(id).image;await image.finish();
  await s.run(`stepPhoto(${direction})`);await flush();
  assert.equal(s.pages.length,1,'page metadata was downloaded twice');
  assert.equal(s.run('listing.page'),target);assert.equal(s.run('state.page'),target);
  assert.equal(s.elements.get('viewer-image'),image);assert.equal(s.run('gridDirty'),true);
  assert.ok(s.run('viewerPages.size')<=3);assert.ok(s.run('previews.entries.size')<=3);
});
test('a late page response after close/reopen cannot change the new viewer',async()=>{
  let resolve;
  const s=setup(page(1,[1,2],4,2),()=>new Promise(r=>{resolve=r;}));
  s.open(1);const navigation=s.run('stepPhoto(1)');
  s.run('++viewSequence;clearViewerPreloads()');s.open(0);
  resolve(page(2,[3,4],4,2));await navigation;await flush();
  assert.equal(s.run('listing.page'),1);assert.equal(s.run('state.page'),1);
});
test('video neighbours and out-of-page bookmarks never trigger image preloading',async()=>{
  const s=setup();s.run('listing.photos[0].kind="video";listing.photos[2].kind="video"');
  s.open(1);await s.entry(2).image.finish();assert.equal(s.run('previews.entries.size'),1);
  await s.run('prefetchNeighbours(-1,viewSequence)');assert.equal(s.pages.length,0);
});
test('speculative failures retry when opened and the buffer remains bounded',async()=>{
  const s=setup();s.open(1);await s.entry(2).image.finish();const failed=s.entry(3);
  failed.image.onerror();await flush();s.open(2);
  assert.notEqual(s.entry(3),failed);assert.equal(s.entry(3).image.fetchPriority,'high');
  await s.entry(3).image.finish();assert.equal(s.elements.get('viewer-image').alt,'photo3.jpg');
  assert.ok(s.run('previews.entries.size')<=3);
});
test('page cache discards and aborts pages outside the current neighbourhood',()=>{
  const s=setup();s.run('let cancelled=false;viewerPages.set(1,{controller:{abort(){cancelled=true;}}});rememberViewerPage({page:3});');
  assert.equal(s.run('cancelled'),true);assert.equal(s.run('viewerPages.has(1)'),false);
});
test('grid refresh retains matching cards and cancels only discarded requests',()=>{
  const fn=source.slice(source.indexOf('function replaceCards('),source.indexOf('async function loadListing()'));
  let cancelled=0;const retained={querySelectorAll:()=>[{removeAttribute:()=>{throw Error('retained image cancelled');}}]};const discarded={querySelectorAll:()=>[{removeAttribute:()=>cancelled++}]};
  const container={children:[retained,discarded],replaceChildren(...cards){this.children=cards;}};
  const context=vm.createContext({container,retained});vm.runInContext(fn+';replaceCards(container,[retained]);',context);assert.equal(cancelled,1);assert.deepEqual(container.children,[retained]);
});
