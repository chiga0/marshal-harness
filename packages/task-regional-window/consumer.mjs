import {parseJson} from '../task-api/http-boundary.mjs';
import {digest} from '../task-store/store.mjs';
import {fields, check, range, rowsFrom, equal, PROFILE} from './policy.mjs';

// Separate download-consumer computation, not the checker's pass field or the
// parent's expected() function. The operator supplies ORIGINAL uploaded bytes
// and dates read back from the accepted preview, not arbitrary driver globals.
export function consumeDelivery(content, originalBytes, acceptedValues) {
  check(content instanceof Uint8Array && content.byteLength <= 16384, 'window_delivery_limit');
  const data = parseJson(Buffer.from(content)), values = range(acceptedValues), rows = rowsFrom(originalBytes);
  check(fields(data, ['profile', 'window', 'sourceDigest', 'files']) && data.profile === PROFILE && equal(data.window, values) &&
    data.sourceDigest === digest(originalBytes) && Array.isArray(data.files) && data.files.length === 2, 'window_delivery_binding');
  const actual = data.files.map((file, index) => {
    const region = ['east', 'west'][index];
    check(fields(file, ['path', 'content']) && file.path === region + '.json' && typeof file.content === 'string' && Buffer.byteLength(file.content) <= 4096);
    const result = parseJson(Buffer.from(file.content));
    const selected = rows.filter(row => row.region === region && row.status === 'paid' && row.date.localeCompare(values.startDate) >= 0 && row.date.localeCompare(values.endDate) <= 0);
    const sum = selected.reduce((total, row) => total + BigInt(row.cents), 0n);
    check(equal(result, {region, ...values, count: selected.length, netCents: Number(sum)}), 'window_consumer_failed'); return result;
  });
  return {window: values, regions: 2, count: actual.reduce((sum, item) => sum + item.count, 0),
    netCents: actual.reduce((sum, item) => sum + item.netCents, 0), digest: digest(content)};
}
