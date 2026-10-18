'use strict';
const $ = id => document.getElementById(id);
const state = { folder:'', q:'', scope:'tree', sort:'name', tag:'', page:1 };
let listing = null, activePhoto = null, photoIndex = -1, listingRequest = null, viewerRequest = null, viewSequence = 0, pageBusy = false;
let scanState = null, lastRefresh = 0, firstLoad = true;
const number = new Intl.NumberFormat();
const folderSVG = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/></svg>';

function el(tag, cls, text) { const e = document.createElement(tag); if (cls) e.className=cls; if (text !== undefined) e.textContent=text; return e; }
function params(overrides={}) { const p=new URLSearchParams(); for(const [k,v] of Object.entries({...state,...overrides})) if(v!=='' && v!==null) p.set(k,String(v)); return p; }
function linkFor(folder) { return '/?'+params({folder,q:'',tag:'',page:1,image:null}); }
function imageURL(p,size='thumb') { return '/api/image?'+new URLSearchParams({id:p.id,size,v:`${p.mtime}-${p.size}-${p.orientation}`}); }
async function api(path, options={}) { const response=await fetch(path,options); const data=await response.json(); if(!response.ok) {const error=new Error(data.error || 'The server could not complete this request.');error.status=response.status;if(response.status===401&&path!=='/api/login')lockGallery();throw error;} return data; }
function writeURL(push=false) { const p=params(); if(activePhoto && $('viewer').open) p.set('image',activePhoto.id); history[push?'pushState':'replaceState'](null,'','/?'+p); }
function readURL() { const p=new URLSearchParams(location.search); for(const key of ['folder','q','tag']) state[key]=p.get(key)||''; state.scope=['tree','folder','all'].includes(p.get('scope'))?p.get('scope'):'tree'; state.sort=p.get('sort')==='date'?'date':'name'; state.page=Math.max(1,Math.min(10000000,parseInt(p.get('page')||'1',10)||1)); }
function syncControls() { closeSuggestions(); $('search').value=state.q; $('scope').value=state.scope; $('sort').value=state.sort; $('tag-filter').hidden=!state.tag; $('clear-tag').textContent=state.tag+' ×'; }
function showError(message) { $('error').textContent=message; $('error').hidden=false; }
async function navigate(changes={},push=true) { closeViewer(false); Object.assign(state,changes); state.page=changes.page||1; syncControls(); writeURL(push); await loadListing(); }

function loadThumbnail(img,photo,placeholder) {
  img.loading='lazy'; img.decoding='async'; img.alt=''; let attempts=0;
  img.addEventListener('error',()=>{ if(!img.isConnected) return; if(attempts++<3) { setTimeout(()=>{if(img.isConnected) img.src=imageURL(photo)+'&retry='+attempts;},1200*attempts); } else { img.hidden=true; if(placeholder) placeholder.textContent='Preview unavailable'; }});
  img.src=imageURL(photo);
}

function renderBreadcrumbs() {
  const nav=$('breadcrumbs'); nav.replaceChildren(); const root=el('a','', 'Library'); root.href=linkFor(''); root.addEventListener('click',e=>{e.preventDefault();navigate({folder:'',q:'',tag:''});}); nav.append(root);
  let accumulated=''; const parts=state.folder.split('/').filter(Boolean);
  parts.forEach((part,index)=>{nav.append(el('span','', '/')); accumulated+=(accumulated?'/':'')+part; const folder=accumulated; const a=el('a',index===parts.length-1?'crumb-current':'',part); a.href=linkFor(folder); if(index===parts.length-1) a.setAttribute('aria-current','page'); a.addEventListener('click',e=>{e.preventDefault();navigate({folder,q:'',tag:''});});nav.append(a);});
}

function renderListing() {
  renderBreadcrumbs(); const searching=Boolean(state.q||state.tag); const name=state.folder.split('/').pop(); $('title').textContent=searching?'Search results':name||'Library';
  const scope=state.scope==='all'?'the entire library':state.folder||'your library';
  $('subtitle').textContent=searching?`${number.format(listing.total)} matching items in ${scope}.`:`${number.format(listing.folder_total)} ${listing.folder_total===1?'folder':'folders'} · ${number.format(listing.total)} ${listing.total===1?'image':'images'}`;
  $('folders').replaceChildren(); $('photos').replaceChildren(); $('folders-section').hidden=!listing.folders.length; $('photos-section').hidden=!listing.photos.length;
  $('folder-count').textContent=number.format(listing.folder_total); $('photo-count').textContent=number.format(listing.total);
  for(const folder of listing.folders) {
    const card=el('a','folder-card');card.href=linkFor(folder.path);card.addEventListener('click',e=>{e.preventDefault();navigate({folder:folder.path,q:'',tag:''});});
    const cover=el('div','folder-cover'); if(folder.cover){const img=el('img');loadThumbnail(img,{id:folder.cover,mtime:0,size:0,orientation:0});cover.append(img);} else {cover.innerHTML=folderSVG;}
    const icon=el('span','folder-icon');icon.innerHTML=folderSVG;cover.append(icon);const text=el('div','folder-text');const label=el('div'); label.append(el('strong','',folder.name),el('small','',number.format(folder.count)+' items'));text.append(label,el('span','folder-arrow','↗'));card.append(cover,text);$('folders').append(card);
  }
  listing.photos.forEach((photo,index)=>{
    const card=el('article','photo-card');const button=el('button','photo-open');button.setAttribute('aria-label','Open '+photo.name);const img=el('img');const placeholder=el('span','photo-placeholder');loadThumbnail(img,photo,placeholder);button.append(img,placeholder);button.addEventListener('click',()=>openViewer(photo,index));
    if(photo.kind&&photo.kind!=='image')button.append(el('span','media-badge',photo.kind==='video'?'▶ VIDEO':photo.kind.toUpperCase()));
    const name=el('span','photo-name',photo.name);name.title=photo.name;const meta=el('div','photo-meta');meta.append(el('span','',photo.tags.length?photo.tags.slice(0,2).join(' · '):new Date(photo.taken*1000).toLocaleDateString(undefined,{year:'numeric',month:'short',day:'numeric'})),el('span',photo.error?'photo-warning':'',photo.error?'Metadata warning':photo.width&&photo.height?`${photo.width} × ${photo.height}`:''));card.append(button,name,meta);$('photos').append(card);
  });
  $('empty').hidden=listing.photos.length>0||listing.folders.length>0;
  $('empty-title').textContent=searching?'No matching media':scanState?.running?'Indexing library…':'No media in this folder';
  $('empty-message').textContent=searching?'Try another filename or tag, or search the entire library.':scanState?.running?'The first scan is running. Media will appear as it is indexed.':'This folder contains no indexed media. Check the configured library path or run a scan from Library status.';
  $('empty-action').hidden=!searching;$('pagination').hidden=listing.pages<=1;$('previous').disabled=listing.page<=1;$('next').disabled=listing.page>=listing.pages;$('page-label').textContent=`${listing.page} / ${listing.pages}`;
}

async function loadListing() {
  if(listingRequest) listingRequest.abort();const controller=new AbortController();listingRequest=controller;
  $('error').hidden=true; if(firstLoad) $('loading').hidden=false;
  try {const data=await api('/api/browse?'+params(),{signal:controller.signal}); if(controller.signal.aborted) return; listing=data;state.page=data.page;writeURL();renderListing();firstLoad=false;document.title=(state.folder?state.folder.split('/').pop():'Photo library')+' · Folderlight';}
  catch(e) {if(e.name!=='AbortError') showError(e.message);}
  finally {if(listingRequest===controller){$('loading').hidden=true;listingRequest=null;}}
}

function renderDetails(photo) {
  $('metadata').replaceChildren();const date=new Date(photo.taken*1000).toLocaleString();const rows=[['Filename',photo.name],['Folder',photo.folder||'Library root'],['Captured',date],['Dimensions',`${photo.width} × ${photo.height} px`],['File size',(photo.size/(1024*1024)).toFixed(2)+' MiB']];if(photo.error) rows.push(['Index warning',photo.error]);
  for(const [key,value] of rows) $('metadata').append(el('dt','',key),el('dd','',value));
  $('viewer-tags').replaceChildren();if(!photo.tags.length) $('viewer-tags').append(el('span','muted small','No embedded tags'));
  for(const tag of photo.tags) {const button=el('button','tag',tag);button.addEventListener('click',()=>navigate({tag:'',q:exactTag(tag),page:1}));$('viewer-tags').append(button);}
  $('open-folder').onclick=()=>navigate({folder:photo.folder,q:'',tag:'',page:1});
}

async function openViewer(photo,index=-1) {
  const sequence=++viewSequence;activePhoto=photo;photoIndex=index;
  if(viewerRequest) viewerRequest.abort();viewerRequest=new AbortController();const signal=viewerRequest.signal;
  $('viewer-title').textContent=photo.name;$('viewer-position').textContent=index>=0?`Item ${number.format((listing.page-1)*listing.limit+index+1)} of ${number.format(listing.total)}`:'Media item';
  $('viewer-prev').disabled=index<0||(index===0&&listing.page===1);$('viewer-next').disabled=index<0||(index===listing.photos.length-1&&listing.page===Math.ceil(listing.total/listing.limit));
  stopVideo();renderDetails(photo);$('viewer-loading').textContent='Loading preview…';$('viewer-loading').hidden=false;$('viewer-error').hidden=true;$('viewer-image').hidden=true;$('viewer-image').removeAttribute('src');$('viewer-image').alt=photo.name;
  if(!$('viewer').open) $('viewer').showModal();writeURL();
  const img=$('viewer-image');let attempts=0;
  img.onload=()=>{if(sequence!==viewSequence)return;$('viewer-loading').hidden=true;img.hidden=false;};
  img.onerror=()=>{if(sequence!==viewSequence||!$('viewer').open)return;if(attempts++<2){setTimeout(()=>{if(sequence===viewSequence&&$('viewer').open)img.src=imageURL(photo,'preview')+'&retry='+attempts;},attempts*1500);}else{$('viewer-loading').hidden=true;$('viewer-error').textContent='This preview could not be generated. The file may be unsupported, damaged, changed since the scan, or above the configured pixel limit.';$('viewer-error').hidden=false;}};
  if(photo.kind==='video')prepareVideo(photo,sequence,signal);else img.src=imageURL(photo,'preview');
  // Fetch fresh details for bookmarked photos and after metadata updates.
  try {const fresh=await api('/api/photo?id='+photo.id,{signal});if(sequence===viewSequence){activePhoto=fresh;renderDetails(fresh);}} catch(e){if(e.name!=='AbortError'&&sequence===viewSequence){$('viewer-error').textContent=e.message;$('viewer-error').hidden=false;}}
}
function stopVideo(){const video=$('viewer-video');video.pause();video.removeAttribute('src');video.removeAttribute('poster');video.load();video.hidden=true;}
async function prepareVideo(photo,sequence,signal){
  $('viewer-loading').textContent='Preparing video preview… First playback can take a few minutes.';
  try{
    while(!signal.aborted&&sequence===viewSequence){
      const result=await api('/api/video?'+new URLSearchParams({id:photo.id,prepare:1}),{signal});
      if(sequence!==viewSequence||signal.aborted)return;
      if(result.ready){const video=$('viewer-video');video.poster=imageURL(photo);video.src='/api/video?'+new URLSearchParams({id:photo.id});video.hidden=false;$('viewer-loading').hidden=true;return;}
      await new Promise(resolve=>{const timer=setTimeout(done,2000);function done(){clearTimeout(timer);signal.removeEventListener('abort',done);resolve();}signal.addEventListener('abort',done,{once:true});});
    }
  }catch(e){if(e.name!=='AbortError'&&sequence===viewSequence){$('viewer-loading').hidden=true;$('viewer-error').textContent=e.message;$('viewer-error').hidden=false;}}
}
function closeViewer(updateURL=true) { if(!$('viewer').open) return; ++viewSequence;viewerRequest?.abort();stopVideo();$('viewer').close();$('viewer-image').removeAttribute('src');activePhoto=null;photoIndex=-1;if(updateURL)writeURL(); }
async function stepPhoto(direction) {
  if(pageBusy||photoIndex<0||!listing) return;const next=photoIndex+direction;
  if(next>=0&&next<listing.photos.length){openViewer(listing.photos[next],next);return;}
  const nextPage=listing.page+direction;if(nextPage<1||nextPage>Math.ceil(listing.total/listing.limit))return;pageBusy=true;
  try {const result=await api('/api/browse?'+params({page:nextPage}));if(!$('viewer').open)return;listing=result;state.page=result.page;renderListing();const index=direction>0?0:listing.photos.length-1;if(index>=0)openViewer(listing.photos[index],index);}catch(e){$('viewer-error').textContent=e.message;$('viewer-error').hidden=false;}finally{pageBusy=false;}
}

async function updateStatus() {
  if(!$('login-screen').hidden){setTimeout(updateStatus,8000);return;}
  try {const previous=scanState;scanState=await api('/api/status');const st=scanState;if(st.running||previous?.running||previous?.started!==st.started)suggestionCache.clear();
    $('scan-label').textContent=st.running?'Scanning…':'Settings';$('status-dot').className='status-dot'+(st.running?' scanning':st.errors?' issue':'');$('scan-title').textContent=st.running?'Finding your media':'Library status';$('scan-message').textContent=st.message||'Ready to scan';$('scan-counts').textContent=`${number.format(st.visited)} checked · ${number.format(st.updated)} updated · ${number.format(st.errors)} warnings`;$('scan-current').textContent=st.current||'';$('rescan').disabled=st.running;$('force').disabled=st.running;
    if(!listingRequest&&!$('viewer').open&&((previous?.running&&!st.running)||(st.running&&Date.now()-lastRefresh>3500&&st.updated!==previous?.updated))){lastRefresh=Date.now();loadListing();}
  }catch(e){$('scan-label').textContent='Connection unavailable';$('scan-message').textContent=e.message;}
  setTimeout(updateStatus,scanState?.running?1500:8000);
}

// A bounded response cache keeps repeated completions instant without loading
// the library's complete tag dictionary into the browser.
const suggestionCache = new Map();
let suggestionTimer = null, suggestionRequest = null, suggestionSequence = 0;
let suggestions = [], suggestionIndex = -1, suggestionContext = null;
function exactTag(tag) { return 'tag:' + JSON.stringify(tag); }
function submitSearch() {
  const q=$('search').value.trim();closeSuggestions();
  navigate({q,scope:$('scope').value,sort:$('sort').value});
}
function completionContext() {
  const input=$('search'), value=input.value, caret=input.selectionStart??value.length;
  const tokens=/(?:tag:"(?:\\.|[^"\\])*(?:"|$)|[^\s]+)/giu;
  let match, start=caret, end=caret, token='';
  while((match=tokens.exec(value))!==null) {
    if(match.index<=caret&&caret<=match.index+match[0].length){start=match.index;end=match.index+match[0].length;token=value.slice(start,caret);break;}
  }
  let prefix=token.replace(/^tag:/iu,'');
  if(prefix.startsWith('"')) {
    try { prefix=JSON.parse(prefix); }
    catch { prefix=prefix.slice(1);try{prefix=JSON.parse('"'+prefix+'"');}catch{prefix=prefix.replace(/\\(["\\])/g,'$1');} }
  }
  return {prefix,start,end,value};
}
function closeSuggestions() {
  clearTimeout(suggestionTimer);suggestionRequest?.abort();suggestionSequence++;
  suggestions=[];suggestionIndex=-1;suggestionContext=null;
  $('tag-suggestions').hidden=true;$('search').setAttribute('aria-expanded','false');$('search').removeAttribute('aria-activedescendant');
}
function highlightSuggestion(index) {
  suggestionIndex=index;
  for(const [i,option] of [...$('tag-options').children].entries())option.setAttribute('aria-selected',String(i===index));
  if(index>=0)$('search').setAttribute('aria-activedescendant','tag-option-'+index);else $('search').removeAttribute('aria-activedescendant');
}
function chooseSuggestion(index) {
  const tag=suggestions[index];if(!tag||!suggestionContext)return;
  const c=suggestionContext;
  const value=c.value.slice(0,c.start)+exactTag(tag.tag)+c.value.slice(c.end);
  if(value.length>512){$('suggestion-status').textContent='This search is too long. Remove a term first.';return;}
  $('search').value=value;submitSearch();$('search').focus();
}
function renderSuggestions(tags,context) {
  suggestions=tags;suggestionContext=context;suggestionIndex=-1;
  $('tag-options').replaceChildren();$('search').removeAttribute('aria-activedescendant');
  for(const [index,tag] of tags.entries()){
    const row=el('div','tag-option');row.id='tag-option-'+index;row.setAttribute('role','option');row.setAttribute('aria-selected','false');
    row.append(el('span','tag-symbol','#'),el('span','tag-name',tag.tag),el('span','tag-hits',number.format(tag.count)+(tag.count===1?' image':' images')));
    row.addEventListener('pointerdown',e=>e.preventDefault());row.addEventListener('click',()=>chooseSuggestion(index));$('tag-options').append(row);
  }
  $('suggestion-status').textContent=tags.length?'':context.prefix?'No tags start with this text. Press Enter to search filenames and tag text.':'No embedded tags in this search scope yet.';
}
async function showSuggestions() {
  if(document.activeElement!==$('search'))return;
  suggestionRequest?.abort();const sequence=++suggestionSequence;
  const context=completionContext(),scope=$('scope').value;
  $('suggestion-scope').textContent=scope==='all'?'Entire library':scope==='folder'?'This folder':'Folder + subfolders';
  $('tag-suggestions').hidden=false;$('search').setAttribute('aria-expanded','true');
  renderSuggestions([],context);$('suggestion-status').textContent='Loading tags…';
  const key=JSON.stringify([state.folder,scope,context.prefix.normalize('NFC').toLowerCase()]);
  const cached=suggestionCache.get(key);
  if(cached&&Date.now()-cached.time<30000){renderSuggestions(cached.tags,context);return;}
  const controller=new AbortController();suggestionRequest=controller;
  try {
    const data=await api('/api/tags?'+new URLSearchParams({folder:state.folder,scope,prefix:context.prefix,limit:8}),{signal:controller.signal});
    if(sequence!==suggestionSequence||document.activeElement!==$('search'))return;
    suggestionCache.delete(key);suggestionCache.set(key,{time:Date.now(),tags:data.tags});
    while(suggestionCache.size>32)suggestionCache.delete(suggestionCache.keys().next().value);
    renderSuggestions(data.tags,context);
  }catch(e){if(e.name!=='AbortError'&&sequence===suggestionSequence)$('suggestion-status').textContent='Tag suggestions unavailable. You can still submit a search.';}
}
$('search').addEventListener('focus',()=>showSuggestions());
$('search').addEventListener('input',()=>{clearTimeout(suggestionTimer);suggestionRequest?.abort();suggestionSequence++;suggestions=[];suggestionContext=null;highlightSuggestion(-1);$('tag-options').replaceChildren();$('suggestion-status').textContent='Loading tags…';suggestionTimer=setTimeout(showSuggestions,120);});
$('search').addEventListener('search',()=>{if(!$('search').value)submitSearch();});
$('search').addEventListener('keydown',async e=>{
  if(e.key==='Escape'){e.preventDefault();closeSuggestions();return;}
  if(e.key==='Tab'){closeSuggestions();return;}
  if(e.key==='ArrowDown'||e.key==='ArrowUp'){
    e.preventDefault();if($('tag-suggestions').hidden)await showSuggestions();
    if(suggestions.length)highlightSuggestion(e.key==='ArrowDown'?(suggestionIndex+1)%suggestions.length:suggestionIndex<0?suggestions.length-1:(suggestionIndex-1+suggestions.length)%suggestions.length);
  }else if(e.key==='Enter'&&suggestionIndex>=0&&!$('tag-suggestions').hidden){e.preventDefault();chooseSuggestion(suggestionIndex);}
});
document.addEventListener('pointerdown',e=>{if(!e.target.closest('.search-wrap'))closeSuggestions();});
$('search').addEventListener('blur',()=>setTimeout(()=>{if(document.activeElement!==$('search'))closeSuggestions();},120));

$('search-form').addEventListener('submit',e=>{e.preventDefault();submitSearch();});
$('scope').addEventListener('change',()=>{suggestionCache.clear();navigate({scope:$('scope').value,q:$('search').value.trim()});});$('sort').addEventListener('change',()=>navigate({sort:$('sort').value}));
function clearFilters(){navigate({q:'',tag:''});} $('clear-tag').onclick=()=>navigate({tag:''});$('clear-search').onclick=clearFilters;$('empty-action').onclick=clearFilters;
$('previous').onclick=()=>navigate({page:state.page-1});$('next').onclick=()=>navigate({page:state.page+1});
$('scan-toggle').onclick=()=>{$('scan-panel').hidden=!$('scan-panel').hidden;$('scan-toggle').setAttribute('aria-expanded',String(!$('scan-panel').hidden));if(!$('scan-panel').hidden)loadSettings().catch(e=>$('password-message').textContent=e.message);};$('scan-close').onclick=()=>{$('scan-panel').hidden=true;$('scan-toggle').setAttribute('aria-expanded','false');$('scan-toggle').focus();};
$('rescan').onclick=async()=>{try{$('rescan').disabled=true;scanState=await api('/api/rescan?force='+($('force').checked?'1':'0'),{method:'POST',headers:{'X-Folderlight-Request':'1'}});$('scan-message').textContent='Scan started. You can keep browsing.';}catch(e){$('scan-message').textContent=e.message;}finally{$('rescan').disabled=Boolean(scanState?.running);}};
$('viewer-close').onclick=()=>closeViewer();$('viewer').addEventListener('cancel',e=>{e.preventDefault();closeViewer();});$('viewer-prev').onclick=()=>stepPhoto(-1);$('viewer-next').onclick=()=>stepPhoto(1);
$('info-toggle').onclick=()=>{const show=$('details').hidden;$('details').hidden=!show;$('info-toggle').setAttribute('aria-expanded',String(show));};
document.addEventListener('keydown',e=>{if(!$('viewer').open||e.target.matches('input,select,textarea,video'))return;if(e.key==='ArrowLeft'){e.preventDefault();stepPhoto(-1);}if(e.key==='ArrowRight'){e.preventDefault();stepPhoto(1);}});
let touch=null;$('image-stage').addEventListener('touchstart',e=>{touch=e.touches.length===1?{x:e.touches[0].clientX,y:e.touches[0].clientY}:null;},{passive:true});$('image-stage').addEventListener('touchend',e=>{if(!touch||!e.changedTouches.length)return;const dx=e.changedTouches[0].clientX-touch.x,dy=e.changedTouches[0].clientY-touch.y;touch=null;if(Math.abs(dx)>60&&Math.abs(dx)>Math.abs(dy)*1.5)stepPhoto(dx<0?1:-1);},{passive:true});
async function restore() {closeViewer(false);readURL();syncControls();const id=Number(new URLSearchParams(location.search).get('image'));await loadListing();if(id>0){try{const index=listing?.photos.findIndex(p=>p.id===id)??-1;const photo=index>=0?listing.photos[index]:await api('/api/photo?id='+id);openViewer(photo,index);}catch(e){showError(e.message);}}}
let gallerySettings=null;
function lockGallery(){closeViewer(false);listingRequest?.abort();$('scan-panel').hidden=true;$('scan-toggle').setAttribute('aria-expanded','false');$('content').hidden=true;$('app-header').hidden=true;$('login-screen').hidden=false;document.title='Sign in · Folderlight';$('folders').replaceChildren();$('photos').replaceChildren();suggestionCache.clear();$('login-password').focus();}
function showGallery(){ $('login-screen').hidden=true;$('content').hidden=false;$('app-header').hidden=false; }
function renderSettings(settings){gallerySettings=settings;$('app-version').textContent='Folderlight '+settings.version;$('login-version').textContent='Folderlight '+settings.version;$('password-status').textContent=settings.password_enabled?'Password protection is enabled for the whole gallery.':'Password protection is off. Anyone who can reach this server can open the gallery.';$('disable-password').hidden=!settings.password_enabled;$('logout').hidden=!settings.password_enabled;const t=settings.media_tools;if(t)$('media-status').textContent=`Video: ${t.ffmpeg&&t.ffprobe?'ready':'install ffmpeg'} · HEIC: ${t['heif-thumbnailer']&&t.exiftool?'ready':'install libheif-tools + exiftool'} · RAW: ${t.dcraw_emu&&t.exiftool?'ready':'install libraw-tools + exiftool'}`;}
async function loadSettings(){const settings=await api('/api/settings');renderSettings(settings);if(!settings.authenticated)lockGallery();return settings;}
const mutationHeaders={'X-Folderlight-Request':'1','Content-Type':'application/json'};
$('login-form').onsubmit=async e=>{e.preventDefault();$('login-submit').disabled=true;$('login-error').textContent='';try{await api('/api/login',{method:'POST',headers:mutationHeaders,body:JSON.stringify({password:$('login-password').value})});$('login-password').value='';await loadSettings();showGallery();await restore();}catch(err){$('login-error').textContent=err.message;}finally{$('login-submit').disabled=false;}};
async function savePassword(password){const settings=await api('/api/password',{method:'POST',headers:mutationHeaders,body:JSON.stringify({password})});renderSettings(settings);$('new-password').value='';$('password-message').textContent=password?'Gallery password saved. Other sessions have been signed out.':'Password protection removed.';}
$('password-form').onsubmit=async e=>{e.preventDefault();try{await savePassword($('new-password').value);}catch(err){$('password-message').textContent=err.message;}};
$('disable-password').onclick=async()=>{try{await savePassword('');}catch(err){$('password-message').textContent=err.message;}};
$('logout').onclick=async()=>{try{await api('/api/logout',{method:'POST',headers:mutationHeaders});lockGallery();}catch(err){$('password-message').textContent=err.message;}};
$('viewer-video').addEventListener('error',()=>{if($('viewer-video').getAttribute('src')){$('viewer-error').textContent='The video could not be played. Check your connection and try opening it again.';$('viewer-error').hidden=false;}});
window.addEventListener('popstate',()=>{if($('login-screen').hidden)restore();});
(async()=>{try{const settings=await loadSettings();if(settings.authenticated){showGallery();await restore();}}catch(e){showError(e.message);}updateStatus();})();
