const secretKey = /^(?:authorization|proxy[-_]?authorization|api[-_]?key|access[-_]?token|refresh[-_]?token|token|password|passwd|secret|client[-_]?secret)$/i;
function redact(value, depth) {
  if (depth > 8) return '[已隐藏嵌套文本]';
  // Prompts commonly embed JSON.stringify(materials), including JSON inside a
  // string. Decode data only; never execute a user policy or revive objects.
  try {
    const parsed = JSON.parse(value);
    if (parsed && typeof parsed === 'object') {
      const walk = (item, level) => {
        if (level > 8) return '[已隐藏嵌套文本]';
        if (typeof item === 'string') return redact(item, level + 1);
        if (Array.isArray(item)) return item.map(child => walk(child, level + 1));
        if (item && typeof item === 'object') return Object.fromEntries(Object.entries(item).map(([key, child]) => [key, secretKey.test(key) ? '[已隐藏]' : walk(child, level + 1)]));
        return item;
      };
      return JSON.stringify(walk(parsed, depth));
    }
    if (typeof parsed === 'string') return JSON.stringify(redact(parsed, depth + 1));
  } catch { /* Ordinary prompt prose follows the same bounded text rules. */ }
  const decoded = value.replace(/"(?:\\[\s\S]|[^"\\])*"/g, token => {
    if (!token.includes('\\')) return token;
    try {return JSON.stringify(redact(JSON.parse(token), depth + 1));} catch {return token;}
  });
  return decoded.replace(/-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?(?:-----END [^-]*PRIVATE KEY-----|$)/g, '[已隐藏私钥]')
    // Whole quoted values, Basic/Bearer credentials, and escaped fragments are
    // conservative whole-field redactions, not first-word substitutions.
    .replace(/(\b(?:authorization|proxy[-_]?authorization|api[-_]?key|access[-_]?token|refresh[-_]?token|token|password|passwd|secret|client[-_]?secret)\b(?:\\?["'])?\s*[:=]\s*)(?:"(?:\\[\s\S]|[^"\\])*"|'[^']*'|[^\r\n,;}]+)/gi, '$1[已隐藏]')
    .replace(/\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+\/=-]+/gi, '[已隐藏凭据]')
    .replace(/\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{8,})/g, '[已隐藏凭据]');
}
export function redactObservedText(value) {return redact(value, 0);}
export function boundedPublicText(value, limit = 512) {
  let result = '', bytes = 0;
  for (const character of redactObservedText(value)) {const size = Buffer.byteLength(character); if (bytes + size > limit) break; result += character; bytes += size;}
  return result;
}

// Only explicit provider token counters, never context capacity or guessed billing.
export function tokenUsage(value, fields, complete = false) {
  if (!value || typeof value !== 'object') return null;
  const values = fields.map(key => value[key]);
  if (!values.every(number => Number.isSafeInteger(number) && number >= 0)) return null;
  return {inputTokens: values[0], outputTokens: values[1], totalTokens: values[2], source: 'provider-reported', complete};
}
