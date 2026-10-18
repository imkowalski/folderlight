const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(require('node:path').join(__dirname,'../web/app.js'),'utf8');
const contextSource=source.slice(source.indexOf('function completionContext() {'),source.indexOf('function closeSuggestions() {'));
function completion(value,caret=value.length){const context=vm.createContext({$:()=>({value,selectionStart:caret})});vm.runInContext(contextSource,context);return JSON.parse(JSON.stringify(context.completionContext()));}
for(const [value,prefix,start,end] of [
  ['tag:Æ','Æ',0,5],
  ['tag:"Family ho','Family ho',0,14],
  ['tag:"Family holiday" Lake','Lake',21,25],
  ['tag:Ærø ','',8,8],
  ['Lake tag:Æ','Æ',5,10],
  ['tag:"Say \\"hel','Say "hel',0,14],
])test('complete '+value,()=>{const c=completion(value);assert.equal(c.prefix,prefix);assert.equal(c.start,start);assert.equal(c.end,end);});
test('replace the whole active token when editing in the middle',()=>{const c=completion('Lake Ærø Summer',7);assert.equal(c.prefix,'Ær');assert.equal(c.start,5);assert.equal(c.end,8);assert.equal(c.value.slice(0,c.start)+'tag:"Ærø"'+c.value.slice(c.end),'Lake tag:"Ærø" Summer');});

test("complete a quoted tag ending in a backslash",()=>{const tag="a"+String.fromCharCode(92);assert.equal(completion("tag:"+JSON.stringify(tag)).prefix,tag);});
