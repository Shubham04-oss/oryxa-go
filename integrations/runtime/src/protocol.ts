export const operations = [
  'catalog.metadata',
  'auth.validate',
  'auth.connection-identifier',
  'property.options',
  'action.run',
  'trigger.enable',
  'trigger.disable',
  'trigger.renew',
  'trigger.handshake',
  'trigger.run',
  'events.verify',
  'events.parse-and-reply',
] as const

export type Operation = (typeof operations)[number]

export interface WebhookPayload {
  method: string
  headers: Record<string, string>
  queryParams: Record<string, string>
  body: unknown
  rawBody: string
}

export interface RunnerRequest {
  invocationId: string
  organizationId: string
  pieceName: string
  pieceVersion: string
  operation: Operation
  actionName?: string
  triggerName?: string
  propertyName?: string
  auth?: unknown
  props?: Record<string, unknown>
  webhookSecret?: string | Record<string, string>
  webhookUrl?: string
  publicUrl?: string
  providerPayload?: WebhookPayload
  initialStore?: Record<string, unknown>
  secondaryConnections?: Record<string, unknown>
  filesBroker?: {
    url: string
    token: string
  }
}

export interface StoreMutation {
  operation: 'put' | 'delete'
  key: string
  value?: unknown
  scope?: string
}

export interface Listener {
  events: string[]
  identifierValue: string
}

export interface RunnerResponse {
  invocationId: string
  output?: unknown
  storeMutations?: StoreMutation[]
  listeners?: Listener[]
  response?: {
    status: number
    headers?: Record<string, string>
    body?: unknown
  }
  tags?: string[]
}
