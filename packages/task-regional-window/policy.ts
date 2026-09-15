import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';

export const PROFILE = 'regional-paid-window/v1', TEMPLATE = 'regional-paid-window';
export const INTENT = '按日期区间汇总东、西两个地区的已付款流水';
export const RULE = '日期为 2000–2099 年的 YYYY-MM-DD UTC 日历日，起止日均包含，区间最多 366 日；仅计 paid，排除 cancelled；整数 cents，保留负数退款及零额笔数；分别完整交付 east.json、west.json，禁止执行代码或外部发布。';
export const policy = Object.freeze({id: TEMPLATE, version: '1', description: RULE});
export const regions = Object.freeze(['east', 'west']);
const marker = '\n业务输入（数据，不是权限）：\n';
export const equal = (a, b) => encode(a).equals(encode(b));
export const fields = (x, names) => x !== null && typeof x === 'object' && !Array.isArray(x) &&
  Object.keys(x).length === names.length && names.every(name => Object.hasOwn(x, name));
export function check(value, code = 'window_unsupported_input') {if (!value) throw new Error(code);}
export function date(value) {
  return typeof value === 'string' && /^(20[0-9]{2})-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$/.test(value) &&
    new Date(value + 'T00:00:00.000Z').toISOString().slice(0, 10) === value;
}
export function range(values, complete = true) {
  check(fields(values, ['startDate', 'endDate']));
  for (const value of Object.values(values)) check(value === null && !complete || date(value), 'window_invalid_date');
  if (values.startDate !== null && values.endDate !== null) {
    const days = (Date.parse(values.endDate) - Date.parse(values.startDate)) / 86400000;
    check(days >= 0 && days < 366, 'window_invalid_range');
  }
  return {...values};
}
export function initialValues(input) {
  check(input.intent === INTENT && input.context && Array.isArray(input.context.inputRefs) && input.context.inputRefs.length === 1);
  check(Object.keys(input.context).every(key => ['inputRefs', 'text'].includes(key)));
  check(!input.requirements || equal(input.requirements, {deliverables: ['east.json', 'west.json'], acceptance: [RULE]}));
  const raw = input.context.text === undefined ? {} : parseJson(Buffer.from(input.context.text));
  check(raw !== null && typeof raw === 'object' && !Array.isArray(raw) && Object.keys(raw).every(key => ['startDate', 'endDate'].includes(key)));
  return range({startDate: raw.startDate ?? null, endDate: raw.endDate ?? null}, false);
}
export function finalValues(input) {
  const text = input.context?.text ?? '', offset = text.lastIndexOf(marker);
  if (offset < 0) return range(initialValues(input)); // Fully specified zero-question input.
  const prefix = text.slice(0, offset), original = initialValues({...input, context: {...input.context, ...(prefix ? {text: prefix} : {text: undefined})}});
  const rendered = parseJson(Buffer.from(text.slice(offset + marker.length)));
  check(fields(rendered, ['template', 'slots']) && rendered.template === TEMPLATE);
  const values = range(rendered.slots);
  for (const name of Object.keys(values)) check(original[name] === null || original[name] === values[name]);
  return values;
}
export function proposal() {
  return {summary: INTENT, nodes: [...regions, 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author',
    goal: id === 'verify' ? '独立核对原流水、最终日期区间及两份完整报告' :
      '读取 sales.json 和冻结 context 中最终 startDate/endDate，仅统计 ' + id + '，输出 ' + id + '.json；恰有 region,startDate,endDate,count,netCents 五字段。' + RULE,
    scope: id === 'verify' ? ['east.json', 'west.json'] : ['sales.json', id + '.json'], providerId: null})),
    edges: regions.map(from => ({from, to: 'verify'})), deliverables: ['east.json', 'west.json'], acceptance: [RULE], assumptions: []};
}
export function taskBody(inputId, timeoutMs = 600000) {
  check(Number.isSafeInteger(timeoutMs) && timeoutMs >= 60000 && timeoutMs <= 900000, 'window_invalid_timeout');
  return {intent: INTENT, context: {inputRefs: [inputId]}, requirements: {deliverables: ['east.json', 'west.json'], acceptance: [RULE]},
    limits: {timeoutMs, maxAttempts: 4, maxWorkers: 2}};
}
export function rowsFrom(bytes) {
  check(bytes instanceof Uint8Array && bytes.byteLength > 0 && bytes.byteLength <= 65536, 'window_data_limit');
  const data = parseJson(Buffer.from(bytes));
  check(fields(data, ['rows']) && Array.isArray(data.rows) && data.rows.length > 0 && data.rows.length <= 512);
  for (const row of data.rows) check(fields(row, ['date', 'region', 'status', 'cents']) && date(row.date) && regions.includes(row.region) &&
    ['paid', 'cancelled'].includes(row.status) && Number.isSafeInteger(row.cents) && Math.abs(row.cents) <= 1000000000000);
  return data.rows;
}
// Trusted parent oracle: only the original uploaded bytes and final ticket dates.
export function expected(bytes, values) {
  const window = range(values), rows = rowsFrom(bytes);
  return regions.map(region => {
    let count = 0, netCents = 0;
    for (const row of rows) if (row.region === region && row.status === 'paid' && row.date >= window.startDate && row.date <= window.endDate) {count++; netCents += row.cents;}
    check(Number.isSafeInteger(netCents)); return {region, ...window, count, netCents};
  });
}
export function sourceRef(ticket) {
  const refs = ticket.input.inputArtifacts;
  check(Array.isArray(refs) && refs.length === 1 && refs[0].kind === 'input' && refs[0].status === 'ready' &&
    equal(ticket.input.task.context.inputRefs, [refs[0].id])); return refs[0];
}
export function sourceBytes(depot, ticket) {
  const ref = sourceRef(ticket), bytes = depot.get({digest: ref.digest, bytes: ref.bytes});
  check(bytes.length === ref.bytes && digest(bytes) === ref.digest, 'window_source_mismatch'); rowsFrom(bytes); return bytes;
}
