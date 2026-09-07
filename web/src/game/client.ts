import type { Command, Reply } from './types'

export async function authenticate(mode: 'login' | 'register', username: string, password: string): Promise<Reply> {
  const response = await fetch(`/api/${mode}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }), signal: AbortSignal.timeout(15_000) })
  const result: Reply = await response.json()
  if (!response.ok || result.code !== 'OK') throw new Error(result.message || '请求失败，请稍后重试')
  return result
}
type Pending = { resolve: (r: Reply) => void; reject: (e: Error) => void; timer: ReturnType<typeof setTimeout> }

export class GameClient {
  private socket?: WebSocket
  private pending = new Map<string, Pending>()
  private heartbeat?: ReturnType<typeof setInterval>
  onReply?: (reply: Reply) => void
  onDisconnect?: () => void

  async connect(token: string): Promise<void> {
    this.close()
    const socket = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/ws`)
    this.socket = socket
    socket.onmessage = (event) => {
      if (this.socket !== socket) return
      let reply: Reply
      try { reply = JSON.parse(event.data) as Reply } catch { socket.close(); return }
      if (reply.type !== 'response' || typeof reply.code !== 'string') { socket.close(); return }
      this.onReply?.(reply)
      const item = this.pending.get(reply.request_id || '')
      if (item) { clearTimeout(item.timer); this.pending.delete(reply.request_id!); item.resolve(reply) }
    }
    socket.onclose = () => { if (this.socket === socket) { this.cleanup(); this.onDisconnect?.() } }
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => { reject(new Error('连接超时')); socket.close() }, 8000)
      socket.addEventListener('open', () => { clearTimeout(timer); resolve() }, { once: true })
      socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('无法连接游戏服务器')); socket.close() }, { once: true })
      socket.addEventListener('close', () => { clearTimeout(timer); reject(new Error('连接已关闭')) }, { once: true })
    })
    const auth = await this.request({ request_id: crypto.randomUUID(), action: 'AUTH', data: { token } })
    if (auth.code !== 'OK') { this.close(); throw new Error(auth.message || '登录已过期') }
    const initial = await this.command('GET_PLAYER_SNAPSHOT')
    if (initial.code !== 'OK') { this.close(); throw new Error(initial.message || '读取农场失败') }
    this.heartbeat = setInterval(() => { void this.command('PING').catch(() => this.socket?.close()) }, 20_000)
  }
  command(action: string, data: Command['data'] = {}): Promise<Reply> { return this.request({ request_id: crypto.randomUUID(), action, data }) }
  request(command: Command): Promise<Reply> {
    const socket = this.socket
    if (!socket || socket.readyState !== WebSocket.OPEN) return Promise.reject(new Error('连接已断开，请重新连接'))
    if (this.pending.has(command.request_id)) return Promise.reject(new Error('请求仍在处理中'))
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(command.request_id); reject(new Error('请求超时，结果尚未确认')) }, 10_000)
      this.pending.set(command.request_id, { resolve, reject, timer })
      try { socket.send(JSON.stringify(command)) } catch { clearTimeout(timer); this.pending.delete(command.request_id); reject(new Error('发送失败，结果尚未确认')) }
    })
  }
  private cleanup(): void {
    clearInterval(this.heartbeat)
    for (const item of this.pending.values()) { clearTimeout(item.timer); item.reject(new Error('连接断开，结果尚未确认')) }
    this.pending.clear()
  }
  close(): void { const old = this.socket; this.socket = undefined; old?.close(); this.cleanup() }
}
