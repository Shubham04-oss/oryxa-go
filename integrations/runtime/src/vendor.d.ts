declare module '@activepieces/piece-slack' {
  import type { Piece } from '@activepieces/pieces-framework'

  // The published Slack piece currently ships JavaScript without its own
  // declaration file. Keep the shim narrow so the runner itself remains
  // strictly typed while upstream fixes its package metadata independently.
  export const slack: Piece<any>
}
