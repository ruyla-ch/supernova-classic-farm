<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { CropCatalogEntryView, PlayerSnapshot, PlotView } from '../gen/classicfarm/v1/ws/ws_pb'
import { PlotState } from '../gen/classicfarm/v1/ws/plot/plot_state_pb'
import type { FarmActionRequest } from '../lib/farm-actions'
import { matureCropSprite } from '../lib/crop-art'

import plotEmpty from '../../../frontend/src/assets/art/runtime/plots/empty.png'
import plotGrowing from '../../../frontend/src/assets/art/runtime/plots/growing.png'
import plotMature from '../../../frontend/src/assets/art/runtime/plots/mature.png'
import plotCleanup from '../../../frontend/src/assets/art/runtime/plots/need-cleanup.png'
import cropGrowing from '../../../frontend/src/assets/art/runtime/crops/demo-growing.png'
import effectIcon from '../../../frontend/src/assets/art/runtime/effects/fertilized.png'
import seedIcon from '../../../frontend/src/assets/art/runtime/items/demo-seed.png'
import fertilizerTool from '../../../frontend/src/assets/art/runtime/tools/fertilizer.png'
import shovelTool from '../../../frontend/src/assets/art/runtime/tools/shovel.png'
import handTool from '../../../frontend/src/assets/art/runtime/tools/hand.png'

type FarmTool = 'seed' | 'fertilizer' | 'pesticide' | 'shovel' | 'hand'

const props = defineProps<{
  snapshot?: PlayerSnapshot
  cropCatalog: CropCatalogEntryView[]
  connected: boolean
  busyAction?: FarmActionRequest
  actionMessage: string
  actionError: string
  nowMs: bigint
}>()

const emit = defineEmits<{
  action: [request: FarmActionRequest]
  openShop: []
  reloadCatalog: []
}>()

const selectedTool = ref<FarmTool>('hand')
const selectedSeedCropId = ref(0)
const localMessage = ref('')

const plots = computed(() => [...(props.snapshot?.plots ?? [])].sort((a, b) => a.plotId - b.plotId))
const inventory = computed(() => {
  const quantities = new Map<number, number>()
  for (const item of props.snapshot?.inventory ?? []) quantities.set(item.itemId, item.quantity)
  return quantities
})
const fertilizerQuantity = computed(() => inventory.value.get(1) ?? 0)
const shopSeedCrops = computed(() => props.cropCatalog.filter((crop) => crop.seedShopEntryId > 0))
const seedCrops = computed(() => shopSeedCrops.value.filter((crop) => seedQuantityOf(crop) > 0))
const selectedSeed = computed(() =>
  seedCrops.value.find((crop) => crop.cropId === selectedSeedCropId.value),
)
const selectedSeedQuantity = computed(() =>
  selectedSeed.value ? seedQuantityOf(selectedSeed.value) : 0,
)
const tools = computed<Array<{ id: FarmTool; label: string; icon: string; quantity?: number }>>(
  () => [
    { id: 'hand', label: '手', icon: handTool },
    { id: 'shovel', label: '铲子', icon: shovelTool },
    { id: 'pesticide', label: '杀虫剂', icon: fertilizerTool },
    { id: 'fertilizer', label: '肥料', icon: fertilizerTool, quantity: fertilizerQuantity.value },
  ],
)
const currentToolLabel = computed(() =>
  selectedTool.value === 'seed'
    ? `${selectedSeed.value?.name ?? '作物'}种子`
    : tools.value.find((tool) => tool.id === selectedTool.value)?.label ?? '手',
)

watch(
  seedCrops,
  (crops) => {
    if (crops.length === 0) {
      selectedSeedCropId.value = 0
    } else if (!crops.some((crop) => crop.cropId === selectedSeedCropId.value)) {
      selectedSeedCropId.value = crops[0].cropId
    }
  },
  { immediate: true },
)

function cropNameById(cropId: number): string {
  return props.cropCatalog.find((crop) => crop.cropId === cropId)?.name || `作物#${cropId}`
}

function formatDuration(input: bigint): string {
  const seconds = Number(input)
  if (!Number.isFinite(seconds) || seconds <= 0) return '即时'
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  const remainder = seconds % 60
  return remainder ? `${minutes} 分 ${remainder} 秒` : `${minutes} 分`
}

function formatCountdown(seconds: number): string {
  const safe = Math.max(0, seconds)
  return `${String(Math.floor(safe / 60)).padStart(2, '0')}:${String(safe % 60).padStart(2, '0')}`
}

function seedQuantityOf(crop: CropCatalogEntryView): number {
  return inventory.value.get(crop.seedItemId) ?? 0
}

function presentation(plot: PlotView) {
  const name = plot.cropId ? cropNameById(plot.cropId) : ''
  switch (plot.plotState) {
    case PlotState.GROWING:
      return {
        label: plot.pestEffect ? `${name}成长中 · 有虫` : `${name}成长中`,
        base: plotGrowing,
        crop: cropGrowing,
      }
    case PlotState.MATURE:
      return { label: `${name}已成熟`, base: plotMature, crop: matureCropSprite(plot.cropId) }
    case PlotState.NEED_CLEANUP:
      return { label: `${name || '作物'}待清理`, base: plotCleanup, crop: undefined }
    default:
      return { label: '空地', base: plotEmpty, crop: undefined }
  }
}

function estimatedSeconds(plot: PlotView): number {
  if (!plot.estimatedMatureAtMs || plot.estimatedMatureAtMs <= props.nowMs) return 0
  return Number((plot.estimatedMatureAtMs - props.nowMs + 999n) / 1000n)
}

function plotMeta(plot: PlotView): string {
  if (plot.plotState === PlotState.GROWING) {
    const seconds = estimatedSeconds(plot)
    const parts = [
      seconds ? `成熟倒计时：${formatCountdown(seconds)}` : '等待服务器确认成熟',
    ]
    if (plot.pestEffect) parts.unshift('有害虫')
    if (selectedTool.value === 'pesticide' && plot.pestEffect) parts.push('点击杀虫')
    return parts.join(' · ')
  }
  if (plot.plotState === PlotState.MATURE) return `可收获 ${plot.harvestableQuantity} 个`
  if (plot.plotState === PlotState.NEED_CLEANUP) return '收获完成，等待清理'
  return selectedTool.value === 'seed' && selectedSeed.value
    ? `可种植${selectedSeed.value.name}`
    : '空地可种植'
}

function targetAction(plot: PlotView): FarmActionRequest | undefined {
  switch (selectedTool.value) {
    case 'seed':
      if (plot.plotState === PlotState.EMPTY && selectedSeed.value && selectedSeedQuantity.value > 0) {
        return {
          action: 'plant',
          plotId: plot.plotId,
          seedItemId: selectedSeed.value.seedItemId,
        }
      }
      localMessage.value = plot.plotState !== PlotState.EMPTY
        ? '种子只能用于空地。'
        : selectedSeed.value ? '仓库里没有所选种子。' : '仓库里没有可用种子。'
      return undefined
    case 'fertilizer':
      if (plot.plotState === PlotState.GROWING && !plot.fertilizerEffect && fertilizerQuantity.value > 0) {
        return { action: 'fertilize', plotId: plot.plotId }
      }
      localMessage.value = plot.plotState !== PlotState.GROWING
        ? '肥料只能用于成长中的作物。'
        : plot.fertilizerEffect ? '该地块已有肥料效果。' : '仓库里没有肥料。'
      return undefined
    case 'pesticide':
      if (plot.plotState === PlotState.GROWING && plot.pestEffect) {
        return { action: 'catch-pest', plotId: plot.plotId }
      }
      localMessage.value = plot.plotState !== PlotState.GROWING
        ? '杀虫剂只能用于成长中的作物。'
        : '这块地没有害虫。'
      return undefined
    case 'hand':
      if (plot.plotState === PlotState.MATURE) return { action: 'harvest', plotId: plot.plotId }
      localMessage.value = '还不能收获。'
      return undefined
    case 'shovel':
      if (plot.plotState === PlotState.NEED_CLEANUP) return { action: 'clean', plotId: plot.plotId }
      localMessage.value = '还不能清理。'
      return undefined
  }
}

function isValidTarget(plot: PlotView): boolean {
  switch (selectedTool.value) {
    case 'seed':
      return plot.plotState === PlotState.EMPTY && selectedSeedQuantity.value > 0
    case 'fertilizer':
      return plot.plotState === PlotState.GROWING && !plot.fertilizerEffect && fertilizerQuantity.value > 0
    case 'pesticide':
      return plot.plotState === PlotState.GROWING && Boolean(plot.pestEffect)
    case 'hand':
      return plot.plotState === PlotState.MATURE
    case 'shovel':
      return plot.plotState === PlotState.NEED_CLEANUP
  }
}

function clickPlot(plot: PlotView): void {
  if (!props.connected || props.busyAction) {
    localMessage.value = props.connected ? '上一项操作仍在处理中。' : '实时连接已断开。'
    return
  }
  const request = targetAction(plot)
  if (request) {
    localMessage.value = ''
    emit('action', request)
  }
}

function selectSeed(crop: CropCatalogEntryView): void {
  selectedSeedCropId.value = crop.cropId
  selectedTool.value = 'seed'
  localMessage.value = ''
}
</script>

<template>
  <section class="farm-dashboard" aria-label="我的农场">
    <header class="farm-toolbar">
      <div>
        <p class="eyebrow">PLAYER FARM · {{ plots.length }} AUTHORITATIVE PLOTS</p>
        <h2>我的农场</h2>
      </div>
      <span class="state-pill">当前工具：{{ currentToolLabel }}</span>
    </header>

    <p v-if="actionError" class="action-notice error-banner" role="alert">{{ actionError }}</p>
    <p v-else-if="localMessage" class="action-notice tool-feedback" role="status">{{ localMessage }}</p>
    <p v-else-if="actionMessage" class="action-notice success-banner" role="status">{{ actionMessage }}</p>

    <div v-if="plots.length" class="plots-grid" :data-tool="selectedTool">
      <button
        v-for="plot in plots"
        :key="plot.plotId"
        type="button"
        class="plot-tile"
        :class="{
          busy: busyAction?.plotId === plot.plotId,
          valid: !busyAction && connected && isValidTarget(plot),
          invalid: !busyAction && connected && !isValidTarget(plot),
        }"
        :aria-label="`地块 ${plot.plotId}，${presentation(plot).label}`"
        @click="clickPlot(plot)"
      >
        <span class="plot-number">
          PLOT {{ String(plot.plotId).padStart(2, '0') }}
          <em v-if="plot.pestEffect" class="pest-badge">有虫</em>
        </span>
        <span class="plot-stage" :data-state="plot.plotState">
          <img class="plot-base pixel-art" :src="presentation(plot).base" alt="" />
          <img v-if="presentation(plot).crop" class="plot-crop pixel-art" :src="presentation(plot).crop" alt="" />
          <img v-if="plot.fertilizerEffect" class="plot-effect pixel-art" :src="effectIcon" alt="肥料效果" />
        </span>
        <span class="plot-caption">
          <strong>{{ presentation(plot).label }}</strong>
          <small>{{ plotMeta(plot) }}</small>
        </span>
        <span v-if="busyAction?.plotId === plot.plotId" class="plot-busy">处理中…</span>
      </button>
    </div>
    <p v-else class="empty-state">服务器快照中没有地块。</p>

    <div class="farm-bars">
      <nav class="farm-bar" aria-label="工具栏">
        <span class="farm-bar__label">工具</span>
        <div class="farm-bar__items">
          <button
            v-for="tool in tools"
            :key="tool.id"
            type="button"
            class="bar-chip"
            :class="{ selected: selectedTool === tool.id }"
            :aria-pressed="selectedTool === tool.id"
            @click="selectedTool = tool.id; localMessage = ''"
          >
            <img class="pixel-art" :src="tool.icon" alt="" />
            <span>{{ tool.label }}</span>
            <small v-if="tool.quantity !== undefined">×{{ tool.quantity }}</small>
          </button>
        </div>
      </nav>

      <nav class="farm-bar" aria-label="种子栏">
        <span class="farm-bar__label">种子</span>
        <div class="farm-bar__items">
          <button
            v-for="crop in seedCrops"
            :key="crop.cropId"
            type="button"
            class="bar-chip seed-chip"
            :class="{ selected: selectedTool === 'seed' && selectedSeedCropId === crop.cropId }"
            :aria-pressed="selectedTool === 'seed' && selectedSeedCropId === crop.cropId"
            :title="`${crop.name} · ${formatDuration(crop.maturitySeconds)}成熟`"
            @click="selectSeed(crop)"
          >
            <img class="pixel-art" :src="seedIcon" alt="" />
            <span>{{ crop.name }}</span>
            <small>×{{ seedQuantityOf(crop) }}</small>
          </button>
          <button v-if="shopSeedCrops.length === 0" type="button" class="bar-chip" @click="emit('reloadCatalog')">
            目录未加载，重试
          </button>
          <span v-else-if="seedCrops.length === 0" class="farm-bar__empty">仓库里还没有种子</span>
        </div>
        <button type="button" class="farm-bar__shop" @click="emit('openShop')">去商店</button>
      </nav>
    </div>
  </section>
</template>

<style scoped>
.farm-bars {
  position: sticky;
  bottom: 0.5rem;
  display: grid;
  gap: 0.5rem;
  z-index: 8;
}
.farm-bar {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.5rem 0.7rem;
  border: 2px solid #8b6c42;
  border-radius: 0.9rem;
  background: #fff8dc;
  box-shadow: 0 0.4rem 1rem rgb(44 58 34 / 16%);
}
.farm-bar__label {
  flex: none;
  color: #6b7c54;
  font-size: 0.68rem;
  font-weight: 800;
  letter-spacing: 0.1em;
}
.farm-bar__items {
  display: flex;
  flex: 1;
  gap: 0.4rem;
  min-width: 0;
  overflow-x: auto;
}
.bar-chip {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 0.3rem;
  min-height: 2.6rem;
  padding: 0.3rem 0.6rem;
  font-size: 0.78rem;
  font-weight: 700;
  white-space: nowrap;
}
.bar-chip img { width: 1.4rem; height: 1.4rem; }
.bar-chip small, .farm-bar__empty { color: #6b745e; font-size: 0.68rem; }
.bar-chip.selected {
  border-color: #31552d;
  background: #dfecc2;
  box-shadow: 0 0 0 3px rgb(49 85 45 / 15%);
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
.farm-bar__shop { flex: none; min-height: 2.2rem; padding: 0.3rem 0.6rem; }
@media (max-width: 440px) {
  .farm-bar { align-items: stretch; flex-wrap: wrap; }
  .farm-bar__items { order: 3; flex-basis: 100%; }
  .farm-bar__shop { margin-left: auto; }
}
</style>
