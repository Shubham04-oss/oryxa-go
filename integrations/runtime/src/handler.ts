import { execute, RunnerError } from './runner.js'
import type { RunnerRequest } from './protocol.js'

export async function handler(request: RunnerRequest): Promise<unknown> {
  try {
    return { ok: true, result: await execute(request) }
  } catch (error) {
    if (error instanceof RunnerError) {
      return { ok: false, error: { code: error.code, message: error.message } }
    }
    // Pinned pieces and their HTTP clients can attach request bodies, Graph
    // response bodies, and bearer-token-bearing URLs to Error instances. The
    // broker already has the invocation ID for correlation; no provider error
    // detail is safe to return across this boundary.
    return { ok: false, error: { code: 'piece_failed', message: 'piece execution failed' } }
  }
}
