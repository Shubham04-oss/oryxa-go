import { slack } from '@activepieces/piece-slack'
import { github } from '@activepieces/piece-github'
import { gmail } from '@activepieces/piece-gmail'
import { googleCalendar } from '@activepieces/piece-google-calendar'
import { googleDrive } from '@activepieces/piece-google-drive'
import { microsoftOutlook } from '@activepieces/piece-microsoft-outlook'
import { microsoftOutlookCalendar } from '@activepieces/piece-microsoft-outlook-calendar'
import { microsoftOneDrive } from '@activepieces/piece-microsoft-onedrive'

export interface CatalogEntry {
  piece: any
  version: string
  actions: ReadonlySet<string>
  triggers: ReadonlySet<string>
  eventHooks: boolean
  projectActionOutput?: (actionName: string, output: unknown) => unknown
}

export type Catalog = ReadonlyMap<string, CatalogEntry>

// This is intentionally an operation allowlist, not a mirror of everything
// exported by the upstream package. Slack covers the remaining unique
// non-file, non-waitpoint operations on the pinned piece, plus Events API
// triggers on a shared app-level webhook.
export const catalog: Catalog = new Map<string, CatalogEntry>([
  [
    '@activepieces/piece-slack',
    {
      piece: slack,
      version: '0.17.10',
      actions: new Set([
        'slack_post_message',
        'slack_send_direct_message',
        'slack_update_message',
        'slack_delete_message',
        'slack_send_ephemeral_message',
        'slack_schedule_message',
        'slack_list_scheduled_messages',
        'slack_delete_scheduled_message',
        'get-message',
        'slack_get_channel_history',
        'slack_get_thread_replies',
        'slack_get_message_permalink',
        'slack_search_messages',
        'slack_mark_conversation_read',
        'slack_close_dm',
        'slack_add_reaction',
        'slack_remove_reaction',
        'slack_get_reactions',
        'slack_find_channel',
        'slack_create_channel',
        'slack_archive_channel',
        'slack_unarchive_channel',
        'slack_rename_channel',
        'slack_set_channel_topic',
        'slack_set_channel_purpose',
        'slack_join_channel',
        'slack_leave_channel',
        'slack_invite_users_to_channel',
        'slack_remove_user_from_channel',
        'slack_list_channels',
        'slack_get_channel_info',
        'slack_list_channel_members',
        'find_user_by_email',
        'find_user_by_handle',
        'find-user-by-id',
        'get_user',
        'list_users',
        'set_user_status',
        'update_profile',
        'slack_list_user_conversations',
        'slack_list_user_reactions',
        'list_user_groups',
        'get_group_by_handle',
        'update_group_users',
        'slack_list_custom_emoji',
        'slack_list_files',
      ]),
      triggers: new Set([
        'new-message',
        'new-message-in-channel',
        'new-direct-message',
        'new_mention',
        'new-mention-in-direct-message',
        'new_reaction_added',
        'new_reaction_removed',
        'channel_created',
        'new_command',
        'new-command-in-direct-message',
        'new-user',
        'new-saved-message',
        'new-team-custom-emoji',
      ]),
      eventHooks: true,
    },
  ],
  [
    '@activepieces/piece-github',
    {
      piece: github,
      version: '0.8.11',
      actions: new Set([
        'create_issue_ai',
        'get_issue_ai',
        'list_repository_issues',
        'update_issue_ai',
        'create_issue_comment_ai',
        'list_issue_comments',
        'lock_issue_ai',
        'unlock_issue_ai',
        'add_labels_to_issue_ai',
        'remove_label_from_issue',
        'set_issue_labels',
        'list_issue_labels',
        'add_assignees_to_issue',
        'remove_assignees_from_issue',
        'list_assignees',
        'list_repository_labels',
        'create_label',
        'update_label',
        'create_pull_request',
        'get_pull_request',
        'list_pull_requests',
        'update_pull_request',
        'merge_pull_request',
        'list_pull_request_files',
        'list_pull_request_commits',
        'create_pull_request_review',
        'request_pull_request_reviewers',
        'create_pull_request_review_comment_ai',
        'list_pull_request_reviews',
        'list_pull_request_review_comments',
        'get_repository',
        'list_my_repositories',
        'list_user_repositories',
        'list_organization_repositories',
        'create_repository',
        'create_organization_repository',
        'update_repository',
        'list_branches',
        'get_branch',
        'create_branch_ai',
        'delete_branch_ai',
        'get_reference',
        'list_commits',
        'get_commit',
        'compare_commits',
        'list_contributors',
        'list_languages',
        'list_tags',
        'get_repository_tree',
        'search_repositories',
        'search_issues_and_pull_requests',
        'search_users',
        'get_authenticated_user',
        'get_user',
        'list_organization_members',
        'create_release',
        'list_releases',
        'get_release',
        'get_latest_release',
        'list_collaborators',
        'add_collaborator',
        'remove_collaborator',
        'create_milestone',
        'list_milestones',
        'get_milestone',
        'update_milestone',
        'star_repository',
        'unstar_repository',
        'list_starred_repositories',
        'check_repository_starred',
      ]),
      // Inbound new-issue / new-pull-request are native Go webhooks. The
      // runner must not expose piece trigger.run for GitHub.
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
  [
    '@activepieces/piece-gmail',
    {
      piece: gmail,
      version: '0.15.0',
      actions: new Set([
        'gmail_send_email',
        'gmail_reply_to_thread',
        'gmail_create_draft',
        'gmail_update_draft',
        'gmail_send_draft',
        'gmail_get_draft',
        'gmail_list_drafts',
        'gmail_delete_draft',
        'gmail_get_thread',
        'gmail_list_threads',
        'gmail_forward_message',
        'gmail_list_labels',
        'gmail_get_label',
        'gmail_create_label',
        'gmail_add_label_to_email',
        'gmail_remove_label_from_email',
        'gmail_archive_email',
        'gmail_get_profile',
        'gmail_list_history',
      ]),
      // Inbound new-email is a native Gmail users.watch. The runner must not
      // expose piece trigger.run for Gmail.
      triggers: new Set<string>(),
      eventHooks: false,
      // Graph-style Gmail send returns the complete Google SDK transport
      // response. Only send-email is projected to provider result data.
      projectActionOutput: (actionName: string, output: unknown) => {
        if (actionName === 'gmail_send_email') {
          const data = (output as { data?: unknown } | null)?.data
          return data ?? {}
        }
        return output ?? {}
      },
    },
  ],
  [
    '@activepieces/piece-google-calendar',
    {
      piece: googleCalendar,
      version: '0.10.3',
      actions: new Set([
        'google_calendar_create_event',
        'google_calendar_list_events',
        'google_calendar_update_event',
        'google_calendar_delete_event',
        'google_calendar_get_event',
        'google-calendar-add-attendees',
        'google_calendar_remove_attendee',
        'create_quick_event',
        'google_calendar_find_busy_periods',
        'google_calendar_find_free_slots',
        'google_calendar_search_events_all_calendars',
        'google_calendar_list_recurring_event_instances',
        'google_calendar_move_event',
        'google_calendar_import_event',
        'google_calendar_list_calendars',
        'google_calendar_get_calendar',
        'google_calendar_get_colors',
        'google_calendar_list_settings',
      ]),
      // Inbound calendar-event-changed is a native Calendar channel. The
      // runner must not expose piece trigger.run for Google Calendar.
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
  [
    '@activepieces/piece-google-drive',
    {
      piece: googleDrive,
      version: '0.9.1',
      actions: new Set([
        'drive_list_files',
        'drive_get_file',
        'drive_create_folder',
        'drive_search_files',
        'drive_copy_file',
        'drive_move_file',
        'drive_update_file_metadata',
        'drive_trash_file',
        'drive_untrash_file',
        'drive_delete_file',
        'drive_share_file',
        'drive_list_permissions',
        'drive_update_permission',
        'drive_remove_permission',
        'drive_list_shared_drives',
        'drive_get_shared_drive',
        'drive_create_shared_drive',
        'drive_update_shared_drive',
        'drive_create_comment',
        'drive_list_comments',
        'drive_create_reply',
        'drive_get_reply',
        'drive_list_replies',
        'drive_update_reply',
        'drive_delete_reply',
        'drive_get_about',
      ]),
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
  [
    '@activepieces/piece-microsoft-outlook',
    {
      piece: microsoftOutlook,
      version: '0.5.3',
      actions: new Set([
        'send-email',
        'reply-email',
        'createDraftEmail',
        'sendDraftEmail',
        'forwardEmail',
        'addLabelToEmail',
        'removeLabelFromEmail',
        'moveEmailToFolder',
        'findEmail',
      ]),
      triggers: new Set<string>(),
      eventHooks: false,
      // Graph sendMail returns 202 with an empty body. Other Outlook actions
      // return JSON identities that Go maps; only send-email is projected away.
      projectActionOutput: (actionName: string, output: unknown) => {
        if (actionName === 'send-email') {
          return {}
        }
        return output ?? {}
      },
    },
  ],
  [
    '@activepieces/piece-microsoft-outlook-calendar',
    {
      piece: microsoftOutlookCalendar,
      version: '0.2.8',
      // The piece calls these create_event, list_events, and delete_event.
      // Public API action aliases, if any, are owned by the Go integration
      // registry; the runtime intentionally exposes upstream operation names
      // only.
      actions: new Set(['create_event', 'list_events', 'delete_event']),
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
  [
    '@activepieces/piece-microsoft-onedrive',
    {
      piece: microsoftOneDrive,
      version: '0.4.7',
      actions: new Set(['list_files', 'list_folders', 'copy_file']),
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
])
