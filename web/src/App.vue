<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type {
  ClientConfigPackage,
  GatewayEndpoint,
  SessionView,
} from './gen/classicfarm/v1/http/http_pb'
import { HttpErrorCode } from './gen/classicfarm/v1/http/http_pb'
import type {
  CropCatalogEntryView,
  PlayerSnapshot,
  PlayerStatePatch,
  ShopEntryView,
  StateVersion,
  WsEnvelope,
  Error as WsError,
  FriendView,
  FarmVisitSnapshot,
  PublicPlotView,
} from './gen/classicfarm/v1/ws/ws_pb'
import { ErrorCode } from './gen/classicfarm/v1/ws/ws_pb'
import { PlotState } from './gen/classicfarm/v1/ws/plot/plot_state_pb'
import {
  ProtobufHttpError,
  authenticate,
  downloadClientConfig,
  fetchBootstrap,
  fetchCsrf,
  fetchSession,
  issueWsTicket,
  logout,
  selectGateway,
} from './lib/http'
import { bytesEqual } from './lib/hash'
import { FarmWebSocket } from './lib/ws'
import FarmDashboard from './components/FarmDashboard.vue'
import FriendsPanel from './components/FriendsPanel.vue'
import FriendFarmDashboard from './components/FriendFarmDashboard.vue'
import GameDrawer from './components/GameDrawer.vue'
import TopNav from './components/TopNav.vue'
import AccountPanel from './components/AccountPanel.vue'
import InventoryPanel from './components/InventoryPanel.vue'
import ShopPanel from './components/ShopPanel.vue'
import TaskPanel from './components/TaskPanel.vue'
import type { FarmAction, FarmActionRequest } from './lib/farm-actions'
import { panelKickers, panelTitles, type PanelId } from './lib/panels'
import { isNewerStateVersion, mergePublicPlotUpserts } from './lib/friend-farm'

type FriendVisitState = {
  owner: FriendView
  visitId: Uint8Array
  expiresAtMs: bigint
  snapshot: FarmVisitSnapshot
  ownerStateVersion: StateVersion
}

type StealRetry = {
  requestId: string
  ownerPlayerId: bigint
  plotId: number
  cropItemId: number
  plantedAtMs: bigint
  quantity: number
}

type FriendPlotAction = 'pest' | 'catch' | 'steal'

type Phase =
  | 'idle'
  | 'csrf'
  | 'session'
  | 'bootstrap'
  | 'config'
  | 'ticket'
  | 'socket'
  | 'auth'
  | 'snapshot'
  | 'ready'
  | 'disconnected'
  | 'failed'

const phaseLabels: Record<Phase, string> = {
  idle: '等待账号操作',
  csrf: '获取 CSRF',
  session: '建立 HTTP Session',
  bootstrap: '读取 bootstrap',
  config: '校验客户端配置',
  ticket: '签发一次性 Ticket',
  socket: '连接 Gateway',
  auth: 'WebSocket AUTH',
  snapshot: '请求玩家快照',
  ready: '快照链路完成',
  disconnected: '已断开',
  failed: '链路失败',
}

const steps: Phase[] = [
  'csrf',
  'session',
  'bootstrap',
  'config',
  'ticket',
  'socket',
  'auth',
  'snapshot',
  'ready',
]

const accountName = ref('')
const password = ref('')
const phase = ref<Phase>('idle')
const busy = ref(false)
const errorMessage = ref('')
const csrfToken = ref('')
const session = ref<SessionView>()
const gateway = ref<GatewayEndpoint>()
const clientConfig = ref<ClientConfigPackage>()
const authRequestId = ref('')
const snapshotRequestId = ref('')
const snapshot = ref<PlayerSnapshot>()
const stateVersion = ref<StateVersion>()
const serverTimeMs = ref<bigint>()
const wsError = ref<WsError>()
const socket = new FarmWebSocket()
const pushCount = ref(0)
const gapRecoveryCount = ref(0)
const shopEntries = ref<ShopEntryView[]>([])
const cropCatalog = ref<CropCatalogEntryView[]>([])
const busyAction = ref<FarmActionRequest>()
const actionMessage = ref('')
const actionError = ref('')
const lastActionRequestId = ref('')
const nowMs = ref(BigInt(Date.now()))
const friends = ref<FriendView[]>([])
const friendBusy = ref(false)
const friendRefreshBusy = ref(false)
const friendRefreshError = ref('')
const activePanel = ref<PanelId | null>(null)
const generatedFriendCode = ref('')
const friendMessage = ref('')
const friendError = ref('')
const friendVisit = ref<FriendVisitState>()
const visitBusy = ref(false)
const stealBusyPlotId = ref<number>()
const visitBusyAction = ref<FriendPlotAction>()
const visitMessage = ref('')
const visitError = ref('')
let stealRetry: StealRetry | undefined
let serverClockOffsetMs = 0n
let clockTimer: ReturnType<typeof setInterval> | undefined
let visitHeartbeatTimer: ReturnType<typeof setInterval> | undefined
let gapRecovery: Promise<void> | undefined
const TAB_SESSION_STORAGE_KEY = 'classic-farm.active-session-player-id'

socket.setPlayerStateChangedHandler(handlePlayerStateChanged)
socket.setFriendFarmChangedHandler(handleFriendFarmChanged)
socket.setDisconnectHandler(handleSocketDisconnect)

const canConnect = computed(() => Boolean(session.value) && !busy.value)
const phaseIndex = computed(() => steps.indexOf(phase.value))
const signedIn = computed(() => Boolean(session.value && snapshot.value))
const inventoryMap = computed(() => {
  const quantities = new Map<number, number>()
  for (const item of snapshot.value?.inventory ?? []) quantities.set(item.itemId, item.quantity)
  return quantities
})
const timelineSteps = computed(() =>
  steps.map((step) => ({ label: phaseLabels[step], state: stepState(step) })),
)
const diagnosticFacts = computed(() => [
  { label: 'Gateway', value: gateway.value?.gatewayId ?? '—' },
  { label: 'AUTH request_id', value: authRequestId.value || '—' },
  { label: 'Snapshot request_id', value: snapshotRequestId.value || '—' },
  { label: 'Last action request_id', value: lastActionRequestId.value || '—' },
  {
    label: 'state_version',
    value: stateVersion.value
      ? `${stateVersion.value.ownerEpoch.toString()} / ${stateVersion.value.playerSeq.toString()}`
      : '—',
  },
  { label: 'server_time_ms', value: serverTimeMs.value?.toString() ?? '—' },
  { label: 'error', value: wsError.value ? String(wsError.value.code) : '—' },
  {
    label: 'config_version',
    value: clientConfig.value?.clientConfigVersion.toString() ?? '—',
  },
  { label: 'Push 数量', value: String(pushCount.value) },
  { label: '缺口快照恢复', value: String(gapRecoveryCount.value) },
])

function stepState(step: Phase): 'done' | 'active' | 'waiting' {
  const index = steps.indexOf(step)
  if (phase.value === 'ready' || index < phaseIndex.value) {
    return 'done'
  }
  if (step === phase.value) {
    return 'active'
  }
  return 'waiting'
}

function clearResult(): void {
  clearFriendVisit()
  errorMessage.value = ''
  authRequestId.value = ''
  snapshotRequestId.value = ''
  snapshot.value = undefined
  stateVersion.value = undefined
  serverTimeMs.value = undefined
  wsError.value = undefined
  pushCount.value = 0
  gapRecoveryCount.value = 0
  shopEntries.value = []
  cropCatalog.value = []
  busyAction.value = undefined
  actionMessage.value = ''
  actionError.value = ''
  lastActionRequestId.value = ''
  friends.value = []
  friendRefreshBusy.value = false
  friendRefreshError.value = ''
  activePanel.value = null
  generatedFriendCode.value = ''
  friendMessage.value = ''
  friendError.value = ''
}

function applyPatch(current: PlayerSnapshot, patch: PlayerStatePatch): PlayerSnapshot {
  const removedItems = new Set(patch.inventoryRemovedItemIds)
  const inventory = new Map(
    current.inventory
      .filter((item) => !removedItems.has(item.itemId))
      .map((item) => [item.itemId, item]),
  )
  for (const item of patch.inventoryUpserts) {
    inventory.set(item.itemId, item)
  }
  const plots = new Map(current.plots.map((plot) => [plot.plotId, plot]))
  for (const plot of patch.plotUpserts) {
    plots.set(plot.plotId, plot)
  }
  return {
    ...current,
    coinBalance: patch.coinBalance ?? current.coinBalance,
    inventory: [...inventory.values()].sort((left, right) => left.itemId - right.itemId),
    plots: [...plots.values()].sort((left, right) => left.plotId - right.plotId),
    currentChapter: patch.currentChapter ?? current.currentChapter,
  }
}

function setServerClock(serverMs: bigint): void {
  if (serverMs <= 0n) {
    return
  }
  serverClockOffsetMs = serverMs - BigInt(Date.now())
  nowMs.value = serverMs
}

async function refreshShop(): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected) {
    return
  }
  const response = await socket.requestShop(playerId)
  setServerClock(response.serverTimeMs)
  if (response.error) {
    throw new Error(`商店请求失败：${describeWsError(response.error)}`)
  }
  if (response.payload.case !== 'getShopResponse') {
    throw new Error('商店响应 payload 无效')
  }
  shopEntries.value = response.payload.value.entries
  cropCatalog.value = response.payload.value.crops
}

function describeWsError(error: WsError): string {
  const labels: Partial<Record<ErrorCode, string>> = {
    [ErrorCode.INVALID_ARGUMENT]: '请求参数无效',
    [ErrorCode.REQUEST_ID_CONFLICT]: '请求编号冲突',
    [ErrorCode.CONFIG_UNAVAILABLE]: '游戏配置暂不可用',
    [ErrorCode.SHOP_ENTRY_NOT_FOUND]: '商品不存在',
    [ErrorCode.SHOP_ENTRY_DISABLED]: '商品已下架',
    [ErrorCode.PRICE_CHANGED]: '价格已经变化，请刷新商店',
    [ErrorCode.INSUFFICIENT_COINS]: '金币不足',
    [ErrorCode.INVENTORY_TYPE_LIMIT]: '仓库种类已满',
    [ErrorCode.INVENTORY_STACK_LIMIT]: '该物品堆叠已满',
    [ErrorCode.ITEM_NOT_OWNED]: '未拥有该物品',
    [ErrorCode.INSUFFICIENT_ITEM_QUANTITY]: '物品数量不足',
    [ErrorCode.PLOT_NOT_FOUND]: '地块不存在',
    [ErrorCode.PLOT_STATE_CONFLICT]: '当前地块状态不能执行此操作',
    [ErrorCode.FERTILIZER_ALREADY_ACTIVE]: '肥料效果仍在生效',
    [ErrorCode.CROP_NOT_MATURE]: '作物尚未成熟',
    [ErrorCode.CHAPTER_NOT_CLAIMABLE]: '章节任务尚未完成',
    [ErrorCode.CHAPTER_REWARD_ALREADY_CLAIMED]: '章节奖励已经领取',
    [ErrorCode.FRIEND_CODE_NOT_FOUND]: '好友码不存在',
    [ErrorCode.FRIEND_CODE_EXPIRED]: '好友码已过期',
    [ErrorCode.CANNOT_FRIEND_SELF]: '不能添加自己为好友',
    [ErrorCode.FRIEND_LIMIT_REACHED]: '好友数量已达上限',
    [ErrorCode.NOT_MUTUAL_FRIEND]: '对方不是你的双向好友',
    [ErrorCode.VISIT_NOT_FOUND]: '本次农场访问已失效',
    [ErrorCode.VISIT_EXPIRED]: '本次农场访问已过期',
    [ErrorCode.STEAL_NOT_AVAILABLE]: '这块地现在不能偷取',
    [ErrorCode.PEST_ALREADY_ACTIVE]: '这块地已经有害虫',
    [ErrorCode.PEST_NOT_ACTIVE]: '这块地没有害虫',
    [ErrorCode.PEST_SOURCE_FORBIDDEN]: '害虫来源限制，当前不能帮忙捉虫',
    [ErrorCode.REQUEST_OUTCOME_UNKNOWN]: '结果暂时未知，请重试确认',
  }
  return labels[error.code] ?? `WebSocket 错误 ${error.code}`
}

function responsePatch(response: WsEnvelope): PlayerStatePatch | undefined {
  switch (response.payload.case) {
    case 'buySeedsResponse':
    case 'buyFertilizerResponse':
    case 'plantResponse':
    case 'applyFertilizerResponse':
    case 'catchPestResponse':
    case 'harvestResponse':
    case 'sellCropResponse':
    case 'claimChapterRewardResponse':
    case 'cleanPlotResponse':
      return response.payload.value.patch
    case 'stealFriendCropResponse':
      return response.payload.value.visitorPatch
    default:
      return undefined
  }
}

async function acceptMutationResponse(response: WsEnvelope): Promise<void> {
  setServerClock(response.serverTimeMs)
  lastActionRequestId.value = response.requestId
  wsError.value = response.error
  if (response.error) {
    if (response.error.code === ErrorCode.PRICE_CHANGED) {
      await refreshShop()
    }
    throw new Error(describeWsError(response.error))
  }
  const patch = responsePatch(response)
  const nextVersion = response.stateVersion
  const currentVersion = stateVersion.value
  const currentSnapshot = snapshot.value
  if (!patch || !nextVersion || !currentVersion || !currentSnapshot) {
    throw new Error('写命令响应缺少 patch 或 state_version')
  }
  if (
    nextVersion.ownerEpoch < currentVersion.ownerEpoch ||
    (nextVersion.ownerEpoch === currentVersion.ownerEpoch &&
      nextVersion.playerSeq <= currentVersion.playerSeq)
  ) {
    return
  }
  if (
    nextVersion.ownerEpoch !== currentVersion.ownerEpoch ||
    nextVersion.playerSeq !== currentVersion.playerSeq + 1n
  ) {
    await recoverSnapshotGap()
    return
  }
  snapshot.value = applyPatch(currentSnapshot, patch)
  stateVersion.value = nextVersion
}

function actionSuccessMessage(action: FarmAction, response: WsEnvelope): string {
  switch (action) {
    case 'buy':
      return `已购买 ${response.payload.case === 'buySeedsResponse' ? response.payload.value.quantity : 0} 粒种子，任务进度已更新。`
    case 'buy-fertilizer':
      return `已购买 ${response.payload.case === 'buyFertilizerResponse' ? response.payload.value.quantity : 0} 袋肥料。`
    case 'plant':
      return '种植成功，作物开始成长。'
    case 'fertilize':
      return '施肥成功，等待服务器成熟 Push。'
    case 'catch-pest':
      return '杀虫成功，地块已恢复正常生长。'
    case 'harvest':
      return `收获成功，获得 ${response.payload.case === 'harvestResponse' ? response.payload.value.harvestedQuantity : 0} 个作物。`
    case 'sell':
      return response.payload.case === 'sellCropResponse'
        ? `已出售 ${response.payload.value.soldQuantity} 个作物，获得 ${response.payload.value.totalPrice} 金币。`
        : '出售成功。'
    case 'claim': {
      const pending =
        response.payload.case === 'claimChapterRewardResponse'
          ? response.payload.value.itemsPendingMail.length
          : 0
      return pending > 0
        ? '奖励已领取，仓库溢出物品正在等待邮件处理。'
        : '章节奖励已领取，第二章已激活。'
    }
    case 'clean':
      return '地块清理完成，服务端单玩家闭环已完成。'
  }
}

async function runFarmAction(request: FarmActionRequest): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected || busyAction.value) {
    return
  }
  const { action, plotId } = request
  if (
    (action === 'plant' ||
      action === 'fertilize' ||
      action === 'catch-pest' ||
      action === 'harvest' ||
      action === 'clean') &&
    !plotId
  ) {
    actionError.value = '地块编号缺失'
    return
  }
  busyAction.value = request
  actionMessage.value = ''
  actionError.value = ''
  try {
    let response: WsEnvelope
    switch (action) {
      case 'buy': {
        const crop = cropCatalog.value.find(
          (entry) =>
            entry.seedItemId === request.seedItemId &&
            entry.seedShopEntryId === request.shopEntryId,
        )
        if (!crop || crop.seedShopEntryId === 0) throw new Error('种子目录项尚未加载')
        response = await socket.buySeeds(
          playerId,
          crop.seedShopEntryId,
          request.quantity ?? 1,
          crop.seedPriceVersion,
        )
        break
      }
      case 'buy-fertilizer': {
        const quote = shopEntries.value.find((entry) => entry.itemId === 1)
        if (!quote) throw new Error('肥料报价尚未加载')
        response = await socket.buyFertilizer(
          playerId,
          quote.shopEntryId,
          request.quantity ?? 1,
          quote.priceVersion,
        )
        break
      }
      case 'plant':
        if (!request.seedItemId || !cropCatalog.value.some((crop) => crop.seedItemId === request.seedItemId)) {
          throw new Error('未选择目录中的种子')
        }
        response = await socket.plant(playerId, plotId!, request.seedItemId)
        break
      case 'fertilize':
        response = await socket.applyFertilizer(playerId, plotId!, 1)
        break
      case 'catch-pest':
        response = await socket.catchPest(playerId, plotId!)
        break
      case 'harvest':
        response = await socket.harvest(playerId, plotId!)
        break
      case 'sell': {
        const crop = cropCatalog.value.find((entry) => entry.cropItemId === request.cropItemId)
        if (!crop) throw new Error('作物收购目录尚未加载')
        response = request.sellAll
          ? await socket.sellAll(playerId, crop.cropItemId, crop.sellPriceVersion)
          : await socket.sellQuantity(
              playerId,
              crop.cropItemId,
              request.quantity ?? 1,
              crop.sellPriceVersion,
            )
        break
      }
      case 'claim':
        response = await socket.claimChapterReward(
          playerId,
          snapshot.value?.currentChapter?.chapterId ?? 0,
        )
        break
      case 'clean':
        response = await socket.cleanPlot(playerId, plotId!)
        break
    }
    await acceptMutationResponse(response)
    actionMessage.value = actionSuccessMessage(action, response)
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : String(error)
  } finally {
    busyAction.value = undefined
  }
}

async function refreshPlayerSnapshot(): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected) {
    return
  }
  const response = await socket.requestPlayerSnapshot(playerId)
  if (
    response.error ||
    !response.stateVersion ||
    response.payload.case !== 'getPlayerSnapshotResponse' ||
    !response.payload.value.snapshot
  ) {
    throw new Error('刷新玩家快照失败')
  }
  snapshot.value = response.payload.value.snapshot
  stateVersion.value = response.stateVersion
  serverTimeMs.value = response.serverTimeMs
  setServerClock(response.serverTimeMs)
}

async function refreshFriends(): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected) {
    friends.value = []
    return
  }
  const response = await socket.listFriends(playerId)
  lastActionRequestId.value = response.requestId
  wsError.value = response.error
  if (response.error) {
    friends.value = []
    throw new Error(describeWsError(response.error))
  }
  if (response.payload.case !== 'listFriendsResponse') {
    throw new Error('好友列表响应无效')
  }
  friends.value = response.payload.value.friends
}

async function createFriendCode(): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected || friendBusy.value) {
    return
  }
  friendBusy.value = true
  friendError.value = ''
  friendMessage.value = ''
  try {
    const response = await socket.createFriendCode(playerId)
    lastActionRequestId.value = response.requestId
    wsError.value = response.error
    if (response.error) {
      throw new Error(describeWsError(response.error))
    }
    if (response.payload.case !== 'createFriendCodeResponse') {
      throw new Error('生成好友码响应无效')
    }
    generatedFriendCode.value = response.payload.value.code
    friendMessage.value = '好友码已生成，复制后发给另一个账号。'
  } catch (error) {
    friendError.value = error instanceof Error ? error.message : String(error)
  } finally {
    friendBusy.value = false
  }
}

async function redeemFriendCode(code: string): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected || friendBusy.value) {
    return
  }
  friendBusy.value = true
  friendError.value = ''
  friendMessage.value = ''
  try {
    const response = await socket.redeemFriendCode(playerId, code.trim())
    lastActionRequestId.value = response.requestId
    wsError.value = response.error
    if (response.error) {
      throw new Error(describeWsError(response.error))
    }
    if (response.payload.case !== 'redeemFriendCodeResponse') {
      throw new Error('兑换好友码响应无效')
    }
    const friend = response.payload.value.friend
    friendMessage.value = response.payload.value.newlyCreated
      ? `已添加好友 ${friend?.accountName ?? friend?.playerId.toString()}`
      : `你们已经是好友 ${friend?.accountName ?? friend?.playerId.toString()}`
    await refreshFriends()
    await refreshPlayerSnapshot()
  } catch (error) {
    friendError.value = error instanceof Error ? error.message : String(error)
  } finally {
    friendBusy.value = false
  }
}

async function listFriendsFromPanel(): Promise<void> {
  if (friendRefreshBusy.value) return
  friendRefreshBusy.value = true
  friendRefreshError.value = ''
  try {
    await refreshFriends()
  } catch (error) {
    friendRefreshError.value = error instanceof Error ? error.message : String(error)
  } finally {
    friendRefreshBusy.value = false
  }
}

function selectPanel(panel: PanelId): void {
  activePanel.value = activePanel.value === panel ? null : panel
}

function sameBytes(left: Uint8Array, right: Uint8Array): boolean {
  return left.byteLength === right.byteLength && left.every((value, index) => value === right[index])
}

function sameStateVersion(left: StateVersion, right: StateVersion): boolean {
  return left.ownerEpoch === right.ownerEpoch && left.playerSeq === right.playerSeq
}

function isCurrentVisit(ownerPlayerId: bigint, visitId: Uint8Array): boolean {
  const current = friendVisit.value
  return Boolean(
    current &&
      current.owner.playerId === ownerPlayerId &&
      sameBytes(current.visitId, visitId),
  )
}

function clearFriendVisit(): void {
  if (visitHeartbeatTimer) {
    clearInterval(visitHeartbeatTimer)
    visitHeartbeatTimer = undefined
  }
  friendVisit.value = undefined
  stealBusyPlotId.value = undefined
  visitBusyAction.value = undefined
  stealRetry = undefined
  visitMessage.value = ''
  visitError.value = ''
}

function handleSocketDisconnect(): void {
  clearFriendVisit()
  if (phase.value === 'ready') {
    phase.value = 'disconnected'
  }
}

function expireFriendVisit(error: WsError): void {
  const message = describeWsError(error)
  clearFriendVisit()
  friendError.value = `${message}，请从好友列表重新进入。`
  activePanel.value = 'friends'
}

async function heartbeatFriendVisit(ownerPlayerId: bigint, visitId: Uint8Array): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected || !isCurrentVisit(ownerPlayerId, visitId)) {
    return
  }
  try {
    const response = await socket.heartbeatFriendFarm(playerId, ownerPlayerId, visitId)
    if (!isCurrentVisit(ownerPlayerId, visitId)) {
      return
    }
    setServerClock(response.serverTimeMs)
    if (response.error) {
      if (
        response.error.code === ErrorCode.VISIT_NOT_FOUND ||
        response.error.code === ErrorCode.VISIT_EXPIRED
      ) {
        expireFriendVisit(response.error)
        return
      }
      throw new Error(describeWsError(response.error))
    }
    if (response.payload.case !== 'farmHeartbeatResponse') {
      throw new Error('农场访问心跳响应无效')
    }
    friendVisit.value = {
      ...friendVisit.value!,
      expiresAtMs: response.payload.value.expiresAtMs,
    }
  } catch (error) {
    if (isCurrentVisit(ownerPlayerId, visitId)) {
      visitError.value = error instanceof Error ? error.message : String(error)
    }
  }
}

function startVisitHeartbeat(visit: FriendVisitState): void {
  if (visitHeartbeatTimer) {
    clearInterval(visitHeartbeatTimer)
  }
  const { playerId } = visit.owner
  const visitId = visit.visitId.slice()
  visitHeartbeatTimer = setInterval(() => {
    void heartbeatFriendVisit(playerId, visitId)
  }, 30_000)
}

async function notifyExit(visit: FriendVisitState): Promise<void> {
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected) {
    return
  }
  const response = await socket.exitFriendFarm(playerId, visit.owner.playerId, visit.visitId)
  if (
    response.error &&
    response.error.code !== ErrorCode.VISIT_NOT_FOUND &&
    response.error.code !== ErrorCode.VISIT_EXPIRED
  ) {
    throw new Error(describeWsError(response.error))
  }
  if (!response.error && response.payload.case !== 'exitFriendFarmResponse') {
    throw new Error('退出好友农场响应无效')
  }
}

async function enterFriendFarm(friend: FriendView): Promise<void> {
  const playerId = session.value?.playerId
  if (
    !playerId ||
    !socket.connected ||
    friendBusy.value ||
    visitBusy.value ||
    stealBusyPlotId.value !== undefined
  ) {
    return
  }
  visitBusy.value = true
  friendError.value = ''
  friendMessage.value = ''
  try {
    const previous = friendVisit.value
    if (previous) {
      clearFriendVisit()
      await notifyExit(previous)
    }
    const response = await socket.enterFriendFarm(playerId, friend.playerId)
    lastActionRequestId.value = response.requestId
    wsError.value = response.error
    setServerClock(response.serverTimeMs)
    if (response.error) {
      throw new Error(describeWsError(response.error))
    }
    if (
      response.payload.case !== 'enterFriendFarmResponse' ||
      !response.payload.value.snapshot ||
      response.payload.value.visitId.byteLength !== 16 ||
      response.payload.value.snapshot.ownerPlayerId !== friend.playerId ||
      !response.payload.value.snapshot.ownerStateVersion ||
      response.payload.value.snapshot.ownerStateVersion.ownerEpoch === 0n
    ) {
      throw new Error('进入好友农场响应无效')
    }
    const visit: FriendVisitState = {
      owner: friend,
      visitId: response.payload.value.visitId,
      expiresAtMs: response.payload.value.expiresAtMs,
      snapshot: response.payload.value.snapshot,
      ownerStateVersion: response.payload.value.snapshot.ownerStateVersion,
    }
    friendVisit.value = visit
    visitMessage.value = `已进入 ${friend.accountName || friend.playerId.toString()} 的农场。`
    activePanel.value = null
    startVisitHeartbeat(visit)
  } catch (error) {
    friendError.value = error instanceof Error ? error.message : String(error)
    activePanel.value = 'friends'
  } finally {
    visitBusy.value = false
  }
}

async function exitFriendFarm(): Promise<void> {
  const visit = friendVisit.value
  if (!visit || visitBusy.value || stealBusyPlotId.value !== undefined) {
    return
  }
  visitBusy.value = true
  clearFriendVisit()
  try {
    await notifyExit(visit)
    friendMessage.value = '已返回自己的农场。'
  } catch (error) {
    friendError.value = error instanceof Error ? error.message : String(error)
  } finally {
    visitBusy.value = false
  }
}

function updateVisitedPlot(
  plot: PublicPlotView,
  expectedOwnerVersion?: StateVersion,
): void {
  const visit = friendVisit.value
  if (
    !visit ||
    (expectedOwnerVersion &&
      !sameStateVersion(visit.ownerStateVersion, expectedOwnerVersion))
  ) {
    return
  }
  friendVisit.value = {
    ...visit,
    snapshot: {
      ...visit.snapshot,
      plots: mergePublicPlotUpserts(visit.snapshot.plots, [plot]),
    },
  }
}

async function runFriendPestAction(
  plot: PublicPlotView,
  action: 'pest' | 'catch',
): Promise<void> {
  const playerId = session.value?.playerId
  const visit = friendVisit.value
  const validPlot =
    plot.plotId > 0 &&
    plot.plotState === PlotState.GROWING &&
    (action === 'pest' ? !plot.pestActive : plot.pestActive)
  if (
    !playerId ||
    !socket.connected ||
    !visit ||
    !validPlot ||
    visitBusy.value ||
    stealBusyPlotId.value !== undefined
  ) {
    return
  }

  const ownerVersionAtRequest = visit.ownerStateVersion
  stealBusyPlotId.value = plot.plotId
  visitBusyAction.value = action
  visitMessage.value = ''
  visitError.value = ''
  try {
    const response =
      action === 'pest'
        ? await socket.applyPestToFriend(
            playerId,
            visit.owner.playerId,
            visit.visitId,
            plot.plotId,
          )
        : await socket.catchPestForFriend(
            playerId,
            visit.owner.playerId,
            visit.visitId,
            plot.plotId,
          )
    if (!isCurrentVisit(visit.owner.playerId, visit.visitId)) {
      return
    }
    lastActionRequestId.value = response.requestId
    wsError.value = response.error
    setServerClock(response.serverTimeMs)
    if (response.error) {
      if (
        response.error.code === ErrorCode.VISIT_NOT_FOUND ||
        response.error.code === ErrorCode.VISIT_EXPIRED
      ) {
        expireFriendVisit(response.error)
        return
      }
      throw new Error(describeWsError(response.error))
    }
    const ownerPlot =
      action === 'pest'
        ? response.payload.case === 'applyPestToFriendResponse'
          ? response.payload.value.ownerPlot
          : undefined
        : response.payload.case === 'catchPestForFriendResponse'
          ? response.payload.value.ownerPlot
          : undefined
    if (!ownerPlot || ownerPlot.plotId !== plot.plotId) {
      throw new Error(action === 'pest' ? '投虫响应缺少好友地块' : '捉虫响应缺少好友地块')
    }
    // Friend action responses do not carry an owner version. Merge only while
    // the same visit still has the version observed at request start; a newer
    // FRIEND_FARM_CHANGED Push remains authoritative.
    updateVisitedPlot(ownerPlot, ownerVersionAtRequest)
    visitMessage.value = action === 'pest' ? '投虫成功。' : '捉虫成功。'
  } catch (error) {
    if (friendVisit.value) {
      visitError.value = error instanceof Error ? error.message : String(error)
    }
  } finally {
    stealBusyPlotId.value = undefined
    visitBusyAction.value = undefined
  }
}

function applyPestToFriend(plot: PublicPlotView): Promise<void> {
  return runFriendPestAction(plot, 'pest')
}

function catchPestForFriend(plot: PublicPlotView): Promise<void> {
  return runFriendPestAction(plot, 'catch')
}

async function stealFriendCrop(plot: PublicPlotView): Promise<void> {
  const playerId = session.value?.playerId
  const visit = friendVisit.value
  if (
    !playerId ||
    !socket.connected ||
    !visit ||
    !plot.canSteal ||
    visitBusy.value ||
    stealBusyPlotId.value !== undefined
  ) {
    return
  }
  const matchingRetry =
    stealRetry?.ownerPlayerId === visit.owner.playerId &&
    stealRetry.plotId === plot.plotId &&
    stealRetry.cropItemId === plot.cropItemId &&
    stealRetry.plantedAtMs === plot.plantedAtMs &&
    stealRetry.quantity === plot.stealQuantity
  const requestId = matchingRetry ? stealRetry!.requestId : crypto.randomUUID()
  const ownerVersionAtRequest = visit.ownerStateVersion
  stealBusyPlotId.value = plot.plotId
  visitBusyAction.value = 'steal'
  visitMessage.value = ''
  visitError.value = ''
  try {
    const response = await socket.stealFriendCrop(
      playerId,
      visit.owner.playerId,
      visit.visitId,
      plot.plotId,
      plot.cropItemId,
      plot.plantedAtMs,
      plot.stealQuantity,
      requestId,
    )
    if (!isCurrentVisit(visit.owner.playerId, visit.visitId)) {
      return
    }
    lastActionRequestId.value = response.requestId
    wsError.value = response.error
    setServerClock(response.serverTimeMs)
    if (response.error) {
      if (response.error.code === ErrorCode.REQUEST_OUTCOME_UNKNOWN) {
        stealRetry = {
          requestId,
          ownerPlayerId: visit.owner.playerId,
          plotId: plot.plotId,
          cropItemId: plot.cropItemId,
          plantedAtMs: plot.plantedAtMs,
          quantity: plot.stealQuantity,
        }
      } else {
        stealRetry = undefined
      }
      if (
        response.error.code === ErrorCode.VISIT_NOT_FOUND ||
        response.error.code === ErrorCode.VISIT_EXPIRED
      ) {
        expireFriendVisit(response.error)
        return
      }
      if (response.error.code === ErrorCode.STEAL_NOT_AVAILABLE) {
        updateVisitedPlot(
          { ...plot, canSteal: false, stealQuantity: 0 },
          ownerVersionAtRequest,
        )
      }
      throw new Error(describeWsError(response.error))
    }
    if (
      response.payload.case !== 'stealFriendCropResponse' ||
      !response.payload.value.visitorPatch ||
      !response.payload.value.ownerPlot
    ) {
      throw new Error('偷菜响应缺少玩家 patch 或好友地块')
    }
    stealRetry = undefined
    await acceptMutationResponse(response)
    // A newer owner Push may arrive while the visitor mutation is being
    // accepted. Only apply the response plot if the visit version is still
    // the one observed when this steal started; otherwise the Push wins.
    updateVisitedPlot(response.payload.value.ownerPlot, ownerVersionAtRequest)
    visitMessage.value = `偷菜成功，获得 ${response.payload.value.stolenQuantity} 个作物。`
  } catch (error) {
    if (friendVisit.value) {
      visitError.value = error instanceof Error ? error.message : String(error)
    }
  } finally {
    stealBusyPlotId.value = undefined
    visitBusyAction.value = undefined
  }
}

function handlePlayerStateChanged(envelope: WsEnvelope): void {
  const version = envelope.stateVersion
  const currentVersion = stateVersion.value
  const currentSnapshot = snapshot.value
  if (
    !version ||
    envelope.targetPlayerId !== session.value?.playerId ||
    envelope.payload.case !== 'playerStateChangedPush'
  ) {
    return
  }
  if (!currentVersion || !currentSnapshot) {
    void recoverSnapshotGap()
    return
  }
  if (
    version.ownerEpoch < currentVersion.ownerEpoch ||
    (version.ownerEpoch === currentVersion.ownerEpoch &&
      version.playerSeq <= currentVersion.playerSeq)
  ) {
    return
  }
  if (
    version.ownerEpoch !== currentVersion.ownerEpoch ||
    version.playerSeq !== currentVersion.playerSeq + 1n
  ) {
    void recoverSnapshotGap()
    return
  }
  snapshot.value = applyPatch(currentSnapshot, envelope.payload.value.patch!)
  stateVersion.value = version
  serverTimeMs.value = envelope.serverTimeMs
  setServerClock(envelope.serverTimeMs)
  pushCount.value += 1
  actionMessage.value = '作物已经成熟，可以收获。'
}

function handleFriendFarmChanged(envelope: WsEnvelope): void {
  const playerId = session.value?.playerId
  const visit = friendVisit.value
  if (
    !playerId ||
    envelope.targetPlayerId !== playerId ||
    envelope.payload.case !== 'friendFarmChangedPush' ||
    !visit
  ) {
    return
  }
  const push = envelope.payload.value
  const nextVersion = push.ownerStateVersion
  if (
    push.ownerPlayerId !== visit.owner.playerId ||
    !sameBytes(push.visitId, visit.visitId) ||
    !nextVersion ||
    !isNewerStateVersion(nextVersion, visit.ownerStateVersion)
  ) {
    return
  }
  const plots = mergePublicPlotUpserts(visit.snapshot.plots, push.plotUpserts)
  // Replace the visit as one value so plots and their owner version cannot be
  // observed from different Pushes. Player-sequence gaps are valid here
  // because private owner mutations do not all produce public farm updates.
  friendVisit.value = {
    ...visit,
    ownerStateVersion: nextVersion,
    snapshot: {
      ...visit.snapshot,
      ownerStateVersion: nextVersion,
      plots,
    },
  }
  serverTimeMs.value = envelope.serverTimeMs
  setServerClock(envelope.serverTimeMs)
}

function recoverSnapshotGap(): Promise<void> {
  if (gapRecovery) {
    return gapRecovery
  }
  const playerId = session.value?.playerId
  if (!playerId || !socket.connected) {
    return Promise.resolve()
  }
  gapRecoveryCount.value += 1
  gapRecovery = socket
    .requestPlayerSnapshot(playerId)
    .then((response) => {
      if (
        response.error ||
        !response.stateVersion ||
        response.payload.case !== 'getPlayerSnapshotResponse' ||
        !response.payload.value.snapshot
      ) {
        throw new Error('Push 版本缺口恢复快照失败')
      }
      snapshot.value = response.payload.value.snapshot
      stateVersion.value = response.stateVersion
      serverTimeMs.value = response.serverTimeMs
      setServerClock(response.serverTimeMs)
    })
    .catch((error) => {
      phase.value = 'failed'
      errorMessage.value = error instanceof Error ? error.message : String(error)
    })
    .finally(() => {
      gapRecovery = undefined
    })
  return gapRecovery
}

async function establishSnapshot(): Promise<void> {
  if (!session.value) {
    throw new Error('请先注册或登录')
  }
  clearResult()
  socket.disconnect()

  phase.value = 'csrf'
  csrfToken.value = (await fetchCsrf()).csrfToken

  phase.value = 'bootstrap'
  const bootstrap = await fetchBootstrap()
  const expectedAuth = bootstrap.authBootstrap
  if (!expectedAuth || expectedAuth.playerId !== session.value.playerId) {
    throw new Error('bootstrap player_id 与 Session 不一致')
  }
  if (expectedAuth.protocolMin > 1 || expectedAuth.protocolMax < 1) {
    throw new Error('bootstrap 不支持 WebSocket 协议 V1')
  }

  phase.value = 'config'
  clientConfig.value = await downloadClientConfig(
    expectedAuth.clientConfigUrl,
    expectedAuth.clientConfigSha256,
    expectedAuth.clientConfigVersion,
  )

  gateway.value = selectGateway(bootstrap.gateways)
  phase.value = 'ticket'
  const ticket = await issueWsTicket(gateway.value.gatewayId, csrfToken.value)

  phase.value = 'socket'
  const connection = await socket.connectAndAuth(
    gateway.value.websocketUrl,
    ticket.wsTicket,
    session.value.playerId,
    () => {
      phase.value = 'auth'
    },
  )
  authRequestId.value = connection.requestId

  const authConfigChanged =
    connection.auth.clientConfigVersion !== expectedAuth.clientConfigVersion ||
    connection.auth.clientConfigUrl !== expectedAuth.clientConfigUrl ||
    !bytesEqual(connection.auth.clientConfigSha256, expectedAuth.clientConfigSha256)
  if (authConfigChanged) {
    phase.value = 'config'
    clientConfig.value = await downloadClientConfig(
      connection.auth.clientConfigUrl,
      connection.auth.clientConfigSha256,
      connection.auth.clientConfigVersion,
    )
  }

  phase.value = 'snapshot'
  const response = await socket.requestPlayerSnapshot(connection.auth.playerId)
  snapshotRequestId.value = response.requestId
  serverTimeMs.value = response.serverTimeMs
  setServerClock(response.serverTimeMs)
  stateVersion.value = response.stateVersion
  wsError.value = response.error
  if (response.error) {
    throw new Error(`快照请求失败：WebSocket 错误 ${response.error.code}`)
  }
  if (
    response.payload.case !== 'getPlayerSnapshotResponse' ||
    !response.payload.value.snapshot ||
    !response.stateVersion
  ) {
    throw new Error('快照响应缺少 payload 或 state_version')
  }
  if (response.payload.value.snapshot.playerId !== connection.auth.playerId) {
    throw new Error('快照 player_id 与认证身份不一致')
  }
  snapshot.value = response.payload.value.snapshot
  await refreshShop()
  try {
    await refreshFriends()
  } catch (error) {
    friends.value = []
    friendError.value = error instanceof Error ? error.message : String(error)
  }
  phase.value = 'ready'
}

function httpErrorCode(error: unknown): HttpErrorCode | undefined {
  return error instanceof ProtobufHttpError ? error.code : undefined
}

function describeAuthError(error: unknown): string {
  switch (httpErrorCode(error)) {
    case HttpErrorCode.INVALID_CREDENTIALS:
      return '账号或密码错误'
    case HttpErrorCode.INVALID_ARGUMENT:
      return '账号需为 3–32 位小写字母、数字或下划线，密码至少 12 个字符'
    case HttpErrorCode.RATE_LIMITED:
      return '尝试次数过多，请稍后再试'
    case HttpErrorCode.CSRF_REJECTED:
      return '登录页面已过期，请刷新后重试'
    default:
      return error instanceof Error ? error.message : String(error)
  }
}

async function authenticateOrRegister(): Promise<SessionView> {
  try {
    return await authenticate('login', accountName.value, password.value, csrfToken.value)
  } catch (error) {
    if (httpErrorCode(error) !== HttpErrorCode.INVALID_CREDENTIALS) {
      throw error
    }
    try {
      return await authenticate('register', accountName.value, password.value, csrfToken.value)
    } catch (registerError) {
      if (httpErrorCode(registerError) === HttpErrorCode.ACCOUNT_NAME_UNAVAILABLE) {
        throw new Error('账号或密码错误')
      }
      throw registerError
    }
  }
}

function markTabSession(playerId: bigint): void {
  try {
    sessionStorage.setItem(TAB_SESSION_STORAGE_KEY, playerId.toString())
  } catch {
    // Without a tab marker, reload safely falls back to the login screen.
  }
}

function clearTabSession(): void {
  try {
    sessionStorage.removeItem(TAB_SESSION_STORAGE_KEY)
  } catch {
    // Storage may be unavailable in a restricted browser context.
  }
}

function loadTabSessionPlayerId(): bigint | undefined {
  try {
    const raw = sessionStorage.getItem(TAB_SESSION_STORAGE_KEY)
    return raw ? BigInt(raw) : undefined
  } catch {
    clearTabSession()
    return undefined
  }
}

async function submitCredentials(): Promise<void> {
  if (busy.value) {
    return
  }
  busy.value = true
  clearResult()
  try {
    phase.value = 'csrf'
    csrfToken.value = (await fetchCsrf()).csrfToken
    phase.value = 'session'
    session.value = await authenticateOrRegister()
    password.value = ''

    // Successful registration/login rotates the token; never reuse the pre-auth value.
    csrfToken.value = (await fetchCsrf()).csrfToken
    await establishSnapshot()
    if (session.value) {
      markTabSession(session.value.playerId)
    }
  } catch (error) {
    phase.value = 'failed'
    errorMessage.value = describeAuthError(error)
  } finally {
    busy.value = false
  }
}

async function reconnect(): Promise<void> {
  busy.value = true
  try {
    await establishSnapshot()
    if (session.value) {
      markTabSession(session.value.playerId)
    }
  } catch (error) {
    phase.value = 'failed'
    errorMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    busy.value = false
  }
}

function disconnect(): void {
  socket.disconnect()
  phase.value = 'disconnected'
}

async function signOut(): Promise<void> {
  const visit = friendVisit.value
  if (visit) {
    clearFriendVisit()
    try {
      await notifyExit(visit)
    } catch {
      // Logout still tears down the local and HTTP sessions if visit exit fails.
    }
  }
  socket.disconnect()
  clearTabSession()
  try {
    const token = csrfToken.value || (await fetchCsrf()).csrfToken
    await logout(token)
  } catch {
    // The cookie may already be expired; local logout must still complete.
  }
  clearResult()
  session.value = undefined
  password.value = ''
  phase.value = 'idle'
}

async function endOrphanedCookieSession(): Promise<void> {
  try {
    const existing = await fetchSession()
    if (!existing) return
    await logout((await fetchCsrf()).csrfToken)
  } catch {
    // First visits and already-expired sessions remain on the login form.
  }
}

async function resumeAfterReload(): Promise<void> {
  const tabPlayerId = loadTabSessionPlayerId()
  if (tabPlayerId === undefined) {
    await endOrphanedCookieSession()
    return
  }
  busy.value = true
  try {
    const existing = await fetchSession()
    if (!existing || existing.playerId !== tabPlayerId) {
      clearTabSession()
      return
    }
    session.value = existing
    accountName.value = existing.accountName
    await establishSnapshot()
    markTabSession(existing.playerId)
  } catch (error) {
    phase.value = 'failed'
    errorMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  clockTimer = setInterval(() => {
    nowMs.value = BigInt(Date.now()) + serverClockOffsetMs
  }, 1000)
  void resumeAfterReload()
})

onBeforeUnmount(() => {
  if (clockTimer) {
    clearInterval(clockTimer)
  }
  socket.disconnect()
})
</script>

<template>
  <main v-if="!signedIn" class="login-shell">
    <section class="login-card" aria-labelledby="login-title">
      <h1 id="login-title" class="login-title">Grow!</h1>
      <p class="login-copy">登录现有账号；账号不存在时会自动注册。</p>
      <form class="login-form" @submit.prevent="submitCredentials">
        <label>
          账号
          <input
            v-model="accountName"
            autocomplete="username"
            minlength="3"
            maxlength="32"
            pattern="[a-z][a-z0-9_]{2,31}"
            placeholder="lowercase_account"
            required
          />
        </label>
        <label>
          密码
          <input
            v-model="password"
            autocomplete="current-password"
            type="password"
            minlength="12"
            maxlength="128"
            placeholder="至少 12 个字符"
            required
          />
        </label>
        <button class="primary" type="submit" :disabled="busy">
          {{ busy ? '正在进入…' : '登录 / 注册' }}
        </button>
      </form>
      <p v-if="errorMessage" class="error-banner" role="alert">{{ errorMessage }}</p>
    </section>
  </main>

  <main v-else class="game-shell">
    <TopNav
      :account-name="session?.accountName ?? ''"
      :coin-balance="snapshot?.coinBalance"
      :friend-count="friends.length"
      :active-panel="activePanel"
      @select="selectPanel"
    />

    <p v-if="phase === 'disconnected' || phase === 'failed'" class="shell-notice" role="status">
      <span>{{ errorMessage || phaseLabels[phase] }}</span>
      <button type="button" :disabled="!canConnect" @click="reconnect">
        {{ busy ? '连接中…' : '重新连接' }}
      </button>
    </p>

    <FriendFarmDashboard
      v-if="friendVisit"
      :owner="friendVisit.owner"
      :snapshot="friendVisit.snapshot"
      :crop-catalog="cropCatalog"
      :connected="socket.connected"
      :expires-at-ms="friendVisit.expiresAtMs"
      :now-ms="nowMs"
      :busy="visitBusy"
      :busy-plot-id="stealBusyPlotId"
      :busy-action="visitBusyAction"
      :message="visitMessage"
      :error="visitError"
      @exit="exitFriendFarm"
      @pest="applyPestToFriend"
      @catch="catchPestForFriend"
      @steal="stealFriendCrop"
    />

    <FarmDashboard
      v-else-if="snapshot"
      :snapshot="snapshot"
      :crop-catalog="cropCatalog"
      :connected="socket.connected"
      :busy-action="busyAction"
      :action-message="actionMessage"
      :action-error="actionError"
      :now-ms="nowMs"
      @action="runFarmAction"
      @open-shop="activePanel = 'shop'"
      @reload-catalog="refreshShop"
    />

    <GameDrawer
      :open="activePanel === 'account'"
      :title="panelTitles.account"
      :kicker="panelKickers.account"
      @close="activePanel = null"
    >
      <AccountPanel
        :account-name="session?.accountName ?? ''"
        :player-id="session?.playerId.toString() ?? ''"
        :phase-label="phaseLabels[phase]"
        :connected="socket.connected"
        :busy="busy"
        :can-reconnect="canConnect"
        :error-message="errorMessage"
        :steps="timelineSteps"
        :facts="diagnosticFacts"
        @reconnect="reconnect"
        @disconnect="disconnect"
        @logout="signOut"
      />
    </GameDrawer>

    <GameDrawer
      :open="activePanel === 'shop'"
      :title="panelTitles.shop"
      :kicker="panelKickers.shop"
      @close="activePanel = null"
    >
      <ShopPanel
        :shop-entries="shopEntries"
        :crop-catalog="cropCatalog"
        :inventory="inventoryMap"
        :coin-balance="snapshot?.coinBalance"
        :connected="socket.connected"
        :busy-action="busyAction"
        @action="runFarmAction"
      />
    </GameDrawer>

    <GameDrawer
      :open="activePanel === 'tasks'"
      :title="panelTitles.tasks"
      :kicker="panelKickers.tasks"
      @close="activePanel = null"
    >
      <TaskPanel
        :chapter="snapshot?.currentChapter"
        :connected="socket.connected"
        :busy-action="busyAction"
        @action="runFarmAction"
      />
    </GameDrawer>

    <GameDrawer
      :open="activePanel === 'inventory'"
      :title="panelTitles.inventory"
      :kicker="panelKickers.inventory"
      @close="activePanel = null"
    >
      <InventoryPanel
        :shop-entries="shopEntries"
        :crop-catalog="cropCatalog"
        :inventory="inventoryMap"
        :connected="socket.connected"
        :busy-action="busyAction"
        @action="runFarmAction"
      />
    </GameDrawer>

    <GameDrawer
      :open="activePanel === 'friends'"
      :title="panelTitles.friends"
      :kicker="panelKickers.friends"
      @close="activePanel = null"
    >
      <FriendsPanel
        :friends="friends"
        :connected="socket.connected"
        :busy="friendBusy || visitBusy || stealBusyPlotId !== undefined"
        :refresh-busy="friendRefreshBusy"
        :refresh-error="friendRefreshError"
        :generated-code="generatedFriendCode"
        :message="friendMessage"
        :error="friendError"
        :visiting-owner-id="friendVisit?.owner.playerId"
        @generate="createFriendCode"
        @redeem="redeemFriendCode"
        @refresh="listFriendsFromPanel"
        @enter="enterFriendFarm"
      />
    </GameDrawer>
  </main>
</template>
