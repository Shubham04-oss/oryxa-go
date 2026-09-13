import { execute, RunnerError } from './runner.js'
import type { RunnerRequest } from './protocol.js'

export async function handler(request: RunnerRequest): Promise<unknown> {
  try {
    return { ok: true, result: await execute(request) }
  } catch (error) {
    if (error instanceof RunnerError) {
      return { ok: false, error: { code: error.code, message: error.message } }
    }
    return { ok: false, error: { code: 'piece_failed', message: safeMessage(error) } }
  }
}

function safeMessage(error: unknown): string {
  if (!(error instanceof Error)) return 'piece execution failed'
  // Connector errors may contain query strings or credentials. The Go broker
  // logs the category and invocation ID; this message is returned only to it.
  const withoutURLs = error.message.replace(/https?:\/\/\S+/gi, '[redacted-url]')
  return withoutURLs.slice(0, 500)
}
