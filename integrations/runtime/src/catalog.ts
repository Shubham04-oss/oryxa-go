import { slack } from '@activepieces/piece-slack'

export interface CatalogEntry {
  piece: any
  version: string
  actions: ReadonlySet<string>
  triggers: ReadonlySet<string>
  eventHooks: boolean
}

export type Catalog = ReadonlyMap<string, CatalogEntry>

// This is intentionally an operation allowlist, not a mirror of everything
// exported by the upstream package. Slack starts with its app-level message
// trigger so the first vertical slice exercises real webhook routing.
export const catalog: Catalog = new Map([
  [
    '@activepieces/piece-slack',
    {
      piece: slack,
      version: '0.17.10',
      actions: new Set<string>(),
      triggers: new Set(['new-message']),
      eventHooks: true,
    },
  ],
])
