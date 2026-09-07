<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { authenticate, GameClient } from './game/client'
import { savePending, loadPending, clearPending } from './game/pending'
import { replaceMailbox, unreadCount } from './game/mailbox'
import type { Command, Config, Mail, Plot, Reply, Snapshot } from './game/types'

const mode = ref<'login' | 'register'>('login')
const username = ref('')
const password = ref('')
const token = ref('')
const playerID = ref('')
const connected = ref(false)
const busy = ref(false)
const message = ref('')
const error = ref('')
const state = ref<Snapshot>()
const config = ref<Config>()
const mails = ref<Mail[]>([])
const mailboxOpen = ref(false)
const uncertain = ref<Command>()
const quantity = ref(1)
const now = ref(Date.now())
let offset = 0
const client = new GameClient()
const timer = setInterval(() => { now.value = Date.now() + offset }, 1000)
const readyToClaim = computed(() => state.value?.tasks.every(t => t.current >= t.target) ?? false)
const disabled = computed(() => busy.value || !connected.value || !!uncertain.value)
const usedSpace = computed(() => (state.value?.seeds || 0) + (state.value?.fertilizer || 0) + (state.value?.crops || 0))
const unreadMails = computed(() => unreadCount(mails.value))

function applyReply(reply: Reply) {
  if (Number.isFinite(reply.server_time_ms)) { offset = reply.server_time_ms - Date.now(); now.value = reply.server_time_ms }
  if (reply.config) config.value = reply.config
  if (reply.snapshot && (!state.value || BigInt(reply.snapshot.state_version) >= BigInt(state.value.state_version))) state.value = reply.snapshot
  // 邮箱响应是服务器的最新完整列表，直接替换本地副本。
  if (reply.mails) mails.value = replaceMailbox(reply.mails)
}
client.onReply = applyReply
client.onDisconnect = () => { connected.value = false; error.value = '连接已断开，请重新连接；登录过期时请退出后重新登录。' }
async function connect() { connected.value = false; await client.connect(token.value); connected.value = true }
async function submitAccount() {
  busy.value = true; error.value = ''; message.value = ''
  try {
    if (mode.value === 'register') await authenticate('register', username.value, password.value)
    const reply = await authenticate('login', username.value, password.value)
    if (!reply.token) throw new Error('服务器未返回登录凭证')
    token.value = reply.token; playerID.value = reply.player_id || ''; password.value = ''; state.value = undefined
    uncertain.value = loadPending(sessionStorage, playerID.value)
    await connect(); message.value = '欢迎来到你的农场'
  } catch (e) { error.value = e instanceof Error ? e.message : '登录失败' }
  finally { busy.value = false }
}
async function reconnect() {
  busy.value = true; error.value = ''
  try { await connect() } catch (e) { error.value = e instanceof Error ? e.message : '连接失败，请重新登录' }
  finally { busy.value = false }
}
async function openMailbox() {
  busy.value = true; error.value = ''
  try {
    // 每次打开都实时查询，确保已读状态来自数据库。
    const reply = await client.command('GET_MAILBOX')
    if (reply.code !== 'OK') throw new Error(reply.message || reply.code)
    mailboxOpen.value = true
  } catch (e) { error.value = e instanceof Error ? e.message : '读取邮箱失败' }
  finally { busy.value = false }
}
async function readMail(mail: Mail) {
  if (mail.is_read) return
  busy.value = true; error.value = ''
  try {
    const reply = await client.command('READ_MAIL', { mail_id: mail.mail_id })
    if (reply.code !== 'OK') throw new Error(reply.message || reply.code)
  } catch (e) { error.value = e instanceof Error ? e.message : '更新邮件失败' }
  finally { busy.value = false }
}
async function perform(action: string, data: Command['data'] = {}) {
  const command = uncertain.value || { request_id: crypto.randomUUID(), action, data }
  busy.value = true; error.value = ''; message.value = ''
  try { savePending(sessionStorage, playerID.value, command) }
  catch { busy.value = false; error.value = '浏览器无法保存请求编号，请检查存储设置后再试'; return }
  try {
    const reply = await client.request(command)
    if (reply.code === 'SERVICE_UNAVAILABLE') { uncertain.value = command; error.value = reply.message || '操作结果未确认'; return }
    uncertain.value = undefined
    clearPending(sessionStorage, playerID.value)
    if (reply.code !== 'OK') { error.value = reply.message || reply.code; return }
    message.value = '操作成功，农场已更新'
  } catch (e) { uncertain.value = command; error.value = e instanceof Error ? e.message : '操作结果未确认' }
  finally { busy.value = false }
}
async function logout() {
  busy.value = true; error.value = ''
  try {
    const response = await fetch('/api/logout', { method: 'POST', headers: { Authorization: 'Bearer ' + token.value }, signal: AbortSignal.timeout(8000) })
    if (!response.ok && response.status !== 401) throw new Error('退出登录失败，请重试')
    client.close(); token.value = ''; state.value = undefined; connected.value = false; uncertain.value = undefined; message.value = ''; mails.value = []; mailboxOpen.value = false
  } catch (e) { error.value = e instanceof Error ? e.message : '退出失败' }
  finally { busy.value = false }
}
function remaining(plot: Plot) { return Math.max(0, Math.ceil((plot.mature_at_ms - now.value) / 1000)) }
function mature(plot: Plot) { return (plot.status === 'GROWING' || plot.status === 'MATURE') && remaining(plot) === 0 }
onBeforeUnmount(() => { clearInterval(timer); client.close() })
</script>

<template>
  <main class="shell">
    <header class="header">
      <div><p class="eyebrow">CLASSIC FARM · 中期课设</p><h1>我的小农场 🌱</h1></div>
      <div v-if="token" class="account"><span>{{ username }} · {{ connected ? '在线' : '离线' }}</span><button :disabled="busy || !connected" @click="openMailbox">邮箱<span v-if="unreadMails">（{{ unreadMails }}）</span></button><button v-if="!connected" :disabled="busy" @click="reconnect">重新连接</button><button class="secondary" :disabled="busy" @click="logout">退出登录</button></div>
    </header>
    <p v-if="error" role="alert" class="notice error">{{ error }}</p>
    <p v-if="message" role="status" class="notice success">{{ message }}</p>
    <section v-if="!token" class="card login-card">
      <h2>{{ mode === 'login' ? '回到你的农场' : '开启农场生活' }}</h2>
      <p class="muted">种下胡萝卜，照料成长，收获属于你的成果。</p>
      <form @submit.prevent="submitAccount">
        <label>账号<input v-model="username" name="username" autocomplete="username" required pattern="[a-z][a-z0-9_]{2,31}" maxlength="32" placeholder="例如 student_a" /></label>
        <small>3–32 位小写字母、数字或下划线，以字母开头。</small>
        <label>密码<input v-model="password" name="password" type="password" :autocomplete="mode === 'login' ? 'current-password' : 'new-password'" required minlength="8" maxlength="128" placeholder="8–128 字节，建议使用字母和数字" /></label>
        <button type="submit" :disabled="busy">{{ busy ? '正在连接…' : mode === 'login' ? '登录农场' : '注册并进入' }}</button>
      </form>
      <button class="text-button" :disabled="busy" @click="mode = mode === 'login' ? 'register' : 'login'">{{ mode === 'login' ? '没有账号？创建一个' : '已有账号？返回登录' }}</button>
    </section>
    <section v-else-if="!state" class="card"><h2>正在连接农场</h2><p>连接失败时，请点击右上角重新连接，或退出后重新登录。</p></section>
    <template v-else>
      <section class="stats" aria-label="背包概况"><div><span>金币</span><strong>🪙 {{ state.coins }}</strong></div><div><span>胡萝卜种子</span><strong>🌱 {{ state.seeds }}</strong></div><div><span>肥料</span><strong>🧴 {{ state.fertilizer }}</strong></div><div><span>胡萝卜</span><strong>🥕 {{ state.crops }}</strong></div></section>
      <section v-if="mailboxOpen" class="card mailbox" aria-label="邮箱">
        <div class="section-title"><h2>邮箱</h2><button class="secondary" @click="mailboxOpen = false">关闭</button></div>
        <p v-if="mails.length === 0" class="muted">暂无邮件</p>
        <article v-for="mail in mails" :key="mail.mail_id" class="mail" :class="{ unread: !mail.is_read }" @click="readMail(mail)">
          <div class="mail-heading"><h3>{{ mail.title }}</h3><span>{{ mail.is_read ? '已读' : '未读' }}</span></div>
          <time :datetime="new Date(mail.created_at_ms).toISOString()">{{ new Date(mail.created_at_ms).toLocaleString() }}</time>
          <p>{{ mail.content }}</p>
        </article>
      </section>
      <div v-if="uncertain" class="notice warning"><p>上次操作结果尚未确认。请先确认原操作，再进行其他操作。</p><button :disabled="busy || !connected" @click="perform(uncertain.action)">重试并确认原操作</button></div>
      <div class="dashboard">
        <section class="card farm"><div class="section-title"><h2>我的农田</h2><span class="muted">{{ state.plots.length }} 块地</span></div>
          <div class="plots"><article v-for="plot in state.plots" :key="plot.plot_id" class="plot" :class="{ ripe: mature(plot) }">
            <span class="plot-number">{{ plot.plot_id }} 号地</span>
            <div class="crop-art" aria-hidden="true">{{ plot.status === 'EMPTY' ? '🟫' : plot.status === 'NEED_CLEANUP' ? '🍂' : mature(plot) ? '🥕' : '🌱' }}</div>
            <h3>{{ plot.status === 'EMPTY' ? '等待播种' : plot.status === 'NEED_CLEANUP' ? '等待清理' : mature(plot) ? '胡萝卜成熟了' : '胡萝卜生长中' }}</h3>
            <p>{{ plot.status === 'GROWING' && !mature(plot) ? '预计 ' + remaining(plot) + ' 秒后成熟' : plot.status === 'EMPTY' ? '一颗种子，一份期待' : mature(plot) ? '可收获 ' + (config?.yield ?? 3) + ' 个胡萝卜' : '清理后可以再次种植' }}</p>
            <button v-if="plot.status === 'EMPTY'" :disabled="disabled || !state.seeds" @click="perform('PLANT', { plot_id: plot.plot_id })">种植胡萝卜</button>
            <button v-else-if="plot.status === 'NEED_CLEANUP'" :disabled="disabled" @click="perform('CLEAN_PLOT', { plot_id: plot.plot_id })">清理地块</button>
            <button v-else-if="mature(plot)" :disabled="disabled" @click="perform('HARVEST', { plot_id: plot.plot_id })">收获</button>
            <button v-else :disabled="disabled || plot.fertilized || !state.fertilizer" @click="perform('APPLY_FERTILIZER', { plot_id: plot.plot_id })">{{ plot.fertilized ? '已施肥' : '施肥加速' }}</button>
          </article></div>
        </section>
        <aside class="sidebar">
          <section class="card"><h2>商店与仓库</h2><label>数量<input v-model.number="quantity" type="number" min="1" max="100" step="1" /></label>
            <div class="shop-actions"><button :disabled="disabled || !Number.isInteger(quantity) || quantity < 1 || quantity > 100" @click="perform('BUY_SEEDS', { quantity })">买胡萝卜种子 · {{ config?.seed_price ?? 2 }} 金币/颗</button><button class="secondary" :disabled="disabled || !Number.isInteger(quantity) || quantity < 1 || quantity > 100" @click="perform('BUY_FERTILIZER', { quantity })">买肥料 · {{ config?.fertilizer_price ?? 2 }} 金币/份</button><button class="secondary" :disabled="disabled || !state.crops" @click="perform('SELL_CROP', { quantity: state.crops })">出售全部胡萝卜 · {{ config?.crop_price ?? 5 }} 金币/个</button></div>
            <p class="muted">仓库 {{ usedSpace }} / {{ config?.capacity ?? 200 }}。肥料缩短 {{ config?.fertilizer_seconds ?? 30 }} 秒，每株限用一次。</p>
          </section>
          <section class="card"><h2>第 {{ state.chapter }} 章 · 农场日常</h2><ul class="tasks"><li v-for="task in state.tasks" :key="task.action"><span>{{ task.current >= task.target ? '✓' : '○' }} {{ task.label }}</span><span>{{ task.current }}/{{ task.target }}</span></li></ul><p class="muted">奖励：10 金币 + 3 颗种子</p><button :disabled="disabled || !readyToClaim" @click="perform('CLAIM_CHAPTER_REWARD')">领取奖励，进入下一章</button></section>
        </aside>
      </div>
    </template>
    <footer>种植 · 照料 · 收获</footer>
  </main>
</template>
