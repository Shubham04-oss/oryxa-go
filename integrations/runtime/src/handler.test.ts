import assert from 'node:assert/strict'
import test from 'node:test'

import nock from 'nock'

import { handler } from './handler.js'

test('handler exposes stable errors for invalid and unapproved requests', async () => {
  const invalid = await handler({
    invocationId: '',
    organizationId: 'org-1',
    pieceName: '@activepieces/piece-slack',
    pieceVersion: '0.17.10',
    operation: 'catalog.metadata',
  }) as { ok: boolean; error: { code: string; message: string } }
  assert.equal(invalid.ok, false)
  assert.deepEqual(invalid.error, {
    code: 'invalid_request',
    message: 'invocationId and organizationId are required',
  })

  const unapproved = await handler({
    invocationId: 'invocation-1',
    organizationId: 'org-1',
    pieceName: '@activepieces/piece-unapproved',
    pieceVersion: '1.0.0',
    operation: 'catalog.metadata',
  }) as { ok: boolean; error: { code: string; message: string } }
  assert.equal(unapproved.ok, false)
  assert.equal(unapproved.error.code, 'piece_not_allowed')
  assert.equal(unapproved.error.message, 'piece and exact version are not allowed')
})

test('handler and hardened Microsoft bundle never expose or log provider failure payloads', async (t) => {
  t.after(() => nock.cleanAll())
  nock.disableNetConnect()
  t.after(() => nock.enableNetConnect())
  const leakedToken = 'eyJ-secret-access-token'
  const emailBody = 'private-email-body@example.test'
  const logged: unknown[][] = []
  const originalConsoleError = console.error
  console.error = (...args: unknown[]) => { logged.push(args) }
  t.after(() => { console.error = originalConsoleError })

  nock('https://graph.microsoft.com')
    .get('/v1.0/me/drive/items/root/children')
    .reply(401, { error: { code: 'InvalidAuthenticationToken', message: `${leakedToken} ${emailBody}` } })
  const result = await handler({
    invocationId: 'invocation-private-failure', organizationId: 'org-1',
    pieceName: '@activepieces/piece-microsoft-onedrive', pieceVersion: '0.4.7',
    operation: 'action.run', actionName: 'list_files',
    auth: { access_token: leakedToken }, props: { parentFolder: 'root' },
  }) as { ok: boolean; error: { code: string; message: string } }
  const serialized = JSON.stringify(result)
  assert.deepEqual(result, { ok: false, error: { code: 'piece_failed', message: 'piece execution failed' } })
  assert.equal(serialized.includes(leakedToken), false)
  assert.equal(serialized.includes(emailBody), false)
  // The Node test loader can emit a deprecation warning through console.error;
  // the pinned piece must never log either provider-controlled sentinel.
  const loggedText = JSON.stringify(logged)
  assert.equal(loggedText.includes(leakedToken), false)
  assert.equal(loggedText.includes(emailBody), false)
})
