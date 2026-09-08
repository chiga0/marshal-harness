#!/usr/bin/env node
// Deterministic HTTP-test fixture, never a production provider or live evidence.
let prompt = '';
for await (const chunk of process.stdin) prompt += chunk;
const normalize = `export function normalize(rows) {
  if (!Array.isArray(rows)) throw new Error('invalid');
  for (let i = 0; i < rows.length; i++) if (!Object.hasOwn(rows, i)) throw new Error('invalid');
  return rows.map(row => {
    if (!row || typeof row.sku !== 'string' || !row.sku.trim() ||
        !Number.isSafeInteger(row.quantity) || row.quantity <= 0 ||
        !Number.isSafeInteger(row.priceCents) || row.priceCents < 0 ||
        !Number.isSafeInteger(row.quantity * row.priceCents)) throw new Error('invalid');
    return {sku: row.sku, quantity: row.quantity, priceCents: row.priceCents};
  });
}`;
const report = `export function report(rows) {
  if (!Array.isArray(rows)) throw new Error('invalid');
  const groups = new Map();
  for (const row of rows) {
    if (!row || typeof row.sku !== 'string' || !row.sku.trim() ||
        !Number.isSafeInteger(row.quantity) || row.quantity <= 0 ||
        !Number.isSafeInteger(row.priceCents) || row.priceCents < 0 ||
        !Number.isSafeInteger(row.quantity * row.priceCents)) throw new Error('invalid');
    const value = groups.get(row.sku) || {sku: row.sku, quantity: 0, totalCents: 0};
    value.quantity += row.quantity; value.totalCents += row.quantity * row.priceCents;
    if (!Number.isSafeInteger(value.quantity) || !Number.isSafeInteger(value.totalCents)) throw new Error('overflow');
    groups.set(row.sku, value);
  }
  return [...groups.values()].sort((a,b) => a.sku < b.sku ? -1 : a.sku > b.sku ? 1 : 0);
}`;
// The frozen prompt names exactly one output file; role instruction is last.
const name = prompt.includes('normalize.mjs') && !prompt.includes('report.mjs')
  ? 'normalize.mjs' : /(?:name|file|产出|实现).*normalize\.mjs/.test(prompt)
    ? 'normalize.mjs' : 'report.mjs';
console.log(JSON.stringify({type: 'session', id: 'fixture-session'}));
await new Promise(resolve => setTimeout(resolve, prompt.includes('fixture-slow') ? 15000 : 300));
const content = name === 'normalize.mjs' ? normalize : report;
const message = {
  role: 'assistant', stopReason: 'stop',
  content: [{type: 'text', text: JSON.stringify({name, content})}],
};
console.log(JSON.stringify({type: 'message_end', message}));
console.log(JSON.stringify({type: 'agent_end', messages: [message]}));
