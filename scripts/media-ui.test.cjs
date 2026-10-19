const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/media.js'),'utf8');
function setup(){
 let callback;const requests=[],timers=new Map();let timerID=0;
 class Observer{constructor(cb){callback=cb;}observe(){}unobserve(){}}
 const context=vm.createContext({IntersectionObserver:Observer,window:{innerHeight:844},setTimeout:fn=>{timers.set(++timerID,fn);return timerID;},clearTimeout:id=>timers.delete(id)});
 vm.runInContext(source+`;const queue=new ThumbnailScheduler(p=>'/api/image?id='+p.id+'&size=small',{concurrency:3});`,context);
 const queue=vm.runInContext('queue',context);
 const images=Array.from({length:60},(_,id)=>({id,isConnected:true,complete:false,naturalWidth:0,top:id<6?100+id*100:5000+id*100,
  getBoundingClientRect(){return {top:this.top,bottom:this.top+90,width:110,height:90};},
  set src(value){this.url=value;this.complete=false;requests.push(value);},get src(){return this.url;},
  getAttribute(){return this.url;},removeAttribute(){this.url='';this.naturalWidth=0;}}));
 for(const image of images)queue.track(image,{id:image.id},{textContent:''});
 const intersect=(items,visible=true)=>callback(items.map(target=>({target,isIntersecting:visible})));
 const finish=image=>{image.complete=true;image.naturalWidth=240;image.onload();};
 return {queue,images,requests,intersect,finish,timers};
}
test('only near-viewport thumbnails start, with at most three active requests',()=>{
 const s=setup();assert.equal(s.requests.length,0);s.intersect(s.images);
 assert.equal(s.requests.length,3);assert.equal(s.queue.active,3);
 s.finish(s.images[0]);s.finish(s.images[1]);s.finish(s.images[2]);
 assert.equal(s.requests.length,6);
 for(const image of s.images.slice(3,6))s.finish(image);
 assert.equal(s.queue.active,0);assert.equal(s.requests.length,6);
 assert.equal(s.queue.loadedURL({id:1}),'/api/image?id=1&size=small');
});
test('opening the viewer cancels grid requests and keeps all slots free until resume',()=>{
 const s=setup();s.intersect(s.images);const oldCallback=s.images[0].onload;
 s.queue.pause();assert.equal(s.queue.active,0);
 for(const image of s.images.slice(0,3))assert.equal(image.src,'');
 oldCallback();assert.equal(s.queue.active,0);
 s.intersect(s.images);assert.equal(s.requests.length,3);
 s.queue.resume();assert.equal(s.queue.active,3);assert.equal(s.requests.length,6);
});
test('fast scrolling cancels old work and prioritizes the newly visible rows',()=>{
 const s=setup();s.intersect(s.images.slice(0,6));
 for(const image of s.images.slice(0,6))image.top=-5000;
 for(const [i,image] of s.images.slice(30,36).entries())image.top=100+i*100;
 s.intersect(s.images.slice(0,6),false);s.intersect(s.images.slice(30,36));
 assert.equal(s.queue.active,3);
 assert.ok(s.requests.slice(-3).every(url=>/id=3[012]&/.test(url)));
});
test('clearing the gallery cancels retries and removes every tracked image',()=>{
 const s=setup();s.intersect(s.images.slice(0,6));s.images[0].onerror();assert.equal(s.timers.size,1);
 s.queue.clear();assert.equal(s.queue.entries.size,0);assert.equal(s.queue.active,0);assert.equal(s.timers.size,0);
 for(const image of s.images)assert.equal(image.onload,null);
});
