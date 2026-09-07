import type { Mail } from './types'

export function unreadCount(mails: Mail[]): number {
  return mails.filter(mail => !mail.is_read).length
}

export function replaceMailbox(returned: Mail[]): Mail[] {
  return returned.map(mail => ({ ...mail }))
}
