<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "@/api";
import { useAuthStore } from "@/store/auth";

const auth = useAuthStore();

const displayName = ref("");
const profileMsg = ref<{ kind: "ok" | "err"; text: string } | null>(null);

const oldPassword = ref("");
const newPassword = ref("");
const newPasswordRepeat = ref("");
const passwordMsg = ref<{ kind: "ok" | "err"; text: string } | null>(null);

onMounted(() => {
  displayName.value = auth.user?.display_name ?? "";
});

async function saveProfile() {
  profileMsg.value = null;
  try {
    await api("/api/auth/update_profile", { display_name: displayName.value });
    if (auth.user) auth.user.display_name = displayName.value;
    profileMsg.value = { kind: "ok", text: "已保存" };
  } catch (e: any) {
    profileMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}

async function changePassword() {
  passwordMsg.value = null;
  if (newPassword.value.length < 6) {
    passwordMsg.value = { kind: "err", text: "新密码至少 6 个字符" };
    return;
  }
  if (newPassword.value !== newPasswordRepeat.value) {
    passwordMsg.value = { kind: "err", text: "两次输入的新密码不一致" };
    return;
  }
  try {
    await api("/api/auth/update_password", {
      old_password: oldPassword.value,
      new_password: newPassword.value,
    });
    oldPassword.value = "";
    newPassword.value = "";
    newPasswordRepeat.value = "";
    passwordMsg.value = { kind: "ok", text: "已更新密码" };
  } catch (e: any) {
    passwordMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}
</script>

<template>
  <div class="px-8 py-8 max-w-2xl mx-auto space-y-6">
    <h1 class="text-2xl font-semibold">设置</h1>

    <section class="card p-6 space-y-4">
      <div>
        <div class="text-sm text-ink-400">用户名</div>
        <div class="text-base">{{ auth.user?.username }}</div>
      </div>
      <div>
        <div class="text-sm text-ink-400">角色</div>
        <div class="text-base">{{ auth.user?.role }}</div>
      </div>
      <div>
        <label class="label">昵称</label>
        <input v-model="displayName" class="input mt-1" placeholder="昵称" />
      </div>
      <div class="flex items-center gap-3">
        <button class="btn-primary" @click="saveProfile">保存昵称</button>
        <span v-if="profileMsg" :class="profileMsg.kind === 'ok' ? 'text-green-400 text-sm' : 'text-red-400 text-sm'">
          {{ profileMsg.text }}
        </span>
      </div>
    </section>

    <section class="card p-6 space-y-4">
      <div class="text-lg font-medium">修改密码</div>
      <div>
        <label class="label">当前密码</label>
        <input v-model="oldPassword" type="password" class="input mt-1" autocomplete="current-password" />
      </div>
      <div>
        <label class="label">新密码（至少 6 位）</label>
        <input v-model="newPassword" type="password" class="input mt-1" autocomplete="new-password" />
      </div>
      <div>
        <label class="label">再次输入新密码</label>
        <input v-model="newPasswordRepeat" type="password" class="input mt-1" autocomplete="new-password" />
      </div>
      <div class="flex items-center gap-3">
        <button class="btn-primary" @click="changePassword">更新密码</button>
        <span v-if="passwordMsg" :class="passwordMsg.kind === 'ok' ? 'text-green-400 text-sm' : 'text-red-400 text-sm'">
          {{ passwordMsg.text }}
        </span>
      </div>
    </section>
  </div>
</template>
