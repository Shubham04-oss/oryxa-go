import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import test from 'node:test'

import {
  createAction,
  createPiece,
  createTrigger,
  Property,
  TriggerStrategy,
} from '@activepieces/pieces-framework'

import type { Catalog } from './catalog.js'
import { execute, executeWithCatalog, RunnerError } from './runner.js'

const base = {
  invocationId: 'invocation-1',
  organizationId: 'org-1',
  pieceName: '@activepieces/piece-slack',
  pieceVersion: '0.17.10',
} as const

test('catalog exposes only explicitly allowed operations', async () => {
  const response = await execute({ ...base, operation: 'catalog.metadata' })
  const output = response.output as { actions: Record<string, unknown>; triggers: Record<string, unknown> }
  assert.deepEqual(Object.keys(output.actions), [])
  assert.deepEqual(Object.keys(output.triggers), ['new-message'])
  assert.doesNotThrow(() => JSON.stringify(output))
})

test('exact piece versions are mandatory', async () => {
  await assert.rejects(
    execute({ ...base, pieceVersion: '0.17.9', operation: 'catalog.metadata' }),
    (error: unknown) => error instanceof RunnerError && error.code === 'piece_not_allowed',
  )
})

test('Slack app webhook verifies the exact raw body and parses routing identity', async () => {
  const secret = 'signing-secret'
  const timestamp = '1789290000'
  const rawBody = JSON.stringify({
    team_id: 'T123',
    event: { type: 'message', channel_type: 'channel', channel: 'C123' },
  })
  const signature = `v0=${crypto.createHmac('sha256', secret).update(`v0:${timestamp}:${rawBody}`).digest('hex')}`
  const providerPayload = {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'x-slack-request-timestamp': timestamp,
      'x-slack-signature': signature,
    },
    queryParams: {},
    rawBody,
    body: JSON.parse(rawBody),
  }

  const verified = await execute({
    ...base,
    operation: 'events.verify',
    webhookSecret: secret,
    providerPayload,
  })
  assert.equal(verified.output, true)

  const parsed = await execute({
    ...base,
    operation: 'events.parse-and-reply',
    providerPayload,
  })
  assert.deepEqual(parsed.output, { event: 'message', identifierValue: 'T123' })

  const tampered = await execute({
    ...base,
    operation: 'events.verify',
    webhookSecret: secret,
    providerPayload: { ...providerPayload, rawBody: `${rawBody} ` },
  })
  assert.equal(tampered.output, false)
})

test('Slack challenge is returned synchronously by the upstream parser', async () => {
  const parsed = await execute({
    ...base,
    operation: 'events.parse-and-reply',
    providerPayload: {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      queryParams: {},
      rawBody: '{"challenge":"challenge-value"}',
      body: { challenge: 'challenge-value' },
    },
  })
  assert.deepEqual(parsed.output, { reply: { body: 'challenge-value', headers: {} } })
})

test('real Slack trigger binds its listener to the OAuth workspace and filters bot events', async () => {
  const auth = {
    access_token: 'xoxb-runtime-only',
    data: { team: { id: 'T123', name: 'Oryxa' } },
  }
  const enabled = await execute({
    ...base,
    operation: 'trigger.enable',
    triggerName: 'new-message',
    auth,
    props: { ignoreBots: true },
    webhookUrl: 'https://connect.example.test/v1/webhooks/opaque',
  })
  assert.deepEqual(enabled.listeners, [{ events: ['message'], identifierValue: 'T123' }])

  const human = await execute({
    ...base,
    operation: 'trigger.run',
    triggerName: 'new-message',
    auth,
    props: { ignoreBots: true },
    providerPayload: {
      method: 'POST', headers: {}, queryParams: {}, rawBody: '{}',
      body: { event: { type: 'message', channel_type: 'channel', channel: 'C123', user: 'U123', text: 'hello', ts: '1.23' } },
    },
  })
  assert.deepEqual(human.output, [{ type: 'message', channel_type: 'channel', channel: 'C123', user: 'U123', text: 'hello', ts: '1.23' }])

  const bot = await execute({
    ...base,
    operation: 'trigger.run',
    triggerName: 'new-message',
    auth,
    props: { ignoreBots: true },
    providerPayload: {
      method: 'POST', headers: {}, queryParams: {}, rawBody: '{}',
      body: { event: { type: 'message', channel_type: 'channel', channel: 'C123', bot_id: 'B123', ts: '1.24' } },
    },
  })
  assert.deepEqual(bot.output, [])
})

test('generic action and trigger contexts capture scoped effects', async () => {
  const action = createAction({
    name: 'echo',
    displayName: 'Echo',
    description: 'Echo input',
    props: { value: Property.ShortText({ displayName: 'Value', required: true }) },
    async run(context) {
      await context.tags.add({ name: 'tested' })
      return { value: context.propsValue.value, organization: await context.project.externalId() }
    },
  })
  const trigger = createTrigger({
    name: 'hook',
    displayName: 'Hook',
    description: 'Test webhook',
    props: {},
    type: TriggerStrategy.WEBHOOK,
    sampleData: {},
    async onEnable(context) { await context.store.put('provider-hook', 'hook-1') },
    async onDisable() {},
    async run(context) { return [context.payload.body] },
  })
  const fixture = createPiece({
    displayName: 'Fixture',
    logoUrl: '',
    authors: ['oryxa'],
    auth: undefined,
    actions: [action],
    triggers: [trigger],
  })
  const fixtureCatalog: Catalog = new Map([['@oryxa/piece-fixture', {
    piece: fixture,
    version: '1.0.0',
    actions: new Set(['echo']),
    triggers: new Set(['hook']),
    eventHooks: false,
  }]])
  const fixtureBase = {
    invocationId: 'invocation-fixture',
    organizationId: 'org-fixture',
    pieceName: '@oryxa/piece-fixture',
    pieceVersion: '1.0.0',
  } as const

  const actionResult = await executeWithCatalog(fixtureCatalog, {
    ...fixtureBase,
    operation: 'action.run',
    actionName: 'echo',
    props: { value: 'hello' },
  })
  assert.deepEqual(actionResult.output, { value: 'hello', organization: 'org-fixture' })
  assert.deepEqual(actionResult.tags, ['tested'])

  const enableResult = await executeWithCatalog(fixtureCatalog, {
    ...fixtureBase,
    operation: 'trigger.enable',
    triggerName: 'hook',
    webhookUrl: 'https://hooks.example.test/v1/webhooks/opaque',
  })
  assert.deepEqual(enableResult.storeMutations, [{
    operation: 'put', key: 'provider-hook', value: 'hook-1',
  }])
})
