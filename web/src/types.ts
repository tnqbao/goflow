export type Phase =
  | 'Pending'
  | 'Running'
  | 'Succeeded'
  | 'Failed'
  | 'Cancelling'
  | 'Cancelled'
  | 'RequiresReview'
export type Input = {
  name: string
  type: 'string' | 'integer' | 'number' | 'boolean'
  required?: boolean
  description?: string
}
export type WorkflowSpec = {
  description?: string
  inputs?: Input[]
  steps: {
    name: string
    type: string
    providerRef?: string
    dependsOn?: string[]
  }[]
  outputs?: { name: string; from: string }[]
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
