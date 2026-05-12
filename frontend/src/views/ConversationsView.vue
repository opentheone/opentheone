<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "@/api";

interface Conversation {
  id: string;
  binding_id: string;
  ilink_user_id: string;
  nickname: string;
  last_message_at: string;
}

const items = ref<Conversation[]>([]);

async function refresh() {
  const r = await api<{ items: Conversation[] }>("/api/conversation/list", { limit: 100 });
  items.value = r.items || [];
}

onMounted(refresh);
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto">
    <h1 class="text-2xl font-semibold mb-6">对话</h1>
    <div v-if="items.length === 0" class="card p-12 text-center text-ink-400">
      没有对话。激活角色 + 扫码绑定微信后，对方在微信里给 TA 发消息就会出现在这里。
    </div>
    <div v-else class="card divide-y divide-ink-800">
      <RouterLink
        v-for="c in items"
        :key="c.id"
        :to="{ name: 'conversation-detail', params: { id: c.id } }"
        class="px-5 py-4 flex items-center justify-between hover:bg-ink-800/40"
      >
        <div>
          <div class="font-medium">{{ c.nickname || c.ilink_user_id }}</div>
          <div class="text-xs text-ink-500">{{ c.ilink_user_id }}</div>
        </div>
        <div class="text-xs text-ink-500">{{ c.last_message_at }}</div>
      </RouterLink>
    </div>
  </div>
</template>
