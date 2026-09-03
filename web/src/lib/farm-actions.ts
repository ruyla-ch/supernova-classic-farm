export type FarmAction =
  | 'buy'
  | 'buy-fertilizer'
  | 'plant'
  | 'fertilize'
  | 'catch-pest'
  | 'harvest'
  | 'sell'
  | 'claim'
  | 'clean'

export type FarmActionRequest = {
  action: FarmAction
  plotId?: number
  quantity?: number
  sellAll?: boolean
  shopEntryId?: number
  seedItemId?: number
  cropItemId?: number
  priceVersion?: bigint
}
