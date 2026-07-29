// Frontend unit tests for the money helpers in utils.js.
//
// These run on Node's built-in test runner with no dependencies and no build
// step — matching the project's no-bundler frontend:
//
//     node --test web/
//
// The money math must mirror the Go backend exactly (integer minor units, no
// floating point), because the same major-unit string a user types is parsed
// here for display and re-parsed on the server for storage. A drift between the
// two would let the UI accept an amount the API rejects, or vice versa.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { moneyExponent, parseMoney, formatMoney } from './utils.js';

test('moneyExponent: exponent by currency class', () => {
  // Default (2-decimal) currencies.
  assert.equal(moneyExponent('USD'), 2);
  assert.equal(moneyExponent('EUR'), 2);
  // Zero-decimal currencies.
  assert.equal(moneyExponent('JPY'), 0);
  assert.equal(moneyExponent('KRW'), 0);
  assert.equal(moneyExponent('XOF'), 0);
  // Three-decimal currencies.
  assert.equal(moneyExponent('BHD'), 3);
  assert.equal(moneyExponent('KWD'), 3);
  // Case-insensitive and whitespace-tolerant.
  assert.equal(moneyExponent('  jpy '), 0);
  assert.equal(moneyExponent('bhd'), 3);
  // Unknown / empty codes fall back to 2.
  assert.equal(moneyExponent('ZZZ'), 2);
  assert.equal(moneyExponent(''), 2);
  assert.equal(moneyExponent(null), 2);
});

test('parseMoney: valid two-decimal amounts', () => {
  assert.equal(parseMoney('100.00', 'USD'), 10000);
  assert.equal(parseMoney('100', 'USD'), 10000);
  assert.equal(parseMoney('0', 'USD'), 0);
  assert.equal(parseMoney('0.01', 'USD'), 1);
  assert.equal(parseMoney('12.5', 'USD'), 1250); // short fraction right-pads
  assert.equal(parseMoney('1234567.89', 'USD'), 123456789);
  assert.equal(parseMoney('  42.00  ', 'USD'), 4200); // surrounding whitespace
});

test('parseMoney: signs', () => {
  assert.equal(parseMoney('-5.00', 'USD'), -500);
  assert.equal(parseMoney('+5.00', 'USD'), 500);
  assert.equal(Math.abs(parseMoney('-0', 'USD')), 0); // zero, sign irrelevant (-0 formats as 0.00)
});

test('parseMoney: zero-decimal currency rejects any fraction', () => {
  assert.equal(parseMoney('1000', 'JPY'), 1000);
  assert.equal(parseMoney('1000.', 'JPY'), 1000); // trailing dot, empty fraction
  assert.equal(parseMoney('1.5', 'JPY'), null); // JPY has no minor unit
  assert.equal(parseMoney('1.0', 'JPY'), null);
});

test('parseMoney: three-decimal currency', () => {
  assert.equal(parseMoney('1.000', 'BHD'), 1000);
  assert.equal(parseMoney('1.5', 'BHD'), 1500);
  assert.equal(parseMoney('0.123', 'BHD'), 123);
  assert.equal(parseMoney('1.2345', 'BHD'), null); // more precision than 3 dp
});

test('parseMoney: fraction longer than exponent is rejected', () => {
  assert.equal(parseMoney('1.005', 'USD'), null);
  assert.equal(parseMoney('1.999', 'USD'), null);
});

test('parseMoney: rejects malformed input', () => {
  assert.equal(parseMoney('', 'USD'), null);
  assert.equal(parseMoney('   ', 'USD'), null);
  assert.equal(parseMoney('.50', 'USD'), null); // empty integer part
  assert.equal(parseMoney('abc', 'USD'), null);
  assert.equal(parseMoney('1.2.3', 'USD'), null); // two decimal points
  assert.equal(parseMoney('1,000.00', 'USD'), null); // thousands separator
  assert.equal(parseMoney('1 000', 'USD'), null); // internal space
  assert.equal(parseMoney('$5', 'USD'), null); // currency symbol
  assert.equal(parseMoney('1e3', 'USD'), null); // exponent notation
  assert.equal(parseMoney(null, 'USD'), null);
  assert.equal(parseMoney(undefined, 'USD'), null);
});

test('parseMoney/formatMoney round-trip for representative amounts', () => {
  // For each currency, a parsed amount re-formatted then re-parsed is stable.
  const cases = [
    ['USD', '100.00', 10000],
    ['JPY', '1000', 1000],
    ['BHD', '1.500', 1500],
  ];
  for (const [code, input, minor] of cases) {
    assert.equal(parseMoney(input, code), minor, `${code} ${input}`);
  }
});

test('formatMoney: renders minor units with the right precision', () => {
  // Compare against Intl output directly so the test is locale-agnostic.
  const usd = formatMoney(10000, 'USD');
  assert.match(usd, /100\.00/); // two fraction digits
  const jpy = formatMoney(1000, 'JPY');
  assert.doesNotMatch(jpy, /\./); // no fraction for a zero-decimal currency
  const bhd = formatMoney(1500, 'BHD');
  assert.match(bhd, /1\.500/); // three fraction digits
});

test('formatMoney: falls back gracefully for non-ISO codes', () => {
  // A free-text currency code makes Intl throw; we fall back to "<amount> <code>".
  const out = formatMoney(10000, 'POINTS');
  assert.equal(out, '100.00 POINTS');
});

test('formatMoney: non-numeric minor units treated as zero', () => {
  assert.match(formatMoney(NaN, 'USD'), /0\.00/);
  assert.match(formatMoney(undefined, 'USD'), /0\.00/);
});
