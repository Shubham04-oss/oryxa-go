# Design reference for the web-next redesign (15 Oct 2026)

This is a reference branch only: never merge it. It is a snapshot of the founder's
"oryxa workspace redesign" canvas, kept so that cloud agents can read the boards.

- `boards/*.dc.html`: artboards. Each `X.dc.html` has an `XLight.dc.html` twin.
  Boards link stylesheets as `/_blob/<id>`, which is `css/<id>.css` here.
  (The compiled web-next stylesheet is left out; it is web-next itself.)
- `INDEX.md`: board titles, and what each stylesheet covers.
- `audit.json`: a gap audit of each area against web-next. For each area it
  lists `gaps[]` and the PR `slices[]` to close them, with the files each owns
  and what to stub.

## Founder decisions that override the boards

1. **Build with the UI library.** Use shadcn "base-nova" (Base UI) from
   `web-next/src/components/ui`, and add missing pieces with
   `npx shadcn add <name>` from web-next. Never copy the boards' HTML or
   class names. The boards show layout and content only.
2. **My workspace** is `WorkspaceV3.dc.html`:
   - one centred column, with the composer in the middle;
   - symmetric and decluttered;
   - both sidebars open as rails (Krude's sidebar collapsed);
   - top to bottom: a live parley pill, the greeting, a large composer, then
     chips, then two equal tiles (Needs you, and While you were away), then
     four equal "Pick up" cards.

   The first run is `OnbFirstRunV3.dc.html`, on the same axis. Ignore the
   older WorkspaceNew and OnbFirstRun.
3. **Onboarding** is `OnbV3s1`–`OnbV3s5` plus `Onb3Model`:
   - the scene on the left, with a miniature of the workspace that builds up
     one part per step;
   - the form on the right, with a step count and a five-part progress bar.
4. **Agent chat versus people chat:** people chats may be a plain list. The
   agent chat must clearly differ: it's a working session (runs, tool steps,
   sources, files, approvals), not a messenger thread. When a file is open
   beside an agent chat, the chat is on the LEFT and the file on the RIGHT,
   everywhere.
5. **Wording:** live calls are "parleys" in copy. Code may keep the
   huddle/hd names.
6. **No fake data.** Where the API is missing, hide the element or show an
   honest one-line state. The backend asks are in discussion #322.
