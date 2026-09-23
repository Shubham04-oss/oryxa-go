# Frontend design contract

This file applies to all work in `web-next/`. Before changing a route, component,
style, copy, or asset that affects the UI, read [DESIGN.md](DESIGN.md) in full.
Treat it as the design source of truth for this app. The repository-level
`docs/design/` folder describes an older redesign plan and does not override
this contract.

## Required workflow for UI changes

1. Identify which rules in `DESIGN.md` apply before editing. Reuse the existing
   shell, tokens, components, icon language, and responsive behavior.
2. Preserve the interaction invariants in the document, especially sidebar
   state, dynamic headers, mobile keyboard behavior, roles, and real versus
   preview data. Do not make a visual change that silently changes behavior.
3. Check the changed view at a desktop width and a mobile width. Inspect both
   expanded and collapsed panel states when the shell is involved.
4. Run `npm run typecheck`, `npm test`, and `npm run build` from `web-next/` for
   frontend changes. Report failures; do not claim checks passed if they did not.
5. If an explicit user request changes a design rule, update `DESIGN.md` in the
   same task. Do not weaken the document merely to excuse an accidental drift.

When existing code conflicts with the contract, follow the contract for new
work and fix the conflict if it is in scope. If it is outside scope, avoid
copying the inconsistency and mention it rather than silently treating it as
the pattern.
