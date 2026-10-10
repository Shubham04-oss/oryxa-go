# Working beside other agents

Three agents work on this repository at once: **macbookpro**, **a1** and
**macmini**. They share nothing but GitHub, so GitHub is how they talk. Read
`docs/STATUS.md` and `docs/LANES.md` for what is live and who owns which
files; this page says how to stay in sync.

Every agent posts as the same GitHub account, so **start every post, comment
and PR description with `[agent:<name>]`**. Nobody is notified of anything:
you see what others wrote only when you look.

## When to look

- **At the start of a session:** the board, open PRs and new discussions.
- **Before touching a file** another item on the board lists under *Owns
  files*, or a hot file in `docs/LANES.md`.
- **Before opening a PR**, and after one merges.

```sh
gh project item-list 2 --owner Shubham04-oss --format json \
  -q '.items[] | "#\(.content.number) \(.status) \(.agent // "-") | \(."owns files" // "")"'
gh pr list
gh api graphql -f query='{repository(owner:"Shubham04-oss",name:"oryxa-go"){discussions(first:10,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{number title updatedAt category{name} comments{totalCount}}}}}' \
  -q '.data.repository.discussions.nodes[] | "#\(.number) [\(.category.name)] \(.title) (\(.comments.totalCount) comments, \(.updatedAt))"'
```

## The board: who is doing what

[Oryxa Go: agents](https://github.com/users/Shubham04-oss/projects/2), project
2. One item per task or PR.

- **Claim before you write code:** add the PR (or a draft item) and set
  **Agent** to yourself and **Status** to In Progress.
- **Owns files:** the paths the work changes. If a path you need is under
  someone else's item, ask in Discussions first; don't edit it.
- A merged PR moves to Done; check that it did.

```sh
gh project item-add 2 --owner Shubham04-oss --url https://github.com/Shubham04-oss/oryxa-go/pull/<n>
# then set Agent, Status and Owns files with gh project item-edit
# (gh project field-list 2 --owner Shubham04-oss --format json gives the ids)
```

## Discussions: talking to each other

| Category | For | Id |
|---|---|---|
| Announcements | "I'm changing X; keep out until PR #N" | `DIC_kwDOUYyqNs4DHeg-` |
| Q&A | A question for another agent; mark the answer | `DIC_kwDOUYyqNs4DHehA` |
| General | Handoffs, blockers and decisions, titled `[handoff]`, `[blocker]` or `[decision]` | `DIC_kwDOUYyqNs4DHeg_` |

`gh` has no discussions command; use the API. The repository id is
`R_kgDOUYyqNg`.

```sh
# Read one discussion and its replies
gh api graphql -f query='{repository(owner:"Shubham04-oss",name:"oryxa-go"){discussion(number:<n>){title body comments(first:50){nodes{id createdAt body replies(first:20){nodes{createdAt body}}}}}}}'

# Start one
gh api graphql -f query='mutation($c:ID!,$t:String!,$b:String!){createDiscussion(input:{repositoryId:"R_kgDOUYyqNg",categoryId:$c,title:$t,body:$b}){discussion{url}}}' \
  -f c=<category id> -f t='<title>' -f b='[agent:<name>] <body>'

# Reply (the discussion's node id is `id` on discussion(number:<n>))
gh api graphql -f query='mutation($d:ID!,$b:String!){addDiscussionComment(input:{discussionId:$d,body:$b}){comment{url}}}' \
  -f d=<discussion node id> -f b='[agent:<name>] <reply>'
```

Asking does not mean waiting: post the question, carry on with other work,
and look again at the next checkpoint.

## Rules that still hold

- One task, one branch, one PR against main. Never merge or deploy; the
  owner does.
- Stay inside your item's files. Commit after each step.
- What you changed, what you found and what's left goes in the PR and on the
  board, not only in your own session, so the next agent can see it.
