import type { Artifact, Input, Run } from './types'

// Derive overview counts from the stored definitions, run states and published outputs.
export function overviewCounts(
  workflowCount: number,
  runs: Run[],
  artifacts: Artifact[],
) {
  return [
    workflowCount,
    runs.filter((run) =>
      ['Pending', 'Running', 'Cancelling'].includes(run.phase),
    ).length,
    runs.filter((run) => run.phase === 'Succeeded').length,
    runs.filter((run) => run.phase === 'Failed').length,
    artifacts.filter((artifact) => artifact.contentType.startsWith('video/'))
      .length,
  ]
}

// Serialize int64 inputs as decimal JSON numbers without rounding through Number.
export function executionBody(
  definitions: Input[],
  fields: FormData,
  allowPaid: boolean,
) {
  const entries = []
  for (const { name, type, required } of definitions) {
    const value = fields.get('input:' + name)
    if (value === '' || typeof value !== 'string') {
      if (required) throw new Error(`Enter a value for ${name}.`)
      continue
    }

    const key = JSON.stringify(name)
    if (type === 'integer') {
      if (!/^[+-]?\d+$/.test(value))
        throw new Error(`Enter a valid integer for ${name}.`)
      const integer = BigInt(value)
      if (integer < -(2n ** 63n) || integer >= 2n ** 63n) {
        throw new Error(`Enter a valid integer for ${name}.`)
      }
      entries.push(`${key}:{"integerValue":${integer}}`)
      continue
    }

    const input =
      type === 'boolean'
        ? value === 'true'
        : type === 'number'
          ? Number(value)
          : value
    if (typeof input === 'number' && !Number.isFinite(input))
      throw new Error(`Enter a valid number for ${name}.`)
    entries.push(`${key}:${JSON.stringify({ [type + 'Value']: input })}`)
  }
  return `{"inputs":{${entries.join(',')}},"allowPaid":${allowPaid}}`
}
