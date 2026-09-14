import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { createServer } from 'node:http'
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
  docs: {
    invocationId: 'invocation-google-docs', organizationId: 'org-1',
    pieceName: '@activepieces/piece-google-docs', pieceVersion: '0.4.9',
  },
  sheets: {
    invocationId: 'invocation-google-sheets', organizationId: 'org-1',
    pieceName: '@activepieces/piece-google-sheets', pieceVersion: '0.16.11',
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
  teams: {
    invocationId: 'invocation-microsoft-teams', organizationId: 'org-1',
    pieceName: '@activepieces/piece-microsoft-teams', pieceVersion: '0.6.1',
  },
  sharePoint: {
    invocationId: 'invocation-microsoft-sharepoint', organizationId: 'org-1',
    pieceName: '@activepieces/piece-microsoft-sharepoint', pieceVersion: '0.3.8',
  },
} as const

test('catalog exposes only explicitly allowed operations', async () => {
  const response = await execute({ ...base, operation: 'catalog.metadata' })
  const output = response.output as { actions: Record<string, unknown>; triggers: Record<string, unknown> }
  assert.deepEqual(Object.keys(output.actions).sort(), [
    'custom_api_call',
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
    'slack_get_file',
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
    'uploadFile',
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
    'create_milestone', 'create_or_update_file', 'create_organization_repository', 'create_pull_request',
    'create_pull_request_review', 'create_pull_request_review_comment_ai',
    'create_release', 'create_repository', 'custom_api_call', 'delete_branch_ai', 'delete_file',
    'get_authenticated_user', 'get_branch', 'get_commit', 'get_file_content', 'get_issue_ai',
    'get_latest_release', 'get_milestone', 'get_pull_request', 'get_reference',
    'get_release', 'get_repository', 'get_repository_tree', 'get_user', 'github_create_gist',
    'list_assignees', 'list_branches', 'list_collaborators', 'list_commits',
    'list_contributors', 'list_issue_comments', 'list_issue_labels',
    'list_languages', 'list_milestones', 'list_my_repositories',
    'list_organization_members', 'list_organization_repositories',
    'list_pull_request_commits', 'list_pull_request_files',
    'list_pull_request_review_comments', 'list_pull_request_reviews',
    'list_pull_requests', 'list_releases', 'list_repository_issues',
    'list_repository_labels', 'list_starred_repositories', 'list_tags',
    'list_user_repositories', 'lock_issue_ai', 'merge_pull_request',
    'rawGraphqlQuery', 'remove_assignees_from_issue', 'remove_collaborator', 'remove_label_from_issue',
    'request_pull_request_reviewers', 'search_code', 'search_issues_and_pull_requests',
    'search_repositories', 'search_users', 'set_issue_labels', 'star_repository',
    'trigger_workflow_dispatch', 'unlock_issue_ai', 'unstar_repository', 'update_issue_ai', 'update_label',
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
      'gmail_get_attachment', 'gmail_get_message', 'gmail_search_email', 'custom_api_call',
    ]],
    [googlePieces.calendar, [
      'google_calendar_create_event', 'google_calendar_list_events', 'google_calendar_update_event',
      'google_calendar_delete_event', 'google_calendar_get_event', 'google-calendar-add-attendees',
      'google_calendar_remove_attendee', 'create_quick_event', 'google_calendar_find_busy_periods',
      'google_calendar_find_free_slots', 'google_calendar_search_events_all_calendars',
      'google_calendar_list_recurring_event_instances', 'google_calendar_move_event',
      'google_calendar_import_event', 'google_calendar_list_calendars', 'google_calendar_get_calendar',
      'google_calendar_get_colors', 'google_calendar_list_settings', 'custom_api_call',
    ]],
    [googlePieces.drive, [
      'drive_list_files', 'drive_get_file', 'drive_create_folder', 'drive_search_files',
      'drive_copy_file', 'drive_move_file', 'drive_update_file_metadata', 'drive_trash_file',
      'drive_untrash_file', 'drive_delete_file', 'drive_share_file', 'drive_list_permissions',
      'drive_update_permission', 'drive_remove_permission', 'drive_list_shared_drives',
      'drive_get_shared_drive', 'drive_create_shared_drive', 'drive_update_shared_drive',
      'drive_create_comment', 'drive_list_comments', 'drive_create_reply', 'drive_get_reply',
      'drive_list_replies', 'drive_update_reply', 'drive_delete_reply', 'drive_get_about',
      'drive_upload_file', 'drive_replace_file_content', 'drive_download_file', 'drive_export_workspace_file',
      'drive_create_file_from_text', 'drive_save_file_as_pdf', 'drive_set_public_access',
      'drive_empty_trash', 'drive_delete_shared_drive', 'drive_upload_from_url', 'custom_api_call',
    ]],
    [googlePieces.docs, [
      'append_text', 'batch_update', 'copy_document', 'create_and_populate_table', 'create_document',
      'create_document_based_on_template', 'create_document_from_markdown', 'create_footer',
      'create_footnote', 'create_header', 'create_named_range', 'create_paragraph_bullets',
      'create_text_document', 'custom_api_call', 'delete_content_range', 'delete_footer',
      'delete_header', 'delete_named_range', 'delete_paragraph_bullets', 'delete_table_column',
      'delete_table_row', 'export_as_pdf', 'get_document', 'get_document_end_index',
      'get_document_plaintext', 'insert_image_in_table_cell', 'insert_inline_image', 'insert_page_break',
      'insert_table', 'insert_table_column', 'insert_table_row', 'insert_text',
      'insert_text_in_table_cell', 'read_document', 'replace_all_text', 'replace_body_with_markdown',
      'replace_image', 'replace_section_with_markdown', 'search_documents', 'unmerge_table_cells',
      'update_document_style', 'update_table_row_style',
    ]],
    [googlePieces.sheets, [
      'custom_api_call', 'sheets_add_column', 'sheets_add_multiple_rows', 'sheets_add_row',
      'sheets_add_row_at_top', 'sheets_add_worksheet', 'sheets_append_dimension', 'sheets_append_values',
      'sheets_auto_resize_dimensions', 'sheets_clear_values', 'sheets_copy_worksheet',
      'sheets_create_spreadsheet', 'sheets_delete_dimension', 'sheets_delete_multiple_rows',
      'sheets_delete_row', 'sheets_delete_worksheet', 'sheets_export_worksheet',
      'sheets_find_or_create_row', 'sheets_find_or_create_worksheet', 'sheets_find_rows',
      'sheets_find_worksheet', 'sheets_format_cells', 'sheets_get_all_rows', 'sheets_get_row',
      'sheets_get_spreadsheet', 'sheets_get_values', 'sheets_insert_dimension', 'sheets_rename_worksheet',
      'sheets_search_spreadsheets', 'sheets_update_dimension_properties', 'sheets_update_multiple_rows',
      'sheets_update_row', 'sheets_update_sheet_properties', 'sheets_update_values',
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
      'addLabelToEmail', 'removeLabelFromEmail', 'moveEmailToFolder', 'findEmail', 'downloadAttachment',
      'custom_api_call',
    ]],
    [microsoftPieces.calendar, ['create_event', 'list_events', 'delete_event']],
    [microsoftPieces.oneDrive, ['list_files', 'list_folders', 'copy_file', 'upload_onedrive_file', 'download_file']],
    [microsoftPieces.teams, [
      'custom_api_call', 'microsoft_teams_create_channel', 'microsoft_teams_create_chat_and_send_message',
      'microsoft_teams_create_private_channel', 'microsoft_teams_delete_chat_message',
      'microsoft_teams_find_channel', 'microsoft_teams_find_team_member',
      'microsoft_teams_get_channel_message', 'microsoft_teams_get_chat_message',
      'microsoft_teams_get_meeting_recording', 'microsoft_teams_get_meeting_transcript',
      'microsoft_teams_reply_to_channel_message', 'microsoft_teams_send_channel_message',
      'microsoft_teams_send_chat_message',
    ]],
    [microsoftPieces.sharePoint, [
      'custom_api_call', 'microsoft_sharepoint_copy_item', 'microsoft_sharepoint_copy_item_within_site',
      'microsoft_sharepoint_create_folder', 'microsoft_sharepoint_create_list',
      'microsoft_sharepoint_create_list_item', 'microsoft_sharepoint_delete_list_item',
      'microsoft_sharepoint_find_file', 'microsoft_sharepoint_find_site',
      'microsoft_sharepoint_get_folder_contents', 'microsoft_sharepoint_get_site_information',
      'microsoft_sharepoint_move_file', 'microsoft_sharepoint_publish_page',
      'microsoft_sharepoint_search_list_item', 'microsoft_sharepoint_update_list_item',
      'microsoft_sharepoint_upload_file',
    ]],
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

test('file refs hydrate and files.write uses the invocation broker', async (t) => {
  const writes: Array<{ filename?: string; content?: string }> = []
  const server = createServer((request, response) => {
    if (request.method === 'GET' && request.url?.includes('file_abc')) {
      assert.equal(request.headers.authorization, 'Bearer fbk_1')
      response.setHeader('Content-Disposition', 'attachment; filename="note.txt"')
      response.end(Buffer.from('hello'))
      return
    }
    if (request.method === 'POST') {
      assert.equal(request.headers.authorization, 'Bearer fbk_1')
      const chunks: Buffer[] = []
      request.on('data', (chunk) => chunks.push(Buffer.from(chunk)))
      request.on('end', () => {
        writes.push(JSON.parse(Buffer.concat(chunks).toString()) as { filename?: string; content?: string })
        response.writeHead(201, { 'Content-Type': 'application/json' })
        response.end(JSON.stringify({ id: 'file_xyz', filename: 'out.bin' }))
      })
      return
    }
    response.writeHead(404)
    response.end()
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  t.after(() => server.close())
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('broker listen failed')
  }

  const roundtrip = createAction({
    name: 'roundtrip',
    displayName: 'Roundtrip',
    description: 'Hydrate and write a file',
    props: {},
    async run(context) {
      const file = context.propsValue.file as { filename: string; data: Buffer }
      const written = await context.files.write({ fileName: 'out.bin', data: file.data })
      return { filename: file.filename, text: file.data.toString(), written }
    },
  })
  const fixture = createPiece({
    displayName: 'Fixture', logoUrl: '', authors: ['oryxa'], auth: undefined,
    actions: [roundtrip], triggers: [],
  })
  const catalog: Catalog = new Map([['@oryxa/piece-files', {
    piece: fixture, version: '1.0.0', actions: new Set(['roundtrip']),
    triggers: new Set<string>(), eventHooks: false,
  }]])
  const result = await executeWithCatalog(catalog, {
    invocationId: 'invocation-files', organizationId: 'org-1',
    pieceName: '@oryxa/piece-files', pieceVersion: '1.0.0',
    operation: 'action.run', actionName: 'roundtrip',
    props: { file: { file_id: 'file_abc' } },
    filesBroker: { url: `http://127.0.0.1:${address.port}`, token: 'fbk_1' },
  })
  assert.deepEqual(result.output, {
    filename: 'note.txt', text: 'hello', written: { fileName: 'out.bin', fileId: 'file_xyz' },
  })
  assert.equal(writes.length, 1)
  assert.equal(writes[0]?.filename, 'out.bin')
})

test('GitHub create_or_update_file flattens hydrated ApFile to base64', async (t) => {
  const server = createServer((request, response) => {
    if (request.method === 'GET' && request.url?.includes('file_abc')) {
      response.setHeader('Content-Disposition', 'attachment; filename="note.txt"')
      response.end(Buffer.from('hello'))
      return
    }
    response.writeHead(404)
    response.end()
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  t.after(() => server.close())
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('broker listen failed')
  }
  const writeFile = createAction({
    name: 'create_or_update_file',
    displayName: 'Create or update file',
    description: 'Receive flattened base64 content',
    props: {},
    async run(context) {
      return { content: context.propsValue.content }
    },
  })
  const fixture = createPiece({
    displayName: 'Fixture', logoUrl: '', authors: ['oryxa'], auth: undefined,
    actions: [writeFile], triggers: [],
  })
  const catalog: Catalog = new Map([['@oryxa/piece-github-files', {
    piece: fixture, version: '1.0.0', actions: new Set(['create_or_update_file']),
    triggers: new Set<string>(), eventHooks: false,
  }]])
  const result = await executeWithCatalog(catalog, {
    invocationId: 'invocation-github-file', organizationId: 'org-1',
    pieceName: '@oryxa/piece-github-files', pieceVersion: '1.0.0',
    operation: 'action.run', actionName: 'create_or_update_file',
    props: { content: { file_id: 'file_abc' } },
    filesBroker: { url: `http://127.0.0.1:${address.port}`, token: 'fbk_1' },
  })
  assert.deepEqual(result.output, { content: Buffer.from('hello').toString('base64') })
})

test('files.write fails closed without a broker', async () => {
  const writeOnly = createAction({
    name: 'write-only',
    displayName: 'Write',
    description: 'Write a file',
    props: {},
    async run(context) {
      return context.files.write({ fileName: 'out.bin', data: Buffer.from('hello') })
    },
  })
  const fixture = createPiece({
    displayName: 'Fixture', logoUrl: '', authors: ['oryxa'], auth: undefined,
    actions: [writeOnly], triggers: [],
  })
  const catalog: Catalog = new Map([['@oryxa/piece-files', {
    piece: fixture, version: '1.0.0', actions: new Set(['write-only']),
    triggers: new Set<string>(), eventHooks: false,
  }]])
  await assert.rejects(
    () => executeWithCatalog(catalog, {
      invocationId: 'invocation-files', organizationId: 'org-1',
      pieceName: '@oryxa/piece-files', pieceVersion: '1.0.0',
      operation: 'action.run', actionName: 'write-only',
    }),
    (error: unknown) => error instanceof RunnerError && error.code === 'capability_not_supported',
  )
})
