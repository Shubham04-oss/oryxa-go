import { readFile, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const verifyOnly = process.argv.includes('--check')
const pieces = [
  'piece-slack',
  'piece-github',
  'piece-gmail',
  'piece-google-calendar',
  'piece-google-drive',
  'piece-microsoft-outlook',
  'piece-microsoft-outlook-calendar',
  'piece-microsoft-onedrive',
]
const insecure = /process\.env\.NODE_TLS_REJECT_UNAUTHORIZED\s*=\s*(["'])0\1/g
const hardened = 'process.env.NODE_TLS_REJECT_UNAUTHORIZED="1"'
// Activepieces' bundled HTTP client used by the reviewed pieces
// logs an Error carrying both request and response bodies before rethrowing.
// Those bodies can include OAuth bearer tokens or user content. The runtime
// does not use a package logger, so remove only this upstream error side effect
// while preserving the thrown error for the handler's opaque failure result.
const unsafeHttpErrorLog = /throw console\.error\("\[HttpClient#\(sanitized error message\)\] Request failed:",\s*([A-Za-z_$][\w$]*)\),\1/g
// Microsoft Graph SDK debug mode logs full request URLs. They can include
// action-supplied query parameters; it is not an approved runtime capability.
const unsafeDebugURLLog = /this\.config\.debugLogging&&console\.log\([A-Za-z_$][\w$]*\),/g

for (const piece of pieces) {
  const path = join(root, 'node_modules', '@activepieces', piece, 'src', 'index.js')
  const source = await readFile(path, 'utf8')
  const needsTLSHardening = insecure.test(source)
  insecure.lastIndex = 0
  const needsErrorLogHardening = unsafeHttpErrorLog.test(source)
  unsafeHttpErrorLog.lastIndex = 0
  const needsDebugLogHardening = unsafeDebugURLLog.test(source)
  unsafeDebugURLLog.lastIndex = 0
  if (!verifyOnly && (needsTLSHardening || needsErrorLogHardening || needsDebugLogHardening)) {
    await writeFile(path, source
      .replaceAll(insecure, hardened)
      .replaceAll(unsafeHttpErrorLog, 'throw $1')
      .replaceAll(unsafeDebugURLLog, ''))
  }
  insecure.lastIndex = 0
  unsafeHttpErrorLog.lastIndex = 0
  unsafeDebugURLLog.lastIndex = 0
  const installed = await readFile(path, 'utf8')
  if (insecure.test(installed) || !installed.includes(hardened) ||
    unsafeHttpErrorLog.test(installed) || unsafeDebugURLLog.test(installed)) {
    throw new Error(`${piece} did not pass dependency hardening checks`)
  }
  insecure.lastIndex = 0
  unsafeHttpErrorLog.lastIndex = 0
  unsafeDebugURLLog.lastIndex = 0
}
