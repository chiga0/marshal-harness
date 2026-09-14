// S02 的明确静态、无脚本、无接收端验收边界；不是通用 HTML 安全分析器。
// 浏览器自己的 form 关联、disabled fieldset/legend、inert 与可见性决定可操作性。
export async function staticInvitationControls(page) {
  return page.locator('input,textarea,select,button').evaluateAll(elements => elements.filter(element => {
    if (!element.form || element.matches(':disabled') || element.closest('[inert]') ||
        !element.getClientRects().length || getComputedStyle(element).visibility === 'hidden') return false;
    if (element instanceof HTMLInputElement && element.type === 'hidden') return false;
    if (element instanceof HTMLTextAreaElement && element.readOnly) return false;
    // readonly input 仍可焦点/Enter 隐式提交；只有 disabled 才排除此路径。
    return true;
  }).map(element => ({tag: element.tagName.toLowerCase(), type: element.type ?? null,
    id: element.id.slice(0,128), formId: element.form.id.slice(0,128)})));
}
