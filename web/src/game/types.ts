export interface Plot { plot_id: number; status: 'EMPTY' | 'GROWING' | 'MATURE' | 'NEED_CLEANUP'; planted_at_ms: number; mature_at_ms: number; fertilized: boolean }
export interface Task { action: string; label: string; current: number; target: number }
export interface Mail { mail_id: string; title: string; content: string; is_read: boolean; created_at_ms: number }
export interface Snapshot { player_id: string; state_version: string; coins: number; seeds: number; fertilizer: number; crops: number; plots: Plot[]; chapter: number; tasks: Task[] }
export interface Config { seed_price: number; fertilizer_price: number; crop_price: number; growth_seconds: number; fertilizer_seconds: number; yield: number; capacity: number }
export interface Reply { type: 'response'; request_id?: string; action?: string; code: string; message?: string; server_time_ms: number; token?: string; player_id?: string; snapshot?: Snapshot; config?: Config; mails?: Mail[] }
export interface Command { request_id: string; action: string; data: { plot_id?: number; quantity?: number; token?: string; mail_id?: string } }
