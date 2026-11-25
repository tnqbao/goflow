import type {
  AspectRatio,
  FFmpegOperation,
  FFmpegSpec,
  Input,
  InputType,
  ResourceList,
  StepResources,
  StepType,
  WorkflowOutput,
  WorkflowSpec,
  WorkflowStep,
} from './types.ts'

export type Point = { x: number; y: number }

export const STEP_TYPES: readonly StepType[] = [
  'input',
  'llm',
  'image',
  'video',
  'ffmpeg',
  'output',
]

export const INPUT_TYPES: readonly InputType[] = [
  'string',
  'integer',
  'number',
  'boolean',
]

export const FFMPEG_OPERATIONS: readonly FFmpegOperation[] = [
  'render_mp4',
  'merge',
  'trim',
  'resize',
  'aspect_ratio',
  'add_audio',
  'normalize',
]

export const ASPECT_RATIOS: readonly AspectRatio[] = [
  '16:9',
  '9:16',
  '1:1',
  '4:3',
  '4:5',
]

const DNS1123_SUBDOMAIN =
  /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

const ALLOWED_SPEC_KEYS = new Set([
  'description',
  'inputs',
  'steps',
  'outputs',
])

const COLUMN_GAP = 290
const ROW_GAP = 148
const ORIGIN_X = 48
const ORIGIN_Y = 56

export function defaultWorkflowSpec(): WorkflowSpec {
  return {
    description: 'Generate a video clip and render a final MP4 output.',
    inputs: [{ name: 'prompt', type: 'string', required: true }],
    steps: [
      { name: 'script', type: 'llm', providerRef: 'mock' },
      {
        name: 'scene',
        type: 'video',
        providerRef: 'mock',
        dependsOn: ['script'],
      },
      {
        name: 'render',
        type: 'ffmpeg',
        dependsOn: ['scene'],
        ffmpeg: { operation: 'render_mp4', aspectRatio: '16:9' },
      },
    ],
    outputs: [{ name: 'video', from: 'render' }],
  }
}

export function isProviderStepType(type: StepType): boolean {
  return type === 'llm' || type === 'image' || type === 'video'
}

export function createDefaultStep(
  type: StepType,
  existingSteps: WorkflowStep[],
): WorkflowStep {
  const taken = new Set(existingSteps.map((step) => step.name))
  let index = 1
  while (taken.has(`${type}-${index}`)) index++
  const name = `${type}-${index}`

  if (type === 'ffmpeg') {
    return {
      name,
      type,
      ffmpeg: { operation: 'render_mp4', aspectRatio: '16:9' },
    }
  }
  if (isProviderStepType(type)) {
    return { name, type, providerRef: 'mock' }
  }
  return { name, type }
}

export function changeStepType(
  step: WorkflowStep,
  nextType: StepType,
): WorkflowStep {
  if (step.type === nextType) return step
  const base: WorkflowStep = {
    name: step.name,
    type: nextType,
    ...(step.dependsOn?.length ? { dependsOn: [...step.dependsOn] } : {}),
    ...(step.retry ? { retry: { ...step.retry } } : {}),
    ...(step.timeoutSeconds ? { timeoutSeconds: step.timeoutSeconds } : {}),
    ...(step.resources ? { resources: step.resources } : {}),
  }

  if (nextType === 'ffmpeg') {
    return {
      ...base,
      ffmpeg: step.ffmpeg ?? { operation: 'render_mp4', aspectRatio: '16:9' },
    }
  }
  if (isProviderStepType(nextType)) {
    return {
      ...base,
      providerRef: step.providerRef || 'mock',
      ...(step.model ? { model: step.model } : {}),
    }
  }
  return base
}

export function computeStepPositions(
  steps: WorkflowStep[],
  preserved?: Record<string, Point>,
): Record<string, Point> {
  const byName = new Map(steps.map((step) => [step.name, step]))
  const memo = new Map<string, number>()
  const visiting = new Set<string>()

  const depthOf = (name: string): number => {
    const cached = memo.get(name)
    if (cached !== undefined) return cached
    if (visiting.has(name)) return 0
    visiting.add(name)
    const step = byName.get(name)
    let rank = 0
    for (const dep of step?.dependsOn ?? []) {
      if (byName.has(dep)) rank = Math.max(rank, depthOf(dep) + 1)
    }
    visiting.delete(name)
    memo.set(name, rank)
    return rank
  }

  const columns = new Map<number, string[]>()
  for (const step of steps) {
    const col = depthOf(step.name)
    const list = columns.get(col) ?? []
    list.push(step.name)
    columns.set(col, list)
  }

  const maxRows = Math.max(
    1,
    ...Array.from(columns.values(), (names) => names.length),
  )
  const positions: Record<string, Point> = {}
  for (const [col, names] of columns.entries()) {
    const offsetY = ((maxRows - names.length) * ROW_GAP) / 2
    names.forEach((name, row) => {
      positions[name] = preserved?.[name] ?? {
        x: ORIGIN_X + col * COLUMN_GAP,
        y: Math.round(ORIGIN_Y + offsetY + row * ROW_GAP),
      }
    })
  }
  return positions
}

export function connectDependency(
  steps: WorkflowStep[],
  sourceName: string,
  targetName: string,
): { steps: WorkflowStep[]; error?: string } {
  if (!sourceName || !targetName) {
    return { steps, error: 'Select both a source step and a target step.' }
  }
  if (sourceName === targetName) {
    return { steps, error: `Step "${sourceName}" cannot depend on itself.` }
  }

  const byName = new Map(steps.map((step) => [step.name, step]))
  const target = byName.get(targetName)
  if (!byName.has(sourceName) || !target) {
    return { steps, error: 'Both connected steps must exist in the workflow.' }
  }
  if (target.dependsOn?.includes(sourceName)) {
    return { steps }
  }

  const candidate = steps.map((step) =>
    step.name === targetName
      ? { ...step, dependsOn: [...(step.dependsOn ?? []), sourceName] }
      : step,
  )
  if (hasCycle(candidate)) {
    return {
      steps,
      error: `Cannot connect "${sourceName}" to "${targetName}" because workflow dependencies must be acyclic.`,
    }
  }
  return { steps: candidate }
}

export function disconnectDependency(
  steps: WorkflowStep[],
  sourceName: string,
  targetName: string,
): WorkflowStep[] {
  return steps.map((step) => {
    if (step.name !== targetName || !step.dependsOn?.length) return step
    const remaining = step.dependsOn.filter((dep) => dep !== sourceName)
    const copy = { ...step }
    if (remaining.length) copy.dependsOn = remaining
    else delete copy.dependsOn
    return copy
  })
}

export function renameStep(
  spec: WorkflowSpec,
  oldName: string,
  rawNewName: string,
  positions: Record<string, Point>,
): { spec: WorkflowSpec; positions: Record<string, Point>; error?: string } {
  const newName = rawNewName.trim()
  if (!newName || newName.length > 63) {
    return {
      spec,
      positions,
      error: 'Step name must contain 1 to 63 characters.',
    }
  }
  if (newName === oldName) return { spec, positions }
  if (spec.steps.some((step) => step.name === newName)) {
    return {
      spec,
      positions,
      error: `A step named "${newName}" already exists.`,
    }
  }

  const nextSteps = spec.steps.map((step) => {
    const updated: WorkflowStep = {
      ...step,
      name: step.name === oldName ? newName : step.name,
    }
    if (step.dependsOn?.includes(oldName)) {
      updated.dependsOn = step.dependsOn.map((dep) =>
        dep === oldName ? newName : dep,
      )
    }
    if (step.ffmpeg) {
      updated.ffmpeg = {
        ...step.ffmpeg,
        ...(step.ffmpeg.inputs
          ? {
              inputs: step.ffmpeg.inputs.map((entry) =>
                entry === oldName ? newName : entry,
              ),
            }
          : {}),
        ...(step.ffmpeg.audioInput === oldName
          ? { audioInput: newName }
          : {}),
      }
    }
    return updated
  })

  const nextOutputs = spec.outputs?.map((output) =>
    output.from === oldName ? { ...output, from: newName } : output,
  )

  const nextPositions = { ...positions }
  if (oldName in nextPositions) {
    nextPositions[newName] = nextPositions[oldName]
    delete nextPositions[oldName]
  }

  return {
    spec: {
      ...spec,
      steps: nextSteps,
      ...(nextOutputs ? { outputs: nextOutputs } : {}),
    },
    positions: nextPositions,
  }
}

export function removeStep(
  spec: WorkflowSpec,
  targetName: string,
  positions: Record<string, Point>,
): { spec: WorkflowSpec; positions: Record<string, Point> } {
  const nextSteps = spec.steps
    .filter((step) => step.name !== targetName)
    .map((step) => {
      if (!step.dependsOn?.includes(targetName)) return step
      const deps = step.dependsOn.filter((dep) => dep !== targetName)
      const copy = { ...step }
      if (deps.length) copy.dependsOn = deps
      else delete copy.dependsOn
      return copy
    })

  const nextOutputs = spec.outputs?.filter(
    (output) => output.from !== targetName,
  )
  const nextPositions = { ...positions }
  delete nextPositions[targetName]

  return {
    spec: {
      ...spec,
      steps: nextSteps,
      outputs: nextOutputs?.length ? nextOutputs : undefined,
    },
    positions: nextPositions,
  }
}

export function normalizeWorkflowSpec(spec: WorkflowSpec): WorkflowSpec {
  const description = spec.description?.trim()
  const inputs = spec.inputs
    ?.map((input): Input => ({
      name: input.name.trim(),
      type: input.type,
      ...(input.required ? { required: true } : {}),
      ...(input.description?.trim()
        ? { description: input.description.trim() }
        : {}),
    }))
    .filter((input) => input.name !== '')

  const steps = spec.steps.map(normalizeStep)

  const outputs = spec.outputs
    ?.map(
      (output): WorkflowOutput => ({
        name: output.name.trim(),
        from: output.from.trim(),
      }),
    )
    .filter((output) => output.name !== '' || output.from !== '')

  return {
    ...(description ? { description } : {}),
    ...(inputs?.length ? { inputs } : {}),
    steps,
    ...(outputs?.length ? { outputs } : {}),
  }
}

function normalizeStep(step: WorkflowStep): WorkflowStep {
  const dependsOn = Array.from(
    new Set((step.dependsOn ?? []).map((dep) => dep.trim()).filter(Boolean)),
  )
  const providerRef = step.providerRef?.trim()
  const model = step.model?.trim()
  const resources = normalizeResources(step.resources)
  const maxAttempts = step.retry?.maxAttempts

  const normalized: WorkflowStep = {
    name: step.name.trim(),
    type: step.type,
    ...(dependsOn.length ? { dependsOn } : {}),
  }

  if (step.type === 'ffmpeg') {
    if (step.ffmpeg) normalized.ffmpeg = normalizeFFmpeg(step.ffmpeg)
  } else {
    if (providerRef) normalized.providerRef = providerRef
    if (model) normalized.model = model
  }

  if (maxAttempts !== undefined && Number.isFinite(maxAttempts) && maxAttempts > 0) {
    normalized.retry = { maxAttempts: Math.trunc(maxAttempts) }
  }
  if (
    step.timeoutSeconds !== undefined &&
    Number.isFinite(step.timeoutSeconds) &&
    step.timeoutSeconds > 0
  ) {
    normalized.timeoutSeconds = Math.trunc(step.timeoutSeconds)
  }
  if (resources) normalized.resources = resources

  return normalized
}

function normalizeFFmpeg(ffmpeg: FFmpegSpec): FFmpegSpec {
  const inputs = (ffmpeg.inputs ?? []).map((s) => s.trim()).filter(Boolean)
  const audioInput = ffmpeg.audioInput?.trim()
  return {
    operation: ffmpeg.operation,
    ...(inputs.length ? { inputs } : {}),
    ...(audioInput ? { audioInput } : {}),
    ...(ffmpeg.width ? { width: Math.trunc(ffmpeg.width) } : {}),
    ...(ffmpeg.height ? { height: Math.trunc(ffmpeg.height) } : {}),
    ...(ffmpeg.aspectRatio ? { aspectRatio: ffmpeg.aspectRatio } : {}),
    ...(ffmpeg.startSeconds !== undefined && Number.isFinite(ffmpeg.startSeconds)
      ? { startSeconds: ffmpeg.startSeconds }
      : {}),
    ...(ffmpeg.durationSeconds !== undefined &&
    Number.isFinite(ffmpeg.durationSeconds)
      ? { durationSeconds: ffmpeg.durationSeconds }
      : {}),
    ...(ffmpeg.fps ? { fps: Math.trunc(ffmpeg.fps) } : {}),
  }
}

function normalizeResources(
  resources?: StepResources,
): StepResources | undefined {
  if (!resources) return undefined
  const requests = cleanResourcePair(resources.requests)
  const limits = cleanResourcePair(resources.limits)
  if (!requests && !limits) return undefined
  return {
    ...(requests ? { requests } : {}),
    ...(limits ? { limits } : {}),
  }
}

function cleanResourcePair(pair?: ResourceList): ResourceList | undefined {
  if (!pair) return undefined
  const cpu = pair.cpu?.trim()
  const memory = pair.memory?.trim()
  if (!cpu && !memory) return undefined
  return {
    ...(cpu ? { cpu } : {}),
    ...(memory ? { memory } : {}),
  }
}

export function buildPatchSpec(
  previous: WorkflowSpec,
  next: WorkflowSpec,
): Record<string, unknown> {
  const normalized = normalizeWorkflowSpec(next)
  const payload: Record<string, unknown> = { ...normalized }
  for (const key of Object.keys(previous)) {
    if (!Object.hasOwn(payload, key)) payload[key] = null
  }
  return payload
}

export function parseWorkflowSpecJSON(raw: string): WorkflowSpec {
  const parsed: unknown = JSON.parse(raw)
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error('Workflow spec must be a JSON object.')
  }
  const obj = parsed as Record<string, unknown>
  for (const key of Object.keys(obj)) {
    if (!ALLOWED_SPEC_KEYS.has(key)) {
      throw new Error(`Unknown workflow spec field "${key}".`)
    }
  }
  if (!Array.isArray(obj.steps)) {
    throw new Error('Workflow spec must include a "steps" array.')
  }
  return obj as WorkflowSpec
}

export function validateWorkflowDraft(
  id: string,
  rawSpec: WorkflowSpec,
  requireId = true,
): string[] {
  const issues: string[] = []
  const trimmedId = id.trim()
  if (requireId) {
    if (
      !trimmedId ||
      trimmedId.length > 253 ||
      !DNS1123_SUBDOMAIN.test(trimmedId)
    ) {
      issues.push(
        'Workflow ID must be a lowercase DNS-1123 subdomain (a-z, 0-9, "-", ".") up to 253 characters.',
      )
    }
  }

  if (rawSpec.description && rawSpec.description.length > 4096) {
    issues.push('Description must be at most 4096 characters.')
  }

  validateInputs(rawSpec.inputs ?? [], issues)
  const stepNames = validateSteps(rawSpec.steps ?? [], issues)
  validateOutputs(rawSpec.outputs ?? [], stepNames, issues)
  return issues
}

function validateInputs(inputs: Input[], issues: string[]) {
  if (inputs.length > 100) {
    issues.push('Workflow may declare at most 100 inputs.')
  }
  const seen = new Set<string>()
  for (const input of inputs) {
    const name = input.name.trim()
    if (!name || name.length > 63) {
      issues.push('Every workflow input must have a name of 1 to 63 characters.')
      continue
    }
    if (seen.has(name)) {
      issues.push(`Duplicate workflow input "${name}".`)
    }
    seen.add(name)
    if (!INPUT_TYPES.includes(input.type)) {
      issues.push(`Input "${name}" has unsupported type "${input.type}".`)
    }
    if (input.description && input.description.length > 1024) {
      issues.push(`Input "${name}" description exceeds 1024 characters.`)
    }
  }
}

function validateSteps(steps: WorkflowStep[], issues: string[]): Set<string> {
  const stepNames = new Set<string>()
  if (steps.length === 0 || steps.length > 100) {
    issues.push('Workflow must contain between 1 and 100 steps.')
    return stepNames
  }

  for (const step of steps) {
    const name = step.name.trim()
    if (!name || name.length > 63) {
      issues.push('Every step must have a name of 1 to 63 characters.')
      continue
    }
    if (stepNames.has(name)) {
      issues.push(`Duplicate step name "${name}".`)
    }
    stepNames.add(name)

    if (!STEP_TYPES.includes(step.type)) {
      issues.push(`Step "${name}" has unsupported type "${step.type}".`)
    }
    validateStepConfiguration(step, name, issues)
  }

  for (const step of steps) {
    const name = step.name.trim()
    if (!name) continue
    const seenDeps = new Set<string>()
    for (const rawDep of step.dependsOn ?? []) {
      const dep = rawDep.trim()
      if (dep === name) {
        issues.push(`Step "${name}" cannot depend on itself.`)
      } else if (!stepNames.has(dep)) {
        issues.push(`Step "${name}" depends on unknown step "${dep}".`)
      } else if (seenDeps.has(dep)) {
        issues.push(`Step "${name}" repeats dependency "${dep}".`)
      }
      seenDeps.add(dep)
    }
  }

  if (issues.length === 0 && hasCycle(steps)) {
    issues.push('Workflow step dependencies contain a cycle.')
  } else if (stepNames.size === steps.length && hasCycleSafe(steps, stepNames)) {
    if (!issues.includes('Workflow step dependencies contain a cycle.')) {
      issues.push('Workflow step dependencies contain a cycle.')
    }
  }

  return stepNames
}

function validateStepConfiguration(
  step: WorkflowStep,
  name: string,
  issues: string[],
) {
  const providerRef = step.providerRef?.trim() ?? ''
  const model = step.model?.trim() ?? ''

  if (step.type === 'ffmpeg') {
    if (providerRef) {
      issues.push(`FFmpeg step "${name}" must not set providerRef.`)
    }
    if (model) {
      issues.push(`FFmpeg step "${name}" must not set model.`)
    }
    if (!step.ffmpeg) {
      issues.push(`FFmpeg step "${name}" requires an FFmpeg operation.`)
    } else {
      validateFFmpegConfiguration(step.ffmpeg, name, issues)
    }
  } else {
    if (step.ffmpeg) {
      issues.push(`Step "${name}" (${step.type}) cannot include FFmpeg settings.`)
    }
    if (model && !providerRef) {
      issues.push(`Step "${name}" specifies a model without a providerRef.`)
    }
    if (providerRef.length > 253) {
      issues.push(`Step "${name}" providerRef exceeds 253 characters.`)
    }
    if (model.length > 253) {
      issues.push(`Step "${name}" model exceeds 253 characters.`)
    }
  }

  if (
    step.timeoutSeconds !== undefined &&
    (!Number.isInteger(step.timeoutSeconds) ||
      step.timeoutSeconds < 1 ||
      step.timeoutSeconds > 86400)
  ) {
    issues.push(`Step "${name}" timeoutSeconds must be an integer between 1 and 86400.`)
  }
  if (
    step.retry?.maxAttempts !== undefined &&
    (!Number.isInteger(step.retry.maxAttempts) ||
      step.retry.maxAttempts < 1 ||
      step.retry.maxAttempts > 100)
  ) {
    issues.push(`Step "${name}" retry maxAttempts must be between 1 and 100.`)
  }
}

function validateFFmpegConfiguration(
  ffmpeg: FFmpegSpec,
  stepName: string,
  issues: string[],
) {
  if (!FFMPEG_OPERATIONS.includes(ffmpeg.operation)) {
    issues.push(
      `FFmpeg step "${stepName}" has unsupported operation "${ffmpeg.operation}".`,
    )
    return
  }

  if (ffmpeg.inputs && ffmpeg.inputs.length > 32) {
    issues.push(`FFmpeg step "${stepName}" may list at most 32 inputs.`)
  }
  if (ffmpeg.operation === 'resize') {
    if (
      !ffmpeg.width ||
      !ffmpeg.height ||
      ffmpeg.width < 16 ||
      ffmpeg.width > 3840 ||
      ffmpeg.height < 16 ||
      ffmpeg.height > 3840
    ) {
      issues.push(
        `FFmpeg resize step "${stepName}" requires width and height between 16 and 3840.`,
      )
    }
  }
  if (ffmpeg.operation === 'aspect_ratio' && !ffmpeg.aspectRatio) {
    issues.push(
      `FFmpeg aspect_ratio step "${stepName}" requires an aspectRatio.`,
    )
  }
  if (ffmpeg.operation === 'add_audio' && !ffmpeg.audioInput?.trim()) {
    issues.push(`FFmpeg add_audio step "${stepName}" requires an audioInput.`)
  }
  if (
    ffmpeg.operation === 'trim' &&
    !(ffmpeg.startSeconds && ffmpeg.startSeconds > 0) &&
    !(ffmpeg.durationSeconds && ffmpeg.durationSeconds >= 0.1)
  ) {
    issues.push(
      `FFmpeg trim step "${stepName}" requires startSeconds > 0 or durationSeconds >= 0.1.`,
    )
  }
  if (
    ffmpeg.fps !== undefined &&
    (!Number.isInteger(ffmpeg.fps) || ffmpeg.fps < 1 || ffmpeg.fps > 60)
  ) {
    issues.push(`FFmpeg step "${stepName}" fps must be between 1 and 60.`)
  }
}

function validateOutputs(
  outputs: WorkflowOutput[],
  stepNames: Set<string>,
  issues: string[],
) {
  if (outputs.length > 100) {
    issues.push('Workflow may declare at most 100 outputs.')
  }
  const seen = new Set<string>()
  for (const output of outputs) {
    const name = output.name.trim()
    const from = output.from.trim()
    if (!name || name.length > 63) {
      issues.push('Every workflow output must have a name of 1 to 63 characters.')
      continue
    }
    if (seen.has(name)) {
      issues.push(`Duplicate workflow output "${name}".`)
    }
    seen.add(name)
    if (!from || !stepNames.has(from)) {
      issues.push(`Output "${name}" references unknown step "${from || '(empty)'}".`)
    }
  }
}

export function hasCycle(steps: WorkflowStep[]): boolean {
  const names = new Set(steps.map((step) => step.name.trim()).filter(Boolean))
  return hasCycleSafe(steps, names)
}

function hasCycleSafe(steps: WorkflowStep[], names: Set<string>): boolean {
  const indegree = new Map<string, number>()
  const dependents = new Map<string, string[]>()

  for (const name of names) {
    indegree.set(name, 0)
    dependents.set(name, [])
  }

  for (const step of steps) {
    const target = step.name.trim()
    if (!names.has(target)) continue
    const uniqueDeps = new Set(
      (step.dependsOn ?? []).map((d) => d.trim()).filter((d) => names.has(d)),
    )
    for (const dep of uniqueDeps) {
      if (dep === target) return true
      dependents.get(dep)!.push(target)
      indegree.set(target, (indegree.get(target) ?? 0) + 1)
    }
  }

  const queue: string[] = []
  for (const [name, degree] of indegree.entries()) {
    if (degree === 0) queue.push(name)
  }

  let visited = 0
  for (let i = 0; i < queue.length; i++) {
    visited++
    for (const next of dependents.get(queue[i]) ?? []) {
      const updated = (indegree.get(next) ?? 1) - 1
      indegree.set(next, updated)
      if (updated === 0) queue.push(next)
    }
  }

  return visited !== names.size
}
