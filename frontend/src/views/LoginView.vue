<script setup lang="ts">
import { ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useAuthStore } from "@/store/auth";

const auth = useAuthStore();
const router = useRouter();
const route = useRoute();

const mode = ref<"login" | "register">("login");
const username = ref("");
const password = ref("");
const displayName = ref("");
const busy = ref(false);
const err = ref("");

async function submit() {
  busy.value = true;
  err.value = "";
  try {
    if (mode.value === "login") {
      await auth.login(username.value, password.value);
    } else {
      await auth.register(username.value, password.value, displayName.value);
    }
    const next =
      (route.query.next as string) || (route.query.redirect as string) || "";
    if (next && next.startsWith("/") && !next.startsWith("/login")) {
      router.push(next);
    } else {
      router.push({ name: "dashboard" });
    }
  } catch (e: any) {
    err.value = e?.message || String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center px-4">
    <div class="card p-8 w-full max-w-md">
      <div class="mb-6">
        <div class="text-2xl font-semibold">
          {{ mode === "login" ? "登录到 OpenTheOne" : "创建账号" }}
        </div>
        <div class="text-sm text-ink-400 mt-1">
          你的唯一，与最重要的 TA。
        </div>
      </div>

      <form class="space-y-4" @submit.prevent="submit">
        <div>
          <label class="label">用户名</label>
          <input v-model="username" class="input mt-1" placeholder="用户名" required autocomplete="username" />
        </div>
        <div v-if="mode === 'register'">
          <label class="label">昵称（可选）</label>
          <input v-model="displayName" class="input mt-1" placeholder="昵称" />
        </div>
        <div>
          <label class="label">密码</label>
          <input
            v-model="password"
            type="password"
            class="input mt-1"
            placeholder="密码"
            required
            :autocomplete="mode === 'register' ? 'new-password' : 'current-password'"
          />
        </div>

        <div v-if="err" class="text-rose-400 text-sm">{{ err }}</div>

        <button type="submit" class="btn-primary w-full" :disabled="busy">
          {{ busy ? "请稍候…" : mode === "login" ? "登录" : "注册并登录" }}
        </button>
      </form>

      <div class="mt-6 text-sm text-ink-400 text-center">
        <button
          class="hover:text-ink-100 underline"
          @click="mode = mode === 'login' ? 'register' : 'login'"
        >
          {{ mode === "login" ? "没有账号？立即注册" : "已有账号？返回登录" }}
        </button>
      </div>
    </div>
  </div>
</template>
