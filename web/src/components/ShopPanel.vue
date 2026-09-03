<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import type { CropCatalogEntryView, ShopEntryView } from '../gen/classicfarm/v1/ws/ws_pb'
import type { FarmActionRequest } from '../lib/farm-actions'

import seedIcon from '../../../frontend/src/assets/art/runtime/items/demo-seed.png'
import fertilizerIcon from '../../../frontend/src/assets/art/runtime/items/fertilizer-basic.png'

const props = defineProps<{
  shopEntries: ShopEntryView[]
  cropCatalog: CropCatalogEntryView[]
  inventory: Map<number, number>
  coinBalance?: bigint
  connected: boolean
  busyAction?: FarmActionRequest
}>()

const emit = defineEmits<{ action: [request: FarmActionRequest] }>()
const seedQuantities = reactive(new Map<number, number>())
const expandedCropIds = reactive(new Set<number>())
const fertilizerBuyQuantity = ref(1)

const seedCrops = computed(() => props.cropCatalog.filter((crop) => crop.seedShopEntryId > 0))
const fertilizerQuote = computed(() => props.shopEntries.find((entry) => entry.itemId === 1))
const fertilizerQuantity = computed(() => props.inventory.get(1) ?? 0)
const fertilizerTotal = computed(
  () => (fertilizerQuote.value?.unitPrice ?? 0n) * BigInt(fertilizerBuyQuantity.value),
)

function quoteFor(crop: CropCatalogEntryView): ShopEntryView | undefined {
  return props.shopEntries.find(
    (entry) =>
      entry.shopEntryId === crop.seedShopEntryId &&
      entry.itemId === crop.seedItemId &&
      entry.priceVersion === crop.seedPriceVersion,
  )
}
function quantityOf(crop: CropCatalogEntryView): number {
  return seedQuantities.get(crop.cropId) ?? 3
}
function setQuantity(crop: CropCatalogEntryView, quantity: number): void {
  seedQuantities.set(crop.cropId, Math.min(50, Math.max(1, Math.trunc(Number(quantity) || 1))))
}
function ownedOf(crop: CropCatalogEntryView): number {
  return props.inventory.get(crop.seedItemId) ?? 0
}
function totalOf(crop: CropCatalogEntryView): bigint {
  return (quoteFor(crop)?.unitPrice ?? crop.seedUnitPrice) * BigInt(quantityOf(crop))
}
function canBuy(crop: CropCatalogEntryView): boolean {
  const quote = quoteFor(crop)
  const quantity = quantityOf(crop)
  return Boolean(
    props.connected && quote?.enabled && props.coinBalance !== undefined &&
    quantity >= 1 && quantity <= 50 && ownedOf(crop) + quantity <= 300 &&
    props.coinBalance >= totalOf(crop),
  )
}
function toggle(crop: CropCatalogEntryView): void {
  if (!expandedCropIds.delete(crop.cropId)) expandedCropIds.add(crop.cropId)
}
function clampFertilizer(): void {
  fertilizerBuyQuantity.value = Math.min(
    50,
    Math.max(1, Math.trunc(Number(fertilizerBuyQuantity.value) || 1)),
  )
}
const canBuyFertilizer = computed(() => Boolean(
  props.connected && fertilizerQuote.value?.enabled && props.coinBalance !== undefined &&
  fertilizerBuyQuantity.value >= 1 && fertilizerBuyQuantity.value <= 50 &&
  fertilizerQuantity.value + fertilizerBuyQuantity.value <= 300 &&
  props.coinBalance >= fertilizerTotal.value,
))
</script>

<template>
  <div class="shop-panel-body">
    <div class="panel-heading"><h3>种子</h3><span class="price-tag">共 {{ seedCrops.length }} 种</span></div>
    <p v-if="seedCrops.length === 0" class="empty-state">种子目录尚未加载。</p>
    <ul class="seed-list">
      <li v-for="crop in seedCrops" :key="crop.cropId" class="seed-row">
        <button type="button" class="seed-summary" :aria-expanded="expandedCropIds.has(crop.cropId)" @click="toggle(crop)">
          <img class="item-icon pixel-art" :src="seedIcon" alt="" />
          <span class="shop-copy">
            <strong>{{ crop.name }}种子</strong>
            <small>{{ quoteFor(crop)?.unitPrice ?? crop.seedUnitPrice }} 金币 / 粒 · 持有 {{ ownedOf(crop) }}</small>
          </span>
          <span aria-hidden="true">{{ expandedCropIds.has(crop.cropId) ? '▾' : '▸' }}</span>
        </button>
        <div v-if="expandedCropIds.has(crop.cropId)" class="seed-detail">
          <small>{{ crop.maturitySeconds }} 秒成熟 · 产量 {{ crop.baseYield }}</small>
          <div class="quantity-row">
            <button type="button" :aria-label="`减少${crop.name}购买数量`" @click="setQuantity(crop, quantityOf(crop) - 1)">−</button>
            <input type="number" min="1" max="50" :value="quantityOf(crop)" :aria-label="`${crop.name}购买数量`" @change="setQuantity(crop, Number(($event.target as HTMLInputElement).value))" />
            <button type="button" :aria-label="`增加${crop.name}购买数量`" @click="setQuantity(crop, quantityOf(crop) + 1)">＋</button>
            <span>合计 {{ totalOf(crop) }} 金币</span>
            <button
              class="primary"
              type="button"
              :disabled="!canBuy(crop) || Boolean(busyAction)"
              @click="emit('action', {
                action: 'buy',
                quantity: quantityOf(crop),
                shopEntryId: crop.seedShopEntryId,
                seedItemId: crop.seedItemId,
                priceVersion: crop.seedPriceVersion,
              })"
            >
              {{ busyAction?.action === 'buy' && busyAction.seedItemId === crop.seedItemId ? '购买中…' : `购买 ${quantityOf(crop)} 粒` }}
            </button>
          </div>
        </div>
      </li>
    </ul>

    <div class="panel-heading"><h3>肥料</h3><span class="price-tag">{{ fertilizerQuote?.unitPrice ?? '—' }} 金币 / 袋</span></div>
    <div class="shop-item">
      <img class="item-icon pixel-art" :src="fertilizerIcon" alt="" />
      <div><strong>基础肥料</strong><small>当前持有 {{ fertilizerQuantity }} 袋</small></div>
    </div>
    <div class="quantity-row">
      <button type="button" aria-label="减少肥料数量" @click="fertilizerBuyQuantity--; clampFertilizer()">−</button>
      <input v-model.number="fertilizerBuyQuantity" type="number" min="1" max="50" aria-label="肥料购买数量" @change="clampFertilizer" />
      <button type="button" aria-label="增加肥料数量" @click="fertilizerBuyQuantity++; clampFertilizer()">＋</button>
      <span>合计 {{ fertilizerTotal }} 金币</span>
      <button class="primary" type="button" :disabled="!canBuyFertilizer || Boolean(busyAction)" @click="emit('action', { action: 'buy-fertilizer', quantity: fertilizerBuyQuantity })">
        {{ busyAction?.action === 'buy-fertilizer' ? '购买中…' : `购买 ${fertilizerBuyQuantity} 袋` }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.shop-panel-body { display: grid; gap: 0.7rem; }
.shop-panel-body h3 { margin: 0; color: #24361f; font-size: 0.95rem; }
.seed-list { display: grid; gap: 0.35rem; margin: 0; padding: 0; list-style: none; }
.seed-row { border: 1px solid #c3b48e; border-radius: 0.6rem; background: #fffdf4; }
.seed-summary { display: flex; align-items: center; gap: 0.55rem; width: 100%; padding: 0.45rem; border: 0; background: transparent; text-align: left; }
.seed-summary .shop-copy { display: grid; flex: 1; }
.seed-summary small, .seed-detail small { color: #6d755f; font-size: 0.68rem; }
.seed-detail { display: grid; gap: 0.35rem; padding: 0 0.55rem 0.55rem; }
</style>
