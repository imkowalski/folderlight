'use strict';

function previewPixels(size){return {small:240,thumb:480,screen:1024,preview:1600}[size]||1600;}

class PreviewBuffer {
  constructor(urlFor,sizeFor=()=>'preview') { this.entries=new Map();this.urlFor=urlFor;this.sizeFor=sizeFor; }
  owns(image) { return [...this.entries.values()].some(entry=>entry.image===image); }
  acquire(photo, background=false, size=this.sizeFor()) {
    const key=this.urlFor(photo,'preview');
    let entry=this.entries.get(key);
    if(entry&&(entry.failed||previewPixels(entry.size)<previewPixels(size))) { this.discard(key,entry.image.isConnected);entry=null; }
    if(!entry) {
      entry={key,url:this.urlFor(photo,size),size,image:new Image(),ready:false,failed:false,background,ticket:0};
      entry.promise=new Promise((resolve,reject)=>{entry.resolve=resolve;entry.reject=reject;});
      entry.promise.catch(()=>{}); // Speculative failures are reported only if opened.
      this.entries.set(key,entry);this.start(entry,background);
    } else if(!background&&!entry.ready&&entry.background) {
      // Promote an unfinished background render into the server's visible queue.
      this.start(entry,false);
    }
    return entry;
  }
  start(entry,background) {
    const image=entry.image,ticket=++entry.ticket;
    entry.background=background;image.decoding='async';image.fetchPriority=background?'low':'high';
    const fail=error=>{if(ticket!==entry.ticket)return;entry.failed=true;entry.reject(error);};
    image.onload=async()=>{
      try { if(image.decode)await image.decode(); }
      catch(error) { fail(error);return; }
      if(ticket!==entry.ticket)return;
      entry.ready=true;entry.resolve(image);
    };
    image.onerror=()=>fail(new Error('The detailed preview could not be loaded.'));
    image.src=entry.url+(background?'&priority=background':'');
  }
  retain(photos) {
    const keep=new Set(photos.map(photo=>this.urlFor(photo,'preview')));
    for(const key of this.entries.keys())if(!keep.has(key))this.discard(key);
  }
  discard(key,keepVisible=false) {
    const entry=this.entries.get(key);if(!entry)return;
    this.entries.delete(key);++entry.ticket;entry.image.onload=null;entry.image.onerror=null;if(!keepVisible)entry.image.removeAttribute('src');
    const error=new Error('Preview cancelled');error.name='AbortError';entry.reject(error);
  }
  clear() { for(const key of this.entries.keys())this.discard(key); }
}

// Native lazy-loading may request several screens of images. Own the queue so
// a small viewport cannot fill every HTTP connection and the server's worker.
class ThumbnailScheduler {
  constructor(urlFor,{concurrency=3,margin=180}={}) {
    this.urlFor=urlFor;this.concurrency=concurrency;this.margin=margin;
    this.entries=new Map();this.active=0;this.paused=false;
    this.observer=typeof IntersectionObserver==='function'?new IntersectionObserver(changes=>{
      for(const change of changes){const entry=this.entries.get(change.target);if(!entry)continue;entry.near=change.isIntersecting;if(!entry.near)this.cancel(entry);}
      this.pump();
    },{rootMargin:`${margin}px 0px`}):null;
    if(!this.observer){const refresh=()=>this.pump();window.addEventListener('scroll',refresh,{passive:true});window.addEventListener('resize',refresh,{passive:true});}
  }
  track(image,photo,placeholder) {
    const entry={image,photo,placeholder,near:!this.observer,loading:false,done:false,attempts:0,ticket:0,timer:null};
    image.alt='';image.decoding='async';image.loading='eager';this.entries.set(image,entry);
    if(this.observer)this.observer.observe(image);else requestAnimationFrame(()=>this.pump());
  }
  distance(entry) {
    if(!entry.near||!entry.image.isConnected)return Infinity;
    const rect=entry.image.getBoundingClientRect(),height=window.innerHeight;
    if(rect.width<=0||rect.height<=0||rect.bottom < -this.margin||rect.top>height+this.margin)return Infinity;
    return Math.max(0,rect.top-height,-rect.bottom);
  }
  pump() {
    if(this.paused)return;
    while(this.active<this.concurrency){
      const entry=[...this.entries.values()].filter(e=>!e.loading&&!e.done&&!e.timer)
        .map(e=>({entry:e,distance:this.distance(e)})).filter(e=>Number.isFinite(e.distance))
        .sort((a,b)=>a.distance-b.distance)[0]?.entry;
      if(!entry)return;
      this.start(entry);
    }
  }
  start(entry) {
    const image=entry.image,ticket=++entry.ticket;entry.loading=true;this.active++;
    image.hidden=false;image.fetchPriority=this.distance(entry)===0?'high':'low';
    image.onload=()=>this.finish(entry,ticket,false);
    image.onerror=()=>this.finish(entry,ticket,true);
    image.src=this.urlFor(entry.photo);
  }
  finish(entry,ticket,failed) {
    if(ticket!==entry.ticket||!entry.loading)return;
    entry.loading=false;this.active--;entry.image.onload=null;entry.image.onerror=null;
    if(!failed){entry.done=true;entry.loadedURL=entry.image.getAttribute('src');}
    else if(entry.attempts++<2){entry.timer=setTimeout(()=>{entry.timer=null;this.pump();},1000*entry.attempts);}
    else {entry.done=true;entry.image.hidden=true;if(entry.placeholder)entry.placeholder.textContent='Preview unavailable';}
    this.pump();
  }
  cancel(entry) {
    if(!entry.loading)return;
    if(entry.image.complete&&entry.image.naturalWidth>0){this.finish(entry,entry.ticket,false);return;}
    ++entry.ticket;entry.loading=false;this.active--;entry.image.onload=null;entry.image.onerror=null;entry.image.removeAttribute('src');
  }
  pause(){this.paused=true;for(const entry of this.entries.values())this.cancel(entry);}
  resume(){this.paused=false;this.pump();}
  loadedURL(photo){for(const entry of this.entries.values())if(entry.photo.id===photo.id&&entry.loadedURL&&entry.image.naturalWidth>0)return entry.loadedURL;return null;}
  untrack(image){const entry=this.entries.get(image);if(!entry)return;this.cancel(entry);clearTimeout(entry.timer);this.observer?.unobserve(image);this.entries.delete(image);image.onload=null;image.onerror=null;image.removeAttribute('src');}
  clear(){this.pause();for(const image of this.entries.keys())this.untrack(image);}
}
