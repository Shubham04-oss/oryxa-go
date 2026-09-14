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
  const props = await flattenGitHubFileContent(
    request.actionName,
    await hydrateFileProps(request, request.props ?? {}),
  )
  const context = runtimeContext({ ...request, props })
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
      write: async ({ fileName, data }: { fileName: string; data: unknown }) => writeFile(request, fileName, data),
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

const maxFileBytes = 8 * 1024 * 1024

async function flattenGitHubFileContent(
  actionName: string | undefined,
  props: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  if (actionName !== 'create_or_update_file') {
    return props
  }
  const content = props.content
  if (!isPlainObject(content)) {
    return props
  }
  if (Buffer.isBuffer(content.data)) {
    return { ...props, content: content.data.toString('base64') }
  }
  if (typeof content.base64 === 'string') {
    return { ...props, content: content.base64 }
  }
  return props
}

async function hydrateFileProps(
  request: RunnerRequest,
  value: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const result: Record<string, unknown> = {}
  for (const [key, item] of Object.entries(value)) {
    result[key] = await hydrateValue(request, item)
  }
  return result
}

async function hydrateValue(request: RunnerRequest, value: unknown): Promise<unknown> {
  if (Array.isArray(value)) {
    return Promise.all(value.map((item) => hydrateValue(request, item)))
  }
  if (!isPlainObject(value)) {
    return value
  }
  if (isFileRef(value)) {
    return readFile(request, value.file_id)
  }
  const result: Record<string, unknown> = {}
  for (const [key, item] of Object.entries(value)) {
    result[key] = await hydrateValue(request, item)
  }
  return result
}

function isFileRef(value: Record<string, unknown>): value is { file_id: string } {
  const keys = Object.keys(value)
  return keys.length === 1 && keys[0] === 'file_id' && typeof value.file_id === 'string'
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) && !Buffer.isBuffer(value)
}

async function readFile(request: RunnerRequest, fileId: string): Promise<Record<string, unknown>> {
  const broker = requireFilesBroker(request)
  const response = await fetch(`${broker.url}/${encodeURIComponent(fileId)}`, {
    method: 'GET',
    headers: { Authorization: `Bearer ${broker.token}` },
  })
  if (!response.ok) {
    throw new RunnerError('file_read_failed', 'file storage read failed')
  }
  const bytes = Buffer.from(await response.arrayBuffer())
  if (bytes.length > maxFileBytes) {
    throw new RunnerError('file_too_large', 'file exceeds the size limit')
  }
  const filename = filenameFromDisposition(response.headers.get('content-disposition')) || fileId
  return apFile(filename, bytes)
}

async function writeFile(request: RunnerRequest, fileName: string, data: unknown): Promise<{ fileName: string; fileId: string }> {
  const broker = requireFilesBroker(request)
  const bytes = await toBuffer(data)
  if (bytes.length === 0 || bytes.length > maxFileBytes) {
    throw new RunnerError('file_too_large', 'file exceeds the size limit')
  }
  const response = await fetch(broker.url, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${broker.token}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ filename: fileName, content: bytes.toString('base64') }),
  })
  if (!response.ok) {
    throw new RunnerError('file_write_failed', 'file storage write failed')
  }
  const body = await response.json() as { id?: string; filename?: string }
  if (!body.id) {
    throw new RunnerError('file_write_failed', 'file storage write failed')
  }
  return { fileName: body.filename || fileName, fileId: body.id }
}

function requireFilesBroker(request: RunnerRequest): { url: string; token: string } {
  const broker = request.filesBroker
  if (!broker?.url || !broker.token) {
    throw new RunnerError('capability_not_supported', 'file storage is not enabled')
  }
  return broker
}

function apFile(filename: string, data: Buffer): Record<string, unknown> {
  const extension = filename.includes('.') ? filename.slice(filename.lastIndexOf('.') + 1) : ''
  return {
    filename,
    extension,
    data,
    get base64() {
      return data.toString('base64')
    },
  }
}

async function toBuffer(data: unknown): Promise<Buffer> {
  if (Buffer.isBuffer(data)) {
    return data
  }
  if (data instanceof Uint8Array) {
    return Buffer.from(data)
  }
  if (typeof data === 'string') {
    return Buffer.from(data)
  }
  if (data && typeof (data as AsyncIterable<unknown>)[Symbol.asyncIterator] === 'function') {
    const chunks: Buffer[] = []
    for await (const chunk of data as AsyncIterable<unknown>) {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as Uint8Array))
    }
    return Buffer.concat(chunks)
  }
  throw new RunnerError('invalid_request', 'file data is invalid')
}

function filenameFromDisposition(value: string | null): string {
  if (!value) {
    return ''
  }
  const match = /filename\*?=(?:UTF-8''|")?([^\";]+)/i.exec(value)
  return match ? match[1].replace(/"/g, '').trim() : ''
}
