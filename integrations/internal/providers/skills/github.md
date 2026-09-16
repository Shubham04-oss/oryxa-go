# GitHub

A subscription watches one repository, named `owner/name`, and asks for
GitHub's own event names: `issues`, `issue_comment`, `pull_request`, `push`,
`release`. A subscription that asks for none receives every event the
repository sends.

## Events

Each delivery is named for the event and its action: `github.issues.opened`,
`github.pull_request.closed`, `github.push`. The trigger's resource names what
it is about — an issue, a pull request, the push — and the actor is the GitHub
user who did it.

An issue and a pull request both have a **number** (`#12`, what people say)
and an **id** (a large integer GitHub uses internally). Every tool here takes
the number. A pull request is also an issue: comment on one with
`github_comment_on_issue` using its number.

## Tools

Say what you are about to do before you write anything, and prefer one precise
comment to several. When an action fails with `needs_reconnect`, the member's
GitHub connection has to be reconnected before anything will work — say so
rather than trying again. `scope_required` means the connection was not
granted what this action needs; name the scope in the message.

Do not paste a whole issue body back to the member; link to it with the `url`
the result carries.
