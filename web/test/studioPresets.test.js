import assert from 'node:assert/strict'
import test from 'node:test'
import {
  buildStudioRunBody,
  buildStudioWorkflowSpec,
  detectProviderReadiness,
  studioWorkflowId,
} from '../src/studioPresets.ts'

const baseDraft = {
  prompt: 'Neon cyberpunk alley in Tokyo at night',
  style: 'Cinematic',
  referenceNotes: 'slow dolly forward',
  aspectRatio: '9:16',
  clips: 1,
  mode: 'mock',
  referenceMode: 'none',
  referenceImageKey: '',
  geminiProviderId: 'gemini',
  falProviderId: 'fal',
  llmModel: 'gemini-2.5-flash',
  imageModel: 'fal-ai/flux/schnell',
  videoModel: 'fal-ai/kling-video/v1.6/standard/image-to-video',
}

test('detectProviderReadiness identifies enabled Gemini and fal.ai providers', () => {
  const readiness = detectProviderReadiness([
    { id: 'mock', type: 'mock', enabled: true, capabilities: ['text', 'image', 'video'], credentialsReferenced: false },
    { id: 'gemini-main', type: 'gemini', enabled: true, capabilities: ['text'], credentialsReferenced: true },
    { id: 'fal-disabled', type: 'fal', enabled: false, capabilities: ['video'], credentialsReferenced: true },
  ])
  assert.equal(readiness.hasGemini, true)
  assert.equal(readiness.hasFal, false)
  assert.equal(readiness.geminiProviders[0].id, 'gemini-main')
})

test('studioWorkflowId produces deterministic Kubernetes-safe names', () => {
  assert.equal(studioWorkflowId(baseDraft), 'studio-mock-t2v-1c-9x16')
  assert.equal(
    studioWorkflowId({ ...baseDraft, mode: 'full-ai', referenceMode: 'auto-image', clips: 2, aspectRatio: '16:9' }),
    'studio-full-ai-refimg-2c-16x9',
  )
})

test('buildStudioWorkflowSpec wires auto-generated reference frame and FFmpeg aspect ratio', () => {
  const spec = buildStudioWorkflowSpec({
    ...baseDraft,
    mode: 'full-ai',
    referenceMode: 'auto-image',
    clips: 2,
    aspectRatio: '16:9',
  })
  assert.deepEqual(
    spec.steps.map((s) => s.name),
    ['script', 'scene-1-image', 'scene-1-video', 'scene-2-image', 'scene-2-video', 'render'],
  )
  const renderStep = spec.steps.find((s) => s.name === 'render')
  assert.equal(renderStep?.ffmpeg?.operation, 'render_mp4')
  assert.equal(renderStep?.ffmpeg?.aspectRatio, '16:9')
  assert.deepEqual(renderStep?.dependsOn, ['scene-1-video', 'scene-2-video'])
})

test('buildStudioRunBody attaches uploaded reference image and requires paid approval for external providers', () => {
  const payload = JSON.parse(
    buildStudioRunBody({
      ...baseDraft,
      mode: 'fal',
      referenceMode: 'upload',
      referenceImageKey: 'references/0123456789abcdef0123456789abcdef.png',
    }),
  )
  assert.equal(payload.allowPaid, true)
  assert.equal(payload.inputs.topic.stringValue, baseDraft.prompt)
  assert.equal(payload.inputs.reference_image.stringValue, 'references/0123456789abcdef0123456789abcdef.png')
  assert.equal(payload.inputs.reference_notes.stringValue, 'slow dolly forward')

  assert.throws(
    () => buildStudioRunBody({ ...baseDraft, prompt: '   ' }),
    /Enter a prompt/,
  )
  assert.throws(
    () => buildStudioRunBody({ ...baseDraft, mode: 'fal', referenceMode: 'upload', referenceImageKey: '' }),
    /Upload a reference image/,
  )
})
