import { catalog, type Catalog, type CatalogEntry } from './catalog.js'
import type {
  Listener,
  RunnerRequest,
  RunnerResponse,
  StoreMutation,
} from './protocol.js'

export class RunnerError extends Error {
  constructor(
    public readonly code: string,
    message: string,
  ) {
    super(message)
    this.name = 'RunnerError'
  }
}

export async function execute(request: RunnerRequest): Promise<RunnerResponse> {
  return executeWithCatalog(catalog, request)
}

export async function executeWithCatalog(
  entries: Catalog,
  request: RunnerRequest,
): Promise<RunnerResponse> {
  validateEnvelope(request)
  const entry = getEntry(entries, request)
  const response: RunnerResponse = { invocationId: request.invocationId }

  switch (request.operation) {
    case 'catalog.metadata':
      response.output = metadata(entry)
      return response
    case 'auth.validate':
      response.output = await validateAuth(entry, request)
      return response
    case 'auth.connection-identifier':
      response.output = await connectionIdentifier(entry, request)
      return response
    case 'property.options':
      response.output = await propertyOptions(entry, request)
      return response
    case 'action.run':
      return runAction(entry, request, response)
    case 'events.verify':
      requireEventHooks(entry)
      response.output = entry.piece.events.verify({
        webhookSecret: required(request.webhookSecret, 'webhookSecret'),
        payload: required(request.providerPayload, 'providerPayload'),
        appWebhookUrl: request.webhookUrl ?? '',
      })
      return response
    case 'events.parse-and-reply':
      requireEventHooks(entry)
      response.output = entry.piece.events.parseAndReply({
        payload: required(request.providerPayload, 'providerPayload'),
        server: { publicUrl: request.publicUrl ?? '' },
      })
      return response
    case 'trigger.enable':
    case 'trigger.disable':
    case 'trigger.renew':
    case 'trigger.handshake':
    case 'trigger.run':
      return runTrigger(entry, request, response)
  }
}

function validateEnvelope(request: RunnerRequest): void {
  if (!request.invocationId || !request.organizationId) {
    throw new RunnerError('invalid_request', 'invocationId and organizationId are required')
  }
  if (!request.pieceName || !request.pieceVersion) {
    throw new RunnerError('invalid_request', 'pieceName and pieceVersion are required')
  }
}

function getEntry(entries: Catalog, request: RunnerRequest): CatalogEntry {
  const entry = entries.get(request.pieceName)
  if (!entry || entry.version !== request.pieceVersion) {
    throw new RunnerError('piece_not_allowed', 'piece and exact version are not allowed')
  }
  return entry
}

function metadata(entry: CatalogEntry): unknown {
  const raw = entry.piece.metadata()
  const actions = Object.fromEntries(
    Object.entries(raw.actions ?? {}).filter(([name]) => entry.actions.has(name)),
  )
  const triggers = Object.fromEntries(
    Object.entries(raw.triggers ?? {}).filter(([name]) => entry.triggers.has(name)),
  )
  return serializable({ ...raw, actions, triggers })
}

async function validateAuth(entry: CatalogEntry, request: RunnerRequest): Promise<unknown> {
  const auth = required(request.auth, 'auth') as Record<string, unknown>
  const definition = authDefinition(entry, String(auth.type ?? ''))
  if (typeof definition.validate !== 'function') {
    return { valid: true }
  }
  const value = authValue(definition.type, auth)
  return definition.validate({ auth: value, server: authServer(request) })
}

async function connectionIdentifier(entry: CatalogEntry, request: RunnerRequest): Promise<unknown> {
  const auth = required(request.auth, 'auth') as Record<string, unknown>
  const definition = authDefinition(entry, String(auth.type ?? ''))
  if (typeof definition.getConnectionIdentifier !== 'function') {
    return { identifier: undefined }
  }
  const value = authValue(definition.type, auth)
  const identifier = await definition.getConnectionIdentifier({
    auth: value,
    server: server(request),
  })
  return { identifier }
}

function authDefinition(entry: CatalogEntry, type: string): any {
  const definitions = Array.isArray(entry.piece.auth) ? entry.piece.auth : [entry.piece.auth]
  const definition = definitions.find((candidate: any) => candidate?.type === type)
  if (!definition) {
    throw new RunnerError('auth_type_not_allowed', 'connection auth type does not match the piece')
  }
  return definition
}

function authValue(type: string, auth: Record<string, unknown>): unknown {
  switch (type) {
    case 'SECRET_TEXT':
      return auth.secret_text
    case 'CUSTOM_AUTH':
    case 'OIDC':
      return auth.props
    default:
      return auth
  }
}

async function propertyOptions(entry: CatalogEntry, request: RunnerRequest): Promise<unknown> {
  const operationName = request.actionName ?? request.triggerName
  if (!operationName || !request.propertyName) {
    throw new RunnerError('invalid_request', 'operation and property names are required')
  }
  const operation = allowedAction(entry, operationName, false) ?? allowedTrigger(entry, operationName, false)
  if (!operation) {
    throw new RunnerError('operation_not_allowed', 'action or trigger is not allowed')
  }
  const property = operation.props?.[request.propertyName]
  if (!property || typeof property.options !== 'function') {
    throw new RunnerError('property_not_executable', 'property has no dynamic options function')
  }
  return property.options(request.props ?? {}, propertyContext(request))
}

async function runAction(
  entry: CatalogEntry,
  request: RunnerRequest,
  response: RunnerResponse,
): Promise<RunnerResponse> {
  const action = allowedAction(entry, required(request.actionName, 'actionName'))
  const context = runtimeContext(request)
  const output = await action.run(context.value)
  response.output = entry.projectActionOutput
    ? entry.projectActionOutput(request.actionName!, output)
    : output
  response.storeMutations = context.storeMutations
  response.tags = context.tags
  const hookResponse = context.response()
  if (hookResponse !== undefined) {
    response.response = hookResponse
  }
  return response
}

async function runTrigger(
  entry: CatalogEntry,
  request: RunnerRequest,
  response: RunnerResponse,
): Promise<RunnerResponse> {
  const trigger = allowedTrigger(entry, required(request.triggerName, 'triggerName'))
  const context = runtimeContext(request)
  switch (request.operation) {
    case 'trigger.enable':
      response.output = await trigger.onEnable(context.value)
      break
    case 'trigger.disable':
      response.output = await trigger.onDisable(context.value)
      break
    case 'trigger.renew':
      response.output = await trigger.onRenew(context.value)
      break
    case 'trigger.handshake':
      response.output = await trigger.onHandshake(context.value)
      break
    case 'trigger.run':
      response.output = await trigger.run(context.value)
      break
    default:
      throw new RunnerError('operation_not_allowed', 'unsupported trigger operation')
  }
  response.storeMutations = context.storeMutations
  response.listeners = context.listeners
  response.tags = context.tags
  const hookResponse = context.response()
  if (hookResponse !== undefined) {
    response.response = hookResponse
  }
  return response
}

function allowedAction(entry: CatalogEntry, name: string, fail = true): any {
  const action = entry.actions.has(name) ? entry.piece.getAction(name) : undefined
  if (!action && fail) {
    throw new RunnerError('operation_not_allowed', 'action is not allowed')
  }
  return action
}

function allowedTrigger(entry: CatalogEntry, name: string, fail = true): any {
  const trigger = entry.triggers.has(name) ? entry.piece.getTrigger(name) : undefined
  if (!trigger && fail) {
    throw new RunnerError('operation_not_allowed', 'trigger is not allowed')
  }
  return trigger
}

function requireEventHooks(entry: CatalogEntry): void {
  if (!entry.eventHooks || !entry.piece.events) {
    throw new RunnerError('operation_not_allowed', 'piece event hooks are not allowed')
  }
}

function runtimeContext(request: RunnerRequest): {
  value: any
  storeMutations: StoreMutation[]
  listeners: Listener[]
  tags: string[]
  response: () => RunnerResponse['response']
} {
  const state = new Map(Object.entries(request.initialStore ?? {}))
  const storeMutations: StoreMutation[] = []
  const listeners: Listener[] = []
  const tags: string[] = []
  let hookResponse: RunnerResponse['response']

  const value: any = {
    executionType: 'BEGIN',
    auth: request.auth,
    propsValue: request.props ?? {},
    payload: request.providerPayload,
    webhookUrl: request.webhookUrl ?? '',
    server: server(request),
    step: { name: request.actionName ?? request.triggerName ?? '' },
    project: {
      id: request.organizationId,
      externalId: async () => request.organizationId,
    },
    store: {
      get: async (key: string) => state.get(key) ?? null,
      put: async (key: string, item: unknown, scope?: string) => {
        state.set(key, item)
        storeMutations.push({ operation: 'put', key, value: item, ...(scope ? { scope } : {}) })
        return item
      },
      delete: async (key: string, scope?: string) => {
        state.delete(key)
        storeMutations.push({ operation: 'delete', key, ...(scope ? { scope } : {}) })
      },
    },
    app: {
      createListeners: (listener: Listener) => listeners.push(listener),
    },
    output: { update: async () => undefined },
    tags: { add: async ({ name }: { name: string }) => { tags.push(name) } },
    files: {
      write: async () => {
        throw new RunnerError('capability_not_supported', 'file storage is not enabled')
      },
    },
    connections: {
      get: async (key: string) => request.secondaryConnections?.[key] ?? null,
    },
    flows: {
      list: async () => {
        throw new RunnerError('capability_not_supported', 'flow APIs are not enabled')
      },
      current: { id: '', version: { id: '' } },
    },
    run: {
      id: request.invocationId,
      stop: ({ response }: any = { response: {} }) => { hookResponse = normalizeResponse(response) },
      respond: ({ response }: any = { response: {} }) => { hookResponse = normalizeResponse(response) },
      createWaitpoint: async () => {
        throw new RunnerError('capability_not_supported', 'waitpoints are not enabled')
      },
      waitForWaitpoint: () => {
        throw new RunnerError('capability_not_supported', 'waitpoints are not enabled')
      },
    },
    setSchedule: () => {
      throw new RunnerError('capability_not_supported', 'polling schedules are not enabled')
    },
  }

  return { value, storeMutations, listeners, tags, response: () => hookResponse }
}

function propertyContext(request: RunnerRequest): any {
  const context = runtimeContext(request)
  return {
    server: context.value.server,
    project: context.value.project,
    flows: context.value.flows,
    connections: context.value.connections,
  }
}

function server(request: RunnerRequest): { apiUrl: string; publicUrl: string; token: string } {
  return { apiUrl: '', publicUrl: request.publicUrl ?? '', token: '' }
}

function authServer(request: RunnerRequest): any {
  return {
    apiUrl: '',
    publicUrl: request.publicUrl ?? '',
    mintOidcToken: async () => {
      throw new RunnerError('capability_not_supported', 'OIDC token minting is not enabled')
    },
  }
}

function normalizeResponse(value: any): NonNullable<RunnerResponse['response']> {
  return {
    status: typeof value?.status === 'number' ? value.status : 200,
    ...(value?.headers ? { headers: value.headers } : {}),
    ...(value && 'body' in value ? { body: value.body } : {}),
  }
}

function serializable(value: unknown): unknown {
  return JSON.parse(JSON.stringify(value))
}

function required<T>(value: T | undefined, name: string): T {
  if (value === undefined || value === null || value === '') {
    throw new RunnerError('invalid_request', `${name} is required`)
  }
  return value
}
