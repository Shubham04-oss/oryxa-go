import { slack } from '@activepieces/piece-slack'
import { github } from '@activepieces/piece-github'
import { gmail } from '@activepieces/piece-gmail'
import { googleCalendar } from '@activepieces/piece-google-calendar'
import { googleDrive } from '@activepieces/piece-google-drive'

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
// exported by the upstream package. Slack starts with its app-level message
// trigger and the small reviewed action set used by the first vertical slice.
export const catalog: Catalog = new Map<string, CatalogEntry>([
  [
    '@activepieces/piece-slack',
    {
      piece: slack,
      version: '0.17.10',
      actions: new Set([
        'slack_post_message',
        'slack_add_reaction',
        'slack_find_channel',
      ]),
      triggers: new Set(['new-message']),
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
        'create_issue_comment_ai',
        'get_issue_ai',
        'list_pull_requests',
      ]),
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
  [
    '@activepieces/piece-gmail',
    {
      piece: gmail,
      version: '0.15.0',
      actions: new Set(['gmail_send_email']),
      triggers: new Set<string>(),
      eventHooks: false,
      // The pinned Gmail action returns the complete Google SDK transport
      // response, including an encoded copy of the submitted message. Only
      // the provider result data is allowed to cross the runner boundary.
      projectActionOutput: (_actionName: string, output: unknown) => {
        const data = (output as { data?: unknown } | null)?.data
        return data ?? {}
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
      ]),
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
  [
    '@activepieces/piece-google-drive',
    {
      piece: googleDrive,
      version: '0.9.1',
      actions: new Set(['drive_list_files', 'drive_get_file']),
      triggers: new Set<string>(),
      eventHooks: false,
    },
  ],
])
