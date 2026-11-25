import assert from 'node:assert/strict'
import test from 'node:test'
import { overviewCounts, executionBody } from '../src/data.ts'

test('overview counts active and terminal runs and published videos', () => {
  const runs = [
    'Pending',
    'Running',
    'Cancelling',
    'Cancelled',
    'RequiresReview',
    'Succeeded',
    'Failed',
  ].map((phase) => ({ phase }))
  const artifacts = ['video/mp4', 'image/png', 'video/webm'].map(
    (contentType) => ({ contentType }),
  )
  assert.deepEqual(overviewCounts(1, runs, artifacts), [1, 3, 1, 1, 2])
})

test('execution form types values and keeps charge approval separate from inputs', () => {
  const definitions = [
    { name: 'allow-paid', type: 'boolean', required: true },
    { name: 'prompt', type: 'string', required: true },
    { name: 'seed', type: 'integer' },
    { name: 'temperature', type: 'number' },
    { name: 'optional', type: 'string' },
  ]
  const fields = new FormData()
  fields.set('allow-paid', 'on')
  fields.set('input:allow-paid', 'false')
  fields.set('input:prompt', 'Story')
  fields.set('input:seed', '42')
  fields.set('input:temperature', '0.5')
  assert.equal(
    JSON.parse(executionBody(definitions, fields, true)).allowPaid,
    true,
  )
  assert.deepEqual(
    JSON.parse(executionBody(definitions, fields, true)).inputs,
    {
      'allow-paid': { booleanValue: false },
      prompt: { stringValue: 'Story' },
      seed: { integerValue: 42 },
      temperature: { numberValue: 0.5 },
    },
  )
})

test('execution form preserves signed int64 values and rejects out-of-range inputs', () => {
  const fields = new FormData()
  const seed = { name: 'seed', type: 'integer', required: true }
  assert.throws(
    () => executionBody([seed], fields, false),
    /Enter a value for seed/,
  )
  for (const value of ['9223372036854775807', '-9223372036854775808']) {
    fields.set('input:seed', value)
    assert.equal(
      executionBody([seed], fields, false),
      `{"inputs":{"seed":{"integerValue":${value}}},"allowPaid":false}`,
    )
  }
  for (const invalid of [
    '9223372036854775808',
    '-9223372036854775809',
    '1.5',
  ]) {
    fields.set('input:seed', invalid)
    assert.throws(
      () => executionBody([seed], fields, false),
      /Enter a valid integer for seed/,
    )
  }
})
