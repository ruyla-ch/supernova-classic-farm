export type PanelId = 'account' | 'shop' | 'tasks' | 'inventory' | 'friends'

export const panelTitles: Record<PanelId, string> = {
  account: '账号',
  shop: '商店',
  tasks: '任务',
  inventory: '仓库',
  friends: '好友',
}

export const panelKickers: Record<PanelId, string> = {
  account: 'SESSION & DIAGNOSTICS',
  shop: 'SHOP',
  tasks: 'CHAPTER',
  inventory: 'BARN',
  friends: 'SOCIAL · MYSQL',
}

export const panelOrder: PanelId[] = ['account', 'shop', 'tasks', 'inventory', 'friends']
