<script setup lang="ts">
import { computed, ref } from 'vue'
import type {
  CropCatalogEntryView,
  FarmVisitSnapshot,
  FriendView,
  PublicPlotView,
} from '../gen/classicfarm/v1/ws/ws_pb'
import { PlotState } from '../gen/classicfarm/v1/ws/plot/plot_state_pb'
import { matureCropSprite } from '../lib/crop-art'

import plotEmpty from '../../../frontend/src/assets/art/runtime/plots/empty.png'
import plotGrowing from '../../../frontend/src/assets/art/runtime/plots/growing.png'
import plotMature from '../../../frontend/src/assets/art/runtime/plots/mature.png'
import plotCleanup from '../../../frontend/src/assets/art/runtime/plots/need-cleanup.png'
import cropGrowing from '../../../frontend/src/assets/art/runtime/crops/demo-growing.png'
import handTool from '../../../frontend/src/assets/art/runtime/tools/hand.png'
import fertilizerTool from '../../../frontend/src/assets/art/runtime/tools/fertilizer.png'
import seedIcon from '../../../frontend/src/assets/art/runtime/items/demo-seed.png'

type VisitAction = 'pest' | 'catch' | 'steal'

const props = defineProps<{
  owner: FriendView
  snapshot: FarmVisitSnapshot
  cropCatalog: CropCatalogEntryView[]
  connected: boolean
  expiresAtMs: bigint
  nowMs: bigint
  busy: boolean
  busyPlotId?: number
  busyAction?: VisitAction
  message: string
  error: string
}>()

const emit = defineEmits<{
  exit: []
  steal: [plot: PublicPlotView]
  pest: [plot: PublicPlotView]
  catch: [plot: PublicPlotView]
}>()

const selectedAction = ref<VisitAction>('steal')
const plots = computed(() => [...props.snapshot.plots].sort((left, right) => left.plotId - right.plotId))
const tools: Array<{ id: VisitAction; label: string; hint: string; icon: string }> = [
  { id: 'pest', label: '投虫', hint: '成长中且无虫', icon: seedIcon },
  { id: 'catch', label: '捉虫', hint: '成长中且有虫', icon: fertilizerTool },
  { id: 'steal', label: '偷菜', hint: '可偷的成熟作物', icon: handTool },
]
const leaseSeconds = computed(() => {
  if (props.expiresAtMs <= props.nowMs) return 0
  return Number((props.expiresAtMs - props.nowMs + 999n) / 1000n)
})

function presentation(plot: PublicPlotView) {
  const cropName =
    props.cropCatalog.find((crop) => crop.cropId === plot.cropId)?.name ||
    (plot.cropId ? `作物#${plot.cropId}` : '')
  switch (plot.plotState) {
    case PlotState.GROWING:
      return {
        label: plot.pestActive ? `${cropName}成长中 · 有虫` : `${cropName}成长中`,
        base: plotGrowing,
        crop: cropGrowing,
      }
    case PlotState.MATURE:
      return { label: `${cropName}已成熟`, base: plotMature, crop: matureCropSprite(plot.cropId) }
    case PlotState.NEED_CLEANUP:
      return { label: `${cropName || '作物'}待清理`, base: plotCleanup, crop: undefined }
    default:
      return { label: '空地', base: plotEmpty, crop: undefined }
  }
}

function matureSeconds(plot: PublicPlotView): number {
  if (plot.estimatedMatureAtMs <= props.nowMs) return 0
  return Number((plot.estimatedMatureAtMs - props.nowMs + 999n) / 1000n)
}

function plotMeta(plot: PublicPlotView): string {
  if (plot.plotState === PlotState.GROWING) {
    const seconds = matureSeconds(plot)
    const timing = seconds > 0 ? `预计 ${seconds} 秒后成熟` : '等待服务器确认成熟'
    return plot.pestActive ? `有害虫 · ${timing}` : timing
  }
  if (plot.plotState === PlotState.MATURE) {
    return `剩余 ${plot.harvestableQuantity} 个 · 已被偷 ${plot.stealCount} 次`
  }
  if (plot.plotState === PlotState.NEED_CLEANUP) return '主人已经收获'
  return '这里还没有作物'
}

function isValidTarget(plot: PublicPlotView): boolean {
  switch (selectedAction.value) {
    case 'pest':
      return plot.plotState === PlotState.GROWING && !plot.pestActive
    case 'catch':
      return plot.plotState === PlotState.GROWING && plot.pestActive
    case 'steal':
      return plot.canSteal
  }
}

function actionLabel(plot: PublicPlotView): string {
  if (props.busyPlotId === plot.plotId) {
    if (props.busyAction === 'pest') return '投虫中…'
    if (props.busyAction === 'catch') return '捉虫中…'
    return '偷取中…'
  }
  if (!isValidTarget(plot)) {
    if (selectedAction.value === 'pest') return '不可投虫'
    if (selectedAction.value === 'catch') return '无虫可捉'
    return '不可偷取'
  }
  if (selectedAction.value === 'steal') return `偷取 ${plot.stealQuantity} 个`
  return tools.find((tool) => tool.id === selectedAction.value)?.label ?? '操作'
}

function requestAction(plot: PublicPlotView): void {
  if (!isValidTarget(plot) || !props.connected || props.busy || props.busyPlotId !== undefined) {
    return
  }
  emit(selectedAction.value, plot)
}
</script>

<template>
  <section class="friend-farm" aria-label="好友农场">
    <header class="friend-farm__toolbar">
      <div>
        <p class="eyebrow">FRIEND FARM · PUBLIC SNAPSHOT</p>
        <h2>{{ owner.accountName || `玩家 ${owner.playerId.toString()}` }} 的农场</h2>
        <small>访问租约剩余约 {{ leaseSeconds }} 秒 · 每 30 秒自动续期</small>
      </div>
      <button
        type="button"
        :disabled="busy || busyPlotId !== undefined"
        @click="emit('exit')"
      >
        {{ busy ? '退出中…' : '返回我的农场' }}
      </button>
    </header>

    <p v-if="error" class="action-notice error-banner" role="alert">{{ error }}</p>
    <p v-else-if="message" class="action-notice success-banner" role="status">{{ message }}</p>

    <nav class="visit-tools" aria-label="好友农场工具">
      <button
        v-for="tool in tools"
        :key="tool.id"
        type="button"
        :class="{ selected: selectedAction === tool.id }"
        :aria-pressed="selectedAction === tool.id"
        @click="selectedAction = tool.id"
      >
        <img class="pixel-art" :src="tool.icon" alt="" />
        <span>{{ tool.label }}</span>
        <small>{{ tool.hint }}</small>
      </button>
    </nav>

    <article class="friend-farm__panel">
      <div class="friend-farm__heading">
        <div>
          <span class="panel-kicker">PUBLIC PLOTS</span>
          <h3>好友的公开地块</h3>
        </div>
        <span class="state-pill">实时接收主人地块变更</span>
      </div>

      <div class="friend-plots">
        <article v-for="plot in plots" :key="plot.plotId" class="friend-plot">
          <span class="plot-number">
            PLOT {{ String(plot.plotId).padStart(2, '0') }}
            <em v-if="plot.pestActive" class="pest-badge">有虫</em>
          </span>
          <span class="friend-plot__stage">
            <img class="friend-plot__base pixel-art" :src="presentation(plot).base" alt="" />
            <img
              v-if="presentation(plot).crop"
              class="friend-plot__crop pixel-art"
              :src="presentation(plot).crop"
              alt=""
            />
          </span>
          <strong>{{ presentation(plot).label }}</strong>
          <small>{{ plotMeta(plot) }}</small>
          <button
            type="button"
            class="steal-button"
            :class="{ available: isValidTarget(plot) }"
            :disabled="!connected || busy || busyPlotId !== undefined || !isValidTarget(plot)"
            @click="requestAction(plot)"
          >
            <img
              class="pixel-art"
              :src="tools.find((tool) => tool.id === selectedAction)?.icon"
              alt=""
            />
            {{ actionLabel(plot) }}
          </button>
        </article>
      </div>
    </article>
  </section>
</template>

<style scoped>
.friend-farm {
  display: grid;
  gap: 1rem;
  margin: 1.4rem 0;
  padding: clamp(1rem, 3vw, 1.6rem);
  border: 2px solid #71895e;
  border-radius: 1.35rem;
  background:
    linear-gradient(rgb(255 255 255 / 8%) 1px, transparent 1px),
    linear-gradient(90deg, rgb(255 255 255 / 8%) 1px, transparent 1px),
    #8eb86d;
  background-size: 32px 32px;
  box-shadow:
    inset 0 0 0 4px rgb(255 255 255 / 18%),
    0 1.2rem 3rem rgb(42 61 35 / 18%);
}

.friend-farm__toolbar,
.friend-farm__heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.8rem;
}

.friend-farm__toolbar {
  padding: 0.3rem 0.3rem 0;
}

.friend-farm__toolbar h2,
.friend-farm__panel h3 {
  margin: 0;
  color: #24361f;
}

.friend-farm__toolbar small {
  display: block;
  margin-top: 0.35rem;
  color: #41573a;
  font-size: 0.72rem;
}

.friend-farm__toolbar button {
  min-height: 2.75rem;
  flex: none;
  padding: 0.65rem 1rem;
  font-weight: 750;
}

.friend-farm__panel {
  padding: 1rem;
  border: 2px solid #8b6c42;
  border-radius: 1rem;
  background: #fff8dc;
  box-shadow:
    inset 0 -4px 0 rgb(102 69 31 / 10%),
    0 0.5rem 1.2rem rgb(44 58 34 / 12%);
}

.visit-tools {
  display: flex;
  gap: 0.5rem;
  overflow-x: auto;
}

.visit-tools button {
  display: grid;
  grid-template-columns: auto auto;
  align-items: center;
  gap: 0.05rem 0.4rem;
  min-width: 8rem;
  padding: 0.45rem 0.65rem;
  text-align: left;
}

.visit-tools button.selected {
  border-color: #31552d;
  background: #dfecc2;
  box-shadow: 0 0 0 3px rgb(49 85 45 / 15%);
}

.visit-tools img {
  grid-row: 1 / 3;
  width: 1.5rem;
  height: 1.5rem;
}

.visit-tools small {
  color: #66705d;
  font-size: 0.62rem;
}

.pest-badge {
  margin-left: 0.35rem;
  padding: 0.05rem 0.32rem;
  border-radius: 99rem;
  background: #8a5a2b;
  color: #fff8dc;
  font-style: normal;
  font-size: 0.58rem;
}

.friend-plots {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.7rem;
  margin-top: 0.8rem;
}

.friend-plot {
  display: grid;
  gap: 0.35rem;
  min-width: 0;
  padding: 0.55rem;
  border: 2px solid #9a7a50;
  border-radius: 0.65rem;
  background: #f5e7bd;
}

.friend-plot > strong {
  color: #33472e;
  font-size: 0.78rem;
}

.friend-plot > small {
  min-height: 1.9rem;
  color: #66705d;
  font-size: 0.65rem;
}

.friend-plot__stage {
  position: relative;
  display: grid;
  min-height: 8.5rem;
  place-items: center;
  overflow: hidden;
  border: 2px solid #755331;
  border-radius: 0.8rem;
  background: #80ad62;
}

.friend-plot__stage::after {
  position: absolute;
  inset: auto 0 0;
  height: 24%;
  background: #d7b56e;
  content: "";
  opacity: 0.35;
}

.friend-plot__base {
  z-index: 1;
  width: min(78%, 8rem);
}

.friend-plot__crop {
  position: absolute;
  z-index: 2;
  width: min(36%, 4rem);
  filter: drop-shadow(0 0.45rem 0 rgb(51 74 36 / 20%));
}

.steal-button {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.4rem;
  min-height: 2.65rem;
  padding: 0.5rem;
  font-size: 0.75rem;
  font-weight: 800;
}

.steal-button.available {
  border-color: #31552d;
  background: #31552d;
  color: white;
}

.steal-button img {
  width: 1.4rem;
  height: 1.4rem;
}

.action-notice {
  margin: 0;
}

@media (width <= 27.5rem) {
  .friend-farm__toolbar,
  .friend-farm__heading {
    align-items: flex-start;
    flex-direction: column;
  }

  .friend-farm__toolbar button {
    width: 100%;
  }

  .friend-plot__stage {
    min-height: 7rem;
  }

  .state-pill {
    max-width: 100%;
    white-space: normal;
  }
}
</style>
