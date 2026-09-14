declare module '@activepieces/piece-slack' {
  import type { Piece } from '@activepieces/pieces-framework'

  // The published Slack piece currently ships JavaScript without its own
  // declaration file. Keep the shim narrow so the runner itself remains
  // strictly typed while upstream fixes its package metadata independently.
  export const slack: Piece<any>
}

declare module '@activepieces/piece-github' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const github: Piece<any>
}

declare module '@activepieces/piece-gmail' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const gmail: Piece<any>
}

declare module '@activepieces/piece-google-calendar' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const googleCalendar: Piece<any>
}

declare module '@activepieces/piece-google-drive' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const googleDrive: Piece<any>
}

declare module '@activepieces/piece-google-docs' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const googleDocs: Piece<any>
}

declare module '@activepieces/piece-google-sheets' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const googleSheets: Piece<any>
}

declare module '@activepieces/piece-microsoft-outlook' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const microsoftOutlook: Piece<any>
}

declare module '@activepieces/piece-microsoft-outlook-calendar' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const microsoftOutlookCalendar: Piece<any>
}

declare module '@activepieces/piece-microsoft-onedrive' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const microsoftOneDrive: Piece<any>
}

declare module '@activepieces/piece-microsoft-teams' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const microsoftTeams: Piece<any>
}

declare module '@activepieces/piece-microsoft-sharepoint' {
  import type { Piece } from '@activepieces/pieces-framework'

  export const microsoftSharePoint: Piece<any>
}
