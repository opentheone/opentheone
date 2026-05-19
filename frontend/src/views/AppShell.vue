<script setup lang="ts">
import { computed } from "vue";
import { RouterLink, useRouter } from "vue-router";
import { useAuthStore } from "@/store/auth";

const auth = useAuthStore();
const router = useRouter();

const nav = computed(() => {
  const base = [
    { to: "/", label: "概览" },
    { to: "/personas", label: "角色" },
    { to: "/conversations", label: "对话" },
    { to: "/llm", label: "模型" },
    { to: "/mcp", label: "MCP 工具" },
    { to: "/settings", label: "设置" },
  ];
  if (auth.user?.role === "admin") {
    base.push({ to: "/admin", label: "管理员" });
  }
  return base;
});

function logout() {
  auth.logout();
  router.push({ name: "login" });
}
</script>

<template>
  <div class="min-h-screen flex">
    <aside
      class="w-64 shrink-0 border-r border-ink-800 bg-ink-900 flex flex-col"
    >
      <div class="px-6 py-6 border-b border-ink-800">
        <div class="flex items-center gap-2">
          <div
            class="w-9 h-9 rounded-xl bg-gradient-to-br from-accent-500 to-fuchsia-500 grid place-items-center text-white font-bold"
          >
            ❶
          </div>
          <div>
            <div class="text-base font-semibold text-ink-100">OpenTheOne</div>
            <div class="text-xs text-ink-400">your only AI</div>
          </div>
        </div>
      </div>

      <nav class="px-3 py-4 flex-1 space-y-1">
        <RouterLink
          v-for="n in nav"
          :key="n.to"
          :to="n.to"
          class="block rounded-lg px-3 py-2 text-sm text-ink-300 hover:bg-ink-800 hover:text-ink-100"
          active-class="bg-ink-800 text-ink-100"
        >
          {{ n.label }}
        </RouterLink>
      </nav>

      <div class="px-3 py-4 border-t border-ink-800">
        <div class="px-3 py-2 text-xs text-ink-400">
          <div class="truncate">{{ auth.user?.display_name || auth.user?.username }}</div>
          <div class="text-[10px] text-ink-500">{{ auth.user?.role }}</div>
        </div>
        <button class="btn-ghost w-full mt-1" @click="logout">退出登录</button>
      </div>
    </aside>

    <main class="flex-1 overflow-y-auto">
      <RouterView />
    </main>
  </div>
</template>
