export type Phase =
  | 'Pending'
  | 'Running'
  | 'Succeeded'
  | 'Failed'
  | 'Cancelling'
  | 'Cancelled'
  | 'RequiresReview'

export type InputType = 'string' | 'integer' | 'number' | 'boolean'

export type Input = {
  name: string
  type: InputType
  required?: boolean
  description?: string
}

export type StepType = 'input' | 'llm' | 'image' | 'video' | 'ffmpeg' | 'output'

export type FFmpegOperation =
  | 'merge'
  | 'trim'
  | 'resize'
  | 'aspect_ratio'
  | 'add_audio'
  | 'normalize'
  | 'render_mp4'

export type AspectRatio = '16:9' | '9:16' | '1:1' | '4:3' | '4:5'

export type FFmpegSpec = {
  operation: FFmpegOperation
  inputs?: string[]
  audioInput?: string
  width?: number
  height?: number
  aspectRatio?: AspectRatio
  startSeconds?: number
  durationSeconds?: number
  fps?: number
}

export type ResourceList = {
  cpu?: string
  memory?: string
}

export type StepResources = {
  requests?: ResourceList
  limits?: ResourceList
}

export type WorkflowStep = {
  name: string
  type: StepType
  dependsOn?: string[]
  providerRef?: string
  model?: string
  ffmpeg?: FFmpegSpec
  retry?: { maxAttempts?: number }
  timeoutSeconds?: number
  resources?: StepResources
}

export type WorkflowOutput = {
  name: string
  from: string
}

export type WorkflowSpec = {
  description?: string
  inputs?: Input[]
  steps: WorkflowStep[]
  outputs?: WorkflowOutput[]
}

export type Workflow = {
  id: string
  resourceVersion: string
  spec: WorkflowSpec
}

export type RunStep = {
  name: string
  phase: Phase
  attempts: number
  startedAt?: string
  completedAt?: string
  error?: string
}

export type Run = {
  id: string
  workflowId: string
  phase: Phase
  createdAt: string
  cancelRequested: boolean
  startedAt?: string
  completedAt?: string
  error?: string
  steps: RunStep[]
}

export type Artifact = {
  id: string
  runId: string
  name: string
  contentType: string
  sizeBytes: number
  modifiedAt: string
}

export type ProviderKind = 'mock' | 'gemini' | 'fal'

export type Provider = {
  id: string
  type: ProviderKind
  enabled: boolean
  capabilities: string[]
  models?: string[]
  maxConcurrent?: number
  timeoutSeconds?: number
  credentialsReferenced: boolean
}

export type User = { username: string; csrfToken: string }

export type Token = {
  id: string
  name: string
  createdAt: string
  expiresAt: string
}
