# Sandbox

Runs code, shell commands and temporary files in an isolated AWS AgentCore
sandbox. Its files and language context live for this conversation only,
never on the service host.

- Use it for computation, data work and file transformations. Prefer it over
  mental arithmetic or pretending to run code.
- Use `sandbox_execute_command` for packages and file transformations; it
  cannot reach the primary service host.
- Write results to files when a command produces a lot, then read what you
  need with `sandbox_read_files`.
- `sandbox_remove_files` and `sandbox_reset` clear files and the language
  context for good. They need the member's confirmation; say what you are
  about to clear and why.
- Tool results are bounded before they reach you. Ask for less, or write to a
  file and read selectively.
