<script setup lang="ts">
import { useAuthStore } from "@/store/auth";

const auth = useAuthStore();
</script>

<template>
  <div v-if="!auth.ready" class="h-screen flex items-center justify-center text-ink-400">
    加载中…
  </div>
  <router-view v-else />
</template>
