import type { Command } from './types'

// Keep only the unresolved command, never a password or login token.
// Session storage survives a refresh or re-login in this tab.
export function savePending(storage: Storage, playerID: string, command: Command): void {
  storage.setItem(`farm-pending:${playerID}`, JSON.stringify(command))
}
export function loadPending(storage: Storage, playerID: string): Command | undefined {
  const raw = storage.getItem(`farm-pending:${playerID}`)
  if (!raw) return undefined
  try {
    const command = JSON.parse(raw) as Command
    if (typeof command.request_id === 'string' && typeof command.action === 'string' && command.data && !command.data.token) return command
  } catch { /* Ignore malformed local data. */ }
  return undefined
}
export function clearPending(storage: Storage, playerID: string): void { storage.removeItem(`farm-pending:${playerID}`) }
