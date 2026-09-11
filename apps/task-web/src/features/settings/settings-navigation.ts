/** 只接受工作台内部路由；不使用 history.back 或外部传入的任意 URL。 */
export function settingsReturnTo(state: unknown): string {
  const value = state && typeof state === 'object' && 'returnTo' in state ? state.returnTo : null;
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\\\u0000-\u0020#]/.test(value)) return '/';
  try {
    const url = new URL(value, 'https://marshal.invalid');
    if (url.origin !== 'https://marshal.invalid' || url.pathname + url.search !== value) return '/';
    if (url.pathname === '/') return value;
    if (!/^\/tasks\/[^/]+(?:\/(?:team|graph|artifacts|activity))?$/.test(url.pathname)) return '/';
    const id = decodeURIComponent(url.pathname.split('/')[2]!);
    if (id === '.' || id === '..' || /[\\/\u0000-\u0020]/.test(id)) return '/';
    return value;
  } catch {
    return '/';
  }
}
