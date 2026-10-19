const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
const viewerFunctions=source.slice(source.indexOf('function clearViewerPreloads(){'),source.indexOf('function stopVideo()'));

function setup(){
  const elements=new Map();const created=[];
  class Image{
    constructor(){this.isConnected=false;this.hidden=true;this.src='';created.push(this);}
    removeAttribute(name){if(name==='src')this.src='';}
    replaceWith(other){this.isConnected=false;other.isConnected=true;elements.set('viewer-image',other);}
  }
  const thumb=new Image();thumb.isConnected=true;
  elements.set('viewer-image',thumb);elements.set('viewer',{open:true});elements.set('viewer-loading',{hidden:false});elements.set('viewer-error',{hidden:true});
  const context=vm.createContext({Image,$:id=>elements.get(id),imageURL:(p,size='thumb')=>`/api/image?id=${p.id}&size=${size}`,setTimeout:()=>{}});
  vm.runInContext(`let viewerPreview=null,neighbourPreview=null,viewSequence=1; const listing={photos:[{id:1,kind:'image',name:'one.jpg'},{id:2,kind:'raw'},{id:3,kind:'image'}]}; ${viewerFunctions}`,context);
  return {context,elements,created,thumb};
}
test('viewer paints the thumbnail before the detailed preview and prefetches only one neighbour',()=>{
  const {context,elements,created,thumb}=setup();vm.runInContext('loadViewerStill(listing.photos[0],0,1)',context);
  assert.match(thumb.src,/size=thumb/);assert.match(created[1].src,/size=preview/);assert.equal(created[1].fetchPriority,'high');
  thumb.onload();assert.equal(thumb.hidden,false);assert.equal(elements.get('viewer-loading').hidden,false);
  created[1].onload();assert.equal(elements.get('viewer-image'),created[1]);assert.equal(elements.get('viewer-loading').hidden,true);
  assert.equal(created.length,3);assert.match(created[2].src,/id=2.*priority=background/);assert.equal(created[2].fetchPriority,'low');
  vm.runInContext('clearViewerPreloads()',context);assert.equal(created[2].src,'');assert.equal(created[2].onload,null);assert.match(created[1].src,/size=preview/);
});
test('a completed request for an old viewer image cannot replace the current image',()=>{
  const {context,elements,created,thumb}=setup();vm.runInContext('loadViewerStill(listing.photos[0],0,1);viewSequence=2',context);created[1].onload();assert.equal(elements.get('viewer-image'),thumb);assert.equal(created.length,2);
});
test('bookmarked photos outside the current page and video neighbours are not prefetched',()=>{
  const {context,created}=setup();vm.runInContext('prefetchNeighbour(-1);listing.photos[1].kind="video";prefetchNeighbour(0)',context);assert.equal(created.length,1);
});
test('grid refresh retains matching cards and cancels only discarded image requests',()=>{
  const fn=source.slice(source.indexOf('function replaceCards('),source.indexOf('async function loadListing()'));
  let cancelled=0;const retained={querySelectorAll:()=>[{removeAttribute:()=>{throw Error('retained image cancelled');}}]};const discarded={querySelectorAll:()=>[{removeAttribute:()=>cancelled++}]};
  const container={children:[retained,discarded],replaceChildren(...cards){this.children=cards;}};
  const context=vm.createContext({container,retained});vm.runInContext(fn+';replaceCards(container,[retained]);',context);assert.equal(cancelled,1);assert.deepEqual(container.children,[retained]);
});
