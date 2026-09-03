<script setup lang="ts">
import { ref } from 'vue'
import type { FriendView } from '../gen/classicfarm/v1/ws/ws_pb'

defineProps<{
  friends: FriendView[]
  connected: boolean
  busy: boolean
  refreshBusy: boolean
  refreshError: string
  generatedCode: string
  message: string
  error: string
  visitingOwnerId?: bigint
}>()

const emit = defineEmits<{
  refresh: []
  generate: []
  redeem: [code: string]
  enter: [friend: FriendView]
}>()

const redeemInput = ref('')
const copyMessage = ref('')

function submitRedeem(): void {
  const code = redeemInput.value.trim()
  if (!code) {
    return
  }
  emit('redeem', code)
}

async function copyCode(code: string): Promise<void> {
  if (!code) return
  try {
    await navigator.clipboard.writeText(code)
    copyMessage.value = '好友码已复制'
  } catch {
    copyMessage.value = '复制失败，请手动选择好友码'
  }
}
</script>

<template>
  <div class="friends-panel">
    <section class="friends-panel__code">
      <div class="friends-panel__row">
        <button
          class="primary"
          type="button"
          :disabled="!connected || busy"
          @click="emit('generate')"
        >
          {{ busy ? '处理中…' : '生成好友码' }}
        </button>
        <button type="button" :disabled="!connected || refreshBusy" @click="emit('refresh')">
          {{ refreshBusy ? '刷新中…' : '刷新列表' }}
        </button>
      </div>
      <p v-if="refreshError" class="error-banner" role="alert">{{ refreshError }}</p>

      <div v-if="generatedCode" class="generated-code">
        <span>我的好友码</span>
        <strong>{{ generatedCode }}</strong>
        <button type="button" @click="copyCode(generatedCode)">复制</button>
      </div>
      <p v-if="copyMessage" class="success-banner">{{ copyMessage }}</p>

      <form class="redeem-form" @submit.prevent="submitRedeem">
        <input
          v-model="redeemInput"
          placeholder="输入 32 位好友码"
          maxlength="32"
          spellcheck="false"
          autocomplete="off"
          :disabled="!connected || busy"
        />
        <button type="submit" :disabled="!connected || !redeemInput.trim() || busy">
          兑换
        </button>
      </form>

      <p v-if="message" class="success-banner">{{ message }}</p>
      <p v-if="error" class="error-banner">{{ error }}</p>
    </section>

    <div class="friends-panel__heading">
      <div>
        <span class="panel-kicker">MY FRIENDS</span>
        <h3>好友列表（{{ friends.length }}）</h3>
      </div>
    </div>
    <ul v-if="friends.length" class="friends-list">
      <li v-for="friend in friends" :key="friend.playerId.toString()">
        <span class="friend-avatar">{{ (friend.accountName || '?').slice(0, 1).toUpperCase() }}</span>
        <div>
          <strong>{{ friend.accountName || '未命名玩家' }}</strong>
          <small>player_id {{ friend.playerId.toString() }}</small>
        </div>
        <button
          type="button"
          :disabled="!connected || busy || visitingOwnerId === friend.playerId"
          @click="emit('enter', friend)"
        >
          {{ visitingOwnerId === friend.playerId ? '访问中' : '进入农场' }}
        </button>
      </li>
    </ul>
    <p v-else class="empty-state">暂无好友，生成好友码分享给另一个账号吧。</p>
  </div>
</template>

<style scoped>
.friends-panel,
.friends-panel__code {
  display: grid;
  gap: 0.75rem;
}

.friends-panel__row,
.generated-code,
.redeem-form,
.friends-panel__heading,
.friends-list li {
  display: flex;
  align-items: center;
  gap: 0.55rem;
}

.friends-panel__row button,
.redeem-form button,
.friends-list button {
  min-height: 2.5rem;
  padding: 0.4rem 0.8rem;
  font-size: 0.82rem;
}

.generated-code {
  flex-wrap: wrap;
  padding: 0.75rem;
  border: 1px solid #c3b48e;
  border-radius: 0.7rem;
  background: #fffdf2;
}

.generated-code span {
  width: 100%;
  color: #6b745e;
  font-size: 0.7rem;
}

.generated-code strong {
  min-width: 0;
  flex: 1;
  color: #31552d;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 0.82rem;
  overflow-wrap: anywhere;
}

.generated-code button {
  flex: none;
}

.redeem-form input {
  min-width: 0;
  flex: 1;
}

.friends-panel__heading {
  justify-content: space-between;
  margin-top: 0.25rem;
}

.friends-panel h3 {
  margin: 0;
  color: #24361f;
  font-size: 0.95rem;
}

.friends-list {
  display: grid;
  gap: 0.55rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.friends-list li {
  padding: 0.65rem;
  border: 1px solid #c3b48e;
  border-radius: 0.7rem;
  background: #fffdf2;
}

.friends-list li > div {
  display: grid;
  min-width: 0;
  flex: 1;
}

.friends-list small {
  color: #6d755f;
  font-size: 0.68rem;
}

.friends-list button {
  flex: none;
}

.friend-avatar {
  display: grid;
  width: 2.25rem;
  height: 2.25rem;
  flex: none;
  place-items: center;
  border-radius: 50%;
  background: #dfecc2;
  color: #31552d;
  font-weight: 850;
}

.friends-panel .success-banner,
.friends-panel .error-banner,
.friends-panel .empty-state {
  margin: 0;
}

@media (width <= 28rem) {
  .redeem-form {
    align-items: stretch;
    flex-direction: column;
  }

  .friends-list li {
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .friends-list button {
    width: 100%;
  }
}
</style>
