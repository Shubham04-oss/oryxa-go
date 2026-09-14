import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import test from 'node:test'

import nock from 'nock'

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

const githubBase = {
  invocationId: 'invocation-github',
  organizationId: 'org-1',
  pieceName: '@activepieces/piece-github',
  pieceVersion: '0.8.11',
} as const

const googlePieces = {
  gmail: {
    invocationId: 'invocation-google-gmail', organizationId: 'org-1',
    pieceName: '@activepieces/piece-gmail', pieceVersion: '0.15.0',
  },
  calendar: {
    invocationId: 'invocation-google-calendar', organizationId: 'org-1',
    pieceName: '@activepieces/piece-google-calendar', pieceVersion: '0.10.3',
  },
  drive: {
    invocationId: 'invocation-google-drive', organizationId: 'org-1',
    pieceName: '@activepieces/piece-google-drive', pieceVersion: '0.9.1',
  },
} as const

const microsoftPieces = {
  outlook: {
    invocationId: 'invocation-microsoft-outlook', organizationId: 'org-1',
    pieceName: '@activepieces/piece-microsoft-outlook', pieceVersion: '0.5.3',
  },
  calendar: {
    invocationId: 'invocation-microsoft-calendar', organizationId: 'org-1',
    pieceName: '@activepieces/piece-microsoft-outlook-calendar', pieceVersion: '0.2.8',
  },
  oneDrive: {
    invocationId: 'invocation-microsoft-onedrive', organizationId: 'org-1',
    pieceName: '@activepieces/piece-microsoft-onedrive', pieceVersion: '0.4.7',
  },
} as const

test('catalog exposes only explicitly allowed operations', async () => {
  const response = await execute({ ...base, operation: 'catalog.metadata' })
  const output = response.output as { actions: Record<string, unknown>; triggers: Record<string, unknown> }
  assert.deepEqual(Object.keys(output.actions).sort(), [
    'find-user-by-id',
    'find_user_by_email',
    'find_user_by_handle',
    'get-message',
    'get_group_by_handle',
    'get_user',
    'list_user_groups',
    'list_users',
    'set_user_status',
    'slack_add_reaction',
    'slack_archive_channel',
    'slack_close_dm',
    'slack_create_channel',
    'slack_delete_message',
    'slack_delete_scheduled_message',
    'slack_find_channel',
    'slack_get_channel_history',
    'slack_get_channel_info',
    'slack_get_message_permalink',
    'slack_get_reactions',
    'slack_get_thread_replies',
    'slack_invite_users_to_channel',
    'slack_join_channel',
    'slack_leave_channel',
    'slack_list_channel_members',
    'slack_list_channels',
    'slack_list_custom_emoji',
    'slack_list_files',
    'slack_list_scheduled_messages',
    'slack_list_user_conversations',
    'slack_list_user_reactions',
    'slack_mark_conversation_read',
    'slack_post_message',
    'slack_remove_reaction',
    'slack_remove_user_from_channel',
    'slack_rename_channel',
    'slack_schedule_message',
    'slack_search_messages',
    'slack_send_direct_message',
    'slack_send_ephemeral_message',
    'slack_set_channel_purpose',
    'slack_set_channel_topic',
    'slack_unarchive_channel',
    'slack_update_message',
    'update_group_users',
    'update_profile',
  ])
  assert.deepEqual(Object.keys(output.triggers).sort(), [
    'channel_created',
    'new-command-in-direct-message',
    'new-direct-message',
    'new-mention-in-direct-message',
    'new-message',
    'new-message-in-channel',
    'new-saved-message',
    'new-team-custom-emoji',
    'new-user',
    'new_command',
    'new_mention',
    'new_reaction_added',
    'new_reaction_removed',
  ])
  assert.doesNotThrow(() => JSON.stringify(output))
})

test('reviewed Slack actions execute the real pinned piece code', async (t) => {
  t.after(() => nock.cleanAll())
  const auth = { access_token: 'xoxb-runtime-only' }

  const post = nock('https://slack.com').post('/api/chat.postMessage').reply(200, {
    ok: true,
    channel: 'C123',
    ts: '1710304378.475129',
    message: { text: 'hello' },
  })
  const posted = await execute({
    ...base,
    operation: 'action.run',
    actionName: 'slack_post_message',
    auth,
    props: { channel: 'C123', text: 'hello' },
  })
  assert.equal((posted.output as { ts: string }).ts, '1710304378.475129')
  assert.equal(post.isDone(), true)

  const reaction = nock('https://slack.com').post('/api/reactions.add').reply(200, { ok: true })
  const reacted = await execute({
    ...base,
    operation: 'action.run',
    actionName: 'slack_add_reaction',
    auth,
    props: {
      channel: 'C123', ts: '1710304378.475129', reaction: 'thumbsup', reactAsUser: false,
    },
  })
  assert.equal((reacted.output as { ok: boolean }).ok, true)
  assert.equal(reaction.isDone(), true)

  const channelLookup = nock('https://slack.com').post('/api/conversations.list').reply(200, {
    ok: true,
    channels: [{ id: 'C123', name: 'general', is_private: false }],
    response_metadata: { next_cursor: '' },
  })
  const found = await execute({
    ...base,
    operation: 'action.run',
    actionName: 'slack_find_channel',
    auth,
    props: { name: 'general', excludeArchived: true },
  })
  assert.deepEqual(found.output, { id: 'C123', name: 'general', is_private: false })
  assert.equal(channelLookup.isDone(), true)

  const dm = nock('https://slack.com').post('/api/chat.postMessage').reply(200, {
    ok: true, channel: 'D123', ts: '1710304378.475130',
  })
  const messaged = await execute({
    ...base,
    operation: 'action.run',
    actionName: 'slack_send_direct_message',
    auth,
    props: { userId: 'U123', text: 'hello' },
  })
  assert.equal((messaged.output as { ts: string }).ts, '1710304378.475130')
  assert.equal(dm.isDone(), true)
})

test('reviewed GitHub actions execute the real pinned piece code', async (t) => {
  t.after(() => nock.cleanAll())
  const metadata = await execute({ ...githubBase, operation: 'catalog.metadata' })
  const exposed = metadata.output as { actions: Record<string, unknown>; triggers: Record<string, unknown> }
  assert.deepEqual(Object.keys(exposed.actions).sort(), [
    'add_assignees_to_issue', 'add_collaborator', 'add_labels_to_issue_ai',
    'check_repository_starred', 'compare_commits', 'create_branch_ai',
    'create_issue_ai', 'create_issue_comment_ai', 'create_label',
    'create_milestone', 'create_organization_repository', 'create_pull_request',
    'create_pull_request_review', 'create_pull_request_review_comment_ai',
    'create_release', 'create_repository', 'delete_branch_ai',
    'get_authenticated_user', 'get_branch', 'get_commit', 'get_issue_ai',
    'get_latest_release', 'get_milestone', 'get_pull_request', 'get_reference',
    'get_release', 'get_repository', 'get_repository_tree', 'get_user',
    'list_assignees', 'list_branches', 'list_collaborators', 'list_commits',
    'list_contributors', 'list_issue_comments', 'list_issue_labels',
    'list_languages', 'list_milestones', 'list_my_repositories',
    'list_organization_members', 'list_organization_repositories',
    'list_pull_request_commits', 'list_pull_request_files',
    'list_pull_request_review_comments', 'list_pull_request_reviews',
    'list_pull_requests', 'list_releases', 'list_repository_issues',
    'list_repository_labels', 'list_starred_repositories', 'list_tags',
    'list_user_repositories', 'lock_issue_ai', 'merge_pull_request',
    'remove_assignees_from_issue', 'remove_collaborator', 'remove_label_from_issue',
    'request_pull_request_reviewers', 'search_issues_and_pull_requests',
    'search_repositories', 'search_users', 'set_issue_labels', 'star_repository',
    'unlock_issue_ai', 'unstar_repository', 'update_issue_ai', 'update_label',
    'update_milestone', 'update_pull_request', 'update_repository',
  ])
  assert.deepEqual(Object.keys(exposed.triggers), [])

  const create = nock('https://api.github.com').post('/repos/oryxa/api/issues').reply(201, {
    number: 42,
    html_url: 'https://github.com/oryxa/api/issues/42',
    title: 'Bug',
    state: 'open',
  })
  const result = await execute({
    ...githubBase,
    operation: 'action.run',
    actionName: 'create_issue_ai',
    auth: { access_token: 'gho-runtime-only' },
    props: { owner: 'oryxa', repo: 'api', title: 'Bug' },
  })
  assert.equal((result.output as { number: number }).number, 42)
  assert.equal(create.isDone(), true)

  const list = nock('https://api.github.com').get(/\/repos\/oryxa\/api\/pulls.*/).reply(200, [{
    number: 7, html_url: 'https://github.com/oryxa/api/pull/7', title: 'Ready', state: 'open', draft: false,
  }])
  const pullRequests = await execute({
    ...githubBase,
    operation: 'action.run',
    actionName: 'list_pull_requests',
    auth: { access_token: 'gho-runtime-only' },
    props: { owner: 'oryxa', repo: 'api', state: 'open' },
  })
  assert.equal((pullRequests.output as { count: number }).count, 1)
  assert.equal(list.isDone(), true)

  const repository = nock('https://api.github.com').get('/repos/oryxa/api').reply(200, {
    full_name: 'oryxa/api', html_url: 'https://github.com/oryxa/api', private: false, default_branch: 'main',
  })
  const repo = await execute({
    ...githubBase,
    operation: 'action.run',
    actionName: 'get_repository',
    auth: { access_token: 'gho-runtime-only' },
    props: { owner: 'oryxa', repo: 'api' },
  })
  assert.equal((repo.output as { full_name: string }).full_name, 'oryxa/api')
  assert.equal(repository.isDone(), true)
})

test('reviewed Google Workspace actions execute the real pinned piece code', async (t) => {
  const expectedActions = [
    [googlePieces.gmail, [
      'gmail_send_email', 'gmail_reply_to_thread', 'gmail_create_draft', 'gmail_update_draft',
      'gmail_send_draft', 'gmail_get_draft', 'gmail_list_drafts', 'gmail_delete_draft',
      'gmail_get_thread', 'gmail_list_threads', 'gmail_forward_message', 'gmail_list_labels',
      'gmail_get_label', 'gmail_create_label', 'gmail_add_label_to_email',
      'gmail_remove_label_from_email', 'gmail_archive_email', 'gmail_get_profile', 'gmail_list_history',
    ]],
    [googlePieces.calendar, [
      'google_calendar_create_event', 'google_calendar_list_events', 'google_calendar_update_event',
      'google_calendar_delete_event', 'google_calendar_get_event', 'google-calendar-add-attendees',
      'google_calendar_remove_attendee', 'create_quick_event', 'google_calendar_find_busy_periods',
      'google_calendar_find_free_slots', 'google_calendar_search_events_all_calendars',
      'google_calendar_list_recurring_event_instances', 'google_calendar_move_event',
      'google_calendar_import_event', 'google_calendar_list_calendars', 'google_calendar_get_calendar',
      'google_calendar_get_colors', 'google_calendar_list_settings',
    ]],
    [googlePieces.drive, [
      'drive_list_files', 'drive_get_file', 'drive_create_folder', 'drive_search_files',
      'drive_copy_file', 'drive_move_file', 'drive_update_file_metadata', 'drive_trash_file',
      'drive_untrash_file', 'drive_delete_file', 'drive_share_file', 'drive_list_permissions',
      'drive_update_permission', 'drive_remove_permission', 'drive_list_shared_drives',
      'drive_get_shared_drive', 'drive_create_shared_drive', 'drive_update_shared_drive',
      'drive_create_comment', 'drive_list_comments', 'drive_create_reply', 'drive_get_reply',
      'drive_list_replies', 'drive_update_reply', 'drive_delete_reply', 'drive_get_about',
    ]],
  ] as const
  for (const [piece, actions] of expectedActions) {
    const metadata = await execute({ ...piece, operation: 'catalog.metadata' })
    const exposed = metadata.output as { actions: Record<string, unknown>; triggers: Record<string, unknown> }
    assert.deepEqual(Object.keys(exposed.actions).sort(), [...actions].sort())
    assert.deepEqual(Object.keys(exposed.triggers), [])
  }

  nock.disableNetConnect()
  nock('https://gmail.googleapis.com').post('/gmail/v1/users/me/messages/send')
    .reply(200, { id: 'message-1', threadId: 'thread-1' })
  nock('https://www.googleapis.com').post(/\/calendar\/v3\/calendars\/primary\/events.*/).reply(200, {
    id: 'event-1', status: 'confirmed', htmlLink: 'https://calendar.google.com/event?id=1',
    summary: 'Planning', start: { dateTime: '2026-09-14T09:00:00Z' },
    end: { dateTime: '2026-09-14T09:30:00Z' },
  })
  nock('https://www.googleapis.com').get(/\/drive\/v3\/files\/file-1.*/)
    .reply(200, { id: 'file-1', name: 'report.pdf', mimeType: 'application/pdf' })
  nock('https://www.googleapis.com').get(/\/drive\/v3\/files.*/).reply(200, {
    kind: 'drive#fileList', files: [{ id: 'file-1', name: 'report.pdf', mimeType: 'application/pdf' }],
  })
  t.after(() => {
    nock.enableNetConnect()
    nock.cleanAll()
  })

  const auth = { access_token: 'google-runtime-only' }
  const sent = await execute({
    ...googlePieces.gmail, operation: 'action.run', actionName: 'gmail_send_email', auth,
    props: {
      receiver: ['member@example.test'], subject: 'Hello', body_type: 'plain_text',
      body: 'Body', from: 'sender@example.test', draft: false,
    },
  })
  assert.deepEqual(sent.output, { id: 'message-1', threadId: 'thread-1' })
  assert.equal(JSON.stringify(sent.output).includes('google-runtime-only'), false)

  const created = await execute({
    ...googlePieces.calendar, operation: 'action.run', actionName: 'google_calendar_create_event', auth,
    props: {
      calendar_id: 'primary', title: 'Planning', start_date_time: '2026-09-14T09:00:00Z',
      end_date_time: '2026-09-14T09:30:00Z', send_notifications: 'none',
    },
  })
  assert.equal((created.output as { id: string }).id, 'event-1')

  const listed = await execute({
    ...googlePieces.drive, operation: 'action.run', actionName: 'drive_list_files', auth,
    props: { folder_id: 'root', depth_level: 1, download_files: false },
  })
  assert.equal((listed.output as { files: Array<{ id: string }> }).files[0]?.id, 'file-1')

  const file = await execute({
    ...googlePieces.drive, operation: 'action.run', actionName: 'drive_get_file', auth,
    props: { file_id: 'file-1' },
  })
  assert.equal((file.output as { id: string }).id, 'file-1')
})

test('reviewed Microsoft 365 actions execute the real pinned piece code', async (t) => {
  const expectedActions = [
    [microsoftPieces.outlook, [
      'send-email', 'reply-email', 'createDraftEmail', 'sendDraftEmail', 'forwardEmail',
      'addLabelToEmail', 'removeLabelFromEmail', 'moveEmailToFolder', 'findEmail',
    ]],
    [microsoftPieces.calendar, ['create_event', 'list_events', 'delete_event']],
    [microsoftPieces.oneDrive, ['list_files', 'list_folders', 'copy_file']],
  ] as const
  for (const [piece, actions] of expectedActions) {
    const metadata = await execute({ ...piece, operation: 'catalog.metadata' })
    const exposed = metadata.output as { actions: Record<string, unknown>; triggers: Record<string, unknown> }
    assert.deepEqual(Object.keys(exposed.actions).sort(), [...actions].sort())
    assert.deepEqual(Object.keys(exposed.triggers), [])
  }

  nock.disableNetConnect()
  const graph = nock('https://graph.microsoft.com')
    .post(/\/me\/sendMail$/, (body) => body.message.subject === 'Planning update')
    .reply(202)
    .post(/\/me\/messages$/, (body) => body.subject === 'Draft plan')
    .reply(201, { id: 'draft-1', subject: 'Draft plan' })
    .post('/v1.0/me/calendars/calendar-1/events', (body) => body.subject === 'Planning')
    .reply(201, {
      id: 'event-1', subject: 'Planning',
      start: { dateTime: '2026-09-14T09:00:00', timeZone: 'UTC' },
    })
    .get('/v1.0/me/calendars/calendar-1/events')
    .query({ '$filter': "subject eq 'Planning'" })
    .reply(200, { value: [{ id: 'event-1', subject: 'Planning' }] })
    .delete('/v1.0/me/calendars/calendar-1/events/event-1')
    .reply(204)
    .get('/v1.0/me/drive/items/root/children')
    .query({ '$filter': 'folder ne null' })
    .reply(200, { value: [{ id: 'folder-1', name: 'Archive', folder: {} }] })
    .get('/v1.0/me/drive/items/root/children')
    .reply(200, {
      value: [
        { id: 'file-1', name: 'report.pdf', file: { mimeType: 'application/pdf' } },
        { id: 'folder-1', name: 'Archive', folder: {} },
      ],
    })
  t.after(() => {
    nock.enableNetConnect()
    nock.cleanAll()
  })

  const auth = { access_token: 'microsoft-runtime-only', props: { cloud: 'login.microsoftonline.com' } }
  const sent = await execute({
    ...microsoftPieces.outlook, operation: 'action.run', actionName: 'send-email', auth,
    props: {
      recipients: ['member@example.test'], ccRecipients: [], bccRecipients: [],
      subject: 'Planning update', bodyFormat: 'text', body: 'The plan is ready.', attachments: [],
    },
  })
  assert.deepEqual(sent.output, {})

  const draft = await execute({
    ...microsoftPieces.outlook, operation: 'action.run', actionName: 'createDraftEmail', auth,
    props: {
      recipients: ['member@example.test'], ccRecipients: [], bccRecipients: [],
      subject: 'Draft plan', bodyFormat: 'text', body: 'Draft body.', attachments: [],
    },
  })
  assert.equal((draft.output as { id: string }).id, 'draft-1')

  const created = await execute({
    ...microsoftPieces.calendar, operation: 'action.run', actionName: 'create_event', auth,
    props: {
      calendarId: 'calendar-1', title: 'Planning', start: '2026-09-14T09:00:00Z',
      end: '2026-09-14T09:30:00Z', timezone: 'UTC', location: 'Remote',
    },
  })
  assert.equal((created.output as { id: string }).id, 'event-1')

  const events = await execute({
    ...microsoftPieces.calendar, operation: 'action.run', actionName: 'list_events', auth,
    props: { calendarId: 'calendar-1', filter: "subject eq 'Planning'" },
  })
  assert.equal((events.output as { value: Array<{ id: string }> }).value[0]?.id, 'event-1')

  await execute({
    ...microsoftPieces.calendar, operation: 'action.run', actionName: 'delete_event', auth,
    props: { calendarId: 'calendar-1', eventId: 'event-1' },
  })

  const folders = await execute({
    ...microsoftPieces.oneDrive, operation: 'action.run', actionName: 'list_folders', auth,
    props: { parentFolder: undefined },
  })
  assert.equal((folders.output as Array<{ id: string }>)[0]?.id, 'folder-1')

  const files = await execute({
    ...microsoftPieces.oneDrive, operation: 'action.run', actionName: 'list_files', auth,
    props: { parentFolder: undefined },
  })
  assert.deepEqual(files.output, [{
    id: 'file-1', name: 'report.pdf', file: { mimeType: 'application/pdf' },
  }])
  assert.equal(graph.isDone(), true)
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
