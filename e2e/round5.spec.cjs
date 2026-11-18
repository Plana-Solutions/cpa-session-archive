const {test,expect}=require('@playwright/test');
async function open(page) {
 await page.addInitScript(()=>localStorage.setItem('cli-proxy-auth',JSON.stringify({state:{apiBase:'http://127.0.0.1:4173',managementKey:'mock-management-key'}})));
 await page.goto('/#/sessions');
 await expect(page.locator('.session-row').first()).toBeVisible();
}
test('HTTP without randomUUID still initializes',async({page})=>{
 await page.addInitScript(()=>Object.defineProperty(crypto,'randomUUID',{value:undefined}));
 await open(page);
});
test('context result retains original tool name and arguments',async({page})=>{
 await open(page);
 await page.evaluate(()=>{
  ensureRequestRecord=async()=>({original_request:btoa(JSON.stringify({input:[{type:'function_call',call_id:'context-call',name:'original_shell',arguments:'{"command":"original-input"}'}]})),response:btoa('{}')});
  api=async()=>[{response:btoa(JSON.stringify({output:[{type:'function_call_output',call_id:'context-call',output:'context-result'}]}))}];
  return showToolDetail('session','request','context-call');
 });
 await expect(page.locator('#subDetailTitle')).toHaveText('original_shell');
 await expect(page.locator('#subDetail')).toContainText('original-input');
 await expect(page.locator('#subDetail')).toContainText('context-result');
});
test('truncated previews offer full diagnostics',async({page})=>{
 await open(page);
 await page.evaluate(()=>document.querySelector('#subDetail').innerHTML=renderTimelineEntry({role:'assistant',text:'shortened',truncated:true}));
 await expect(page.locator('#subDetail button')).toBeVisible();
});
