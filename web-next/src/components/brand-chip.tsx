/**
 * oryxa's mark in the workspace (gtm/brand/BRAND.md): the wordmark, lowercase
 * oryxa in Inter Bold with tight tracking and the gold-deep square after the
 * "a", on a small tab of the shell's own surface that rises from the bottom
 * edge of the canvas, rounded on top and square below as the canvas is.
 */
export function BrandChip() {
  return (
    <div className="workspace-brand-chip" role="img" aria-label="oryxa">
      <span className="workspace-wordmark" aria-hidden="true">
        oryxa
        <i />
      </span>
    </div>
  )
}
