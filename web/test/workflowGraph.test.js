import assert from 'node:assert/strict'
import test from 'node:test'
import {
  buildPatchSpec,
  changeStepType,
  computeStepPositions,
  connectDependency,
  createDefaultStep,
  defaultWorkflowSpec,
  disconnectDependency,
  normalizeWorkflowSpec,
  parseWorkflowSpecJSON,
  removeStep,
  renameStep,
  validateWorkflowDraft,
} from '../src/workflowGraph.ts'

test('default workflow spec and step type transitions preserve CRD rules', () => {
  const starter = defaultWorkflowSpec()
  assert.deepEqual(validateWorkflowDraft('horror-shorts', starter), [])

  const added = createDefaultStep('video', [
    { name: 'video-1', type: 'video', providerRef: 'mock' },
  ])
  assert.equal(added.name, 'video-2')
  assert.equal(added.providerRef, 'mock')

  const toFFmpeg = changeStepType(
    {
      name: 'clip',
      type: 'video',
      providerRef: 'fal-main',
      model: 'fal-ai/wan-t2v',
      dependsOn: ['script'],
      timeoutSeconds: 120,
    },
    'ffmpeg',
  )
  assert.equal(toFFmpeg.type, 'ffmpeg')
  assert.equal(toFFmpeg.providerRef, undefined)
  assert.equal(toFFmpeg.model, undefined)
  assert.equal(toFFmpeg.ffmpeg?.operation, 'render_mp4')
  assert.deepEqual(toFFmpeg.dependsOn, ['script'])
  assert.equal(toFFmpeg.timeoutSeconds, 120)

  const backToImage = changeStepType(toFFmpeg, 'image')
  assert.equal(backToImage.ffmpeg, undefined)
  assert.equal(backToImage.providerRef, 'mock')
})

test('topological layout orders columns left-to-right and tolerates cyclic imports', () => {
  const dagSteps = [
    { name: 'script', type: 'llm', providerRef: 'mock' },
    {
      name: 'img-a',
      type: 'image',
      providerRef: 'mock',
      dependsOn: ['script'],
    },
    {
      name: 'img-b',
      type: 'image',
      providerRef: 'mock',
      dependsOn: ['script'],
    },
    {
      name: 'merge',
      type: 'ffmpeg',
      dependsOn: ['img-a', 'img-b'],
      ffmpeg: { operation: 'render_mp4' },
    },
  ]
  const pos = computeStepPositions(dagSteps)
  assert.ok(pos['script'].x < pos['img-a'].x)
  assert.equal(pos['img-a'].x, pos['img-b'].x)
  assert.notEqual(pos['img-a'].y, pos['img-b'].y)
  assert.ok(pos['img-a'].x < pos['merge'].x)

  const cyclic = [
    { name: 'a', type: 'input', dependsOn: ['b'] },
    { name: 'b', type: 'output', dependsOn: ['a'] },
  ]
  const cyclicPos = computeStepPositions(cyclic)
  assert.ok(Number.isFinite(cyclicPos['a'].x))
  assert.ok(Number.isFinite(cyclicPos['b'].x))
})

test('dependency wiring blocks self-loops and cycles and supports disconnection', () => {
  const initial = [
    { name: 'a', type: 'llm', providerRef: 'mock' },
    { name: 'b', type: 'image', providerRef: 'mock' },
    { name: 'c', type: 'video', providerRef: 'mock' },
  ]

  const selfRes = connectDependency(initial, 'a', 'a')
  assert.match(selfRes.error ?? '', /cannot depend on itself/)

  const ab = connectDependency(initial, 'a', 'b')
  assert.equal(ab.error, undefined)
  const bc = connectDependency(ab.steps, 'b', 'c')
  assert.equal(bc.error, undefined)

  const cycleAttempt = connectDependency(bc.steps, 'c', 'a')
  assert.match(cycleAttempt.error ?? '', /acyclic/)

  const disconnected = disconnectDependency(bc.steps, 'a', 'b')
  assert.equal(disconnected.find((s) => s.name === 'b')?.dependsOn, undefined)
})

test('renaming and removing steps updates downstream dependencies, FFmpeg inputs, and outputs', () => {
  const spec = {
    steps: [
      { name: 'raw-clip', type: 'video', providerRef: 'mock' },
      {
        name: 'encode',
        type: 'ffmpeg',
        dependsOn: ['raw-clip'],
        ffmpeg: {
          operation: 'render_mp4',
          inputs: ['raw-clip'],
          audioInput: 'raw-clip',
        },
      },
    ],
    outputs: [{ name: 'final', from: 'raw-clip' }],
  }
  const positions = {
    'raw-clip': { x: 40, y: 60 },
    encode: { x: 320, y: 60 },
  }

  const renamed = renameStep(spec, 'raw-clip', 'intro-clip', positions)
  assert.equal(renamed.error, undefined)
  assert.deepEqual(renamed.spec.steps[1].dependsOn, ['intro-clip'])
  assert.deepEqual(renamed.spec.steps[1].ffmpeg?.inputs, ['intro-clip'])
  assert.equal(renamed.spec.steps[1].ffmpeg?.audioInput, 'intro-clip')
  assert.equal(renamed.spec.outputs?.[0].from, 'intro-clip')
  assert.deepEqual(renamed.positions['intro-clip'], { x: 40, y: 60 })
  assert.equal(renamed.positions['raw-clip'], undefined)

  const removed = removeStep(renamed.spec, 'intro-clip', renamed.positions)
  assert.equal(removed.spec.steps.length, 1)
  assert.equal(removed.spec.steps[0].dependsOn, undefined)
  assert.equal(removed.spec.outputs, undefined)
})

test('validation catches invalid IDs, cycles, FFmpeg constraints, and patch nulls removed keys', () => {
  const badSpec = {
    inputs: [
      { name: 'topic', type: 'string' },
      { name: 'topic', type: 'string' },
    ],
    steps: [
      {
        name: 'render',
        type: 'ffmpeg',
        providerRef: 'mock',
        ffmpeg: { operation: 'resize', width: 8, height: 8 },
      },
      { name: 'llm-step', type: 'llm', model: 'gemini-4-hydrogen' },
    ],
    outputs: [{ name: 'out', from: 'missing-step' }],
  }
  const issues = validateWorkflowDraft('Invalid_ID', badSpec)
  assert.ok(issues.some((msg) => msg.includes('Workflow ID')))
  assert.ok(issues.some((msg) => msg.includes('Duplicate workflow input')))
  assert.ok(issues.some((msg) => msg.includes('must not set providerRef')))
  assert.ok(issues.some((msg) => msg.includes('width and height')))
  assert.ok(issues.some((msg) => msg.includes('without a providerRef')))
  assert.ok(issues.some((msg) => msg.includes('unknown step')))

  const parsed = parseWorkflowSpecJSON(
    JSON.stringify(normalizeWorkflowSpec(defaultWorkflowSpec())),
  )
  assert.equal(parsed.steps.length, 3)

  assert.throws(
    () => parseWorkflowSpecJSON('{"steps":[],"extra":true}'),
    /Unknown workflow spec field/,
  )

  const patch = buildPatchSpec(defaultWorkflowSpec(), {
    steps: [{ name: 'only', type: 'video', providerRef: 'mock' }],
  })
  assert.equal(patch.description, null)
  assert.equal(patch.inputs, null)
  assert.equal(patch.outputs, null)
})
