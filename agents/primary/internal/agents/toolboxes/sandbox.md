# Sandbox

Runs code, shell commands and temporary files in an isolated sandbox. Its
files and language context live for this conversation only, never on the
service host.

This is a scratch machine for *executing* things — not a way to reach the
member's own data. `sandbox_read_files` and the rest only ever see files you
wrote in this sandbox yourself. They are never the member's repository or
their real files: to read, search or change those, use apps instead.

- Use it for computation, data work and file transformations. Prefer it over
  mental arithmetic or pretending to run code.
- Use `sandbox_execute_command` for packages and file transformations; it
  cannot reach the primary service host.
- Write results to files when a command produces a lot, then read what you
  need with `sandbox_read_files`.
- To keep a file past this conversation — a document, an image, anything not
  small text — use `sandbox_save_artifact`, not `sandbox_read_files` followed
  by `save_artifact`. It moves the bytes directly into the artifact store;
  the other way asks you to retype arbitrarily long base64 yourself, which
  breaks on anything real and has no way to tell you it broke.
- `sandbox_remove_files` and `sandbox_reset` clear files and the language
  context for good. They need the member's confirmation; say what you are
  about to clear and why.
- Tool results are bounded before they reach you. Ask for less, or write to a
  file and read selectively.
