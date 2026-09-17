import test from 'node:test';
import assert from 'node:assert/strict';
import {boundedPublicText, redactObservedText, tokenUsage} from './normalization.ts';

test('shared disclosure redacts complete credential forms before bounded Unicode preview', () => {
  const raw = '公开🙂\nAuthorization: Bearer synthetic-secret\n"api_key":"synthetic-key"\npassword=synthetic-password\n' +
    '-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----';
  const result = redactObservedText(raw);
  assert.doesNotMatch(result, /synthetic-secret|synthetic-key|synthetic-password|private-material/);
  assert.match(result, /公开🙂/);
  const preview = boundedPublicText(raw + '🙂'.repeat(1000));
  assert.ok(Buffer.byteLength(preview) <= 512); assert.equal(preview.isWellFormed(), true);
  assert.equal(tokenUsage({used:30,size:100}, ['input','output','totalTokens']), null);
  assert.equal(tokenUsage({input:-1,output:1,totalTokens:0}, ['input','output','totalTokens']), null);
});

test('nested serialized materials, quoted spaces, Basic and token fields never retain supported secret values', () => {
  const values = [
    JSON.stringify({materials:[{content:'{"password":"fixture-secret-value"}'}]}),
    '业务上下文：' + JSON.stringify({materials:[{content:'{"password":"fixture secret value"}'}]}),
    'password="fixture secret value"\n正常说明',
    'Authorization: Basic Zml4dHVyZTpzZWNyZXQ=\n正常说明',
    '{"token":"fixture-secret-value"}',
    'escaped: {\\"password\\":\\"fixture-secret-value\\"}',
  ];
  for (const value of values) {
    const redacted = redactObservedText(value);
    assert.doesNotMatch(redacted, /fixture.secret.value|Zml4dHVyZTpzZWNyZXQ/);
    assert.match(redacted,/已隐藏/);
  }
});
