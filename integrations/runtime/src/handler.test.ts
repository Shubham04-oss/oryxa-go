import assert from 'node:assert/strict'
import test from 'node:test'

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
