<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { api } from "@/api";
import { useAuthStore } from "@/store/auth";

const auth = useAuthStore();
const router = useRouter();

interface AdminUser {
  id: string;
  username: string;
  display_name: string;
  role: string;
  created_at: string;
}

const users = ref<AdminUser[]>([]);
const allowRegister = ref(true);
const settingsMsg = ref<{ kind: "ok" | "err"; text: string } | null>(null);
const usersMsg = ref<{ kind: "ok" | "err"; text: string } | null>(null);

onMounted(async () => {
  if (auth.user?.role !== "admin") {
    router.replace("/");
    return;
  }
  await loadUsers();
  await loadSettings();
});

async function loadUsers() {
  try {
    const r = await api<{ items: AdminUser[] }>("/api/admin/users");
    users.value = r.items || [];
  } catch (e: any) {
    usersMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}

async function loadSettings() {
  try {
    const r = await api<{ allow_register: boolean }>("/api/admin/settings");
    allowRegister.value = !!r.allow_register;
  } catch (e: any) {
    settingsMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}

async function toggleAllowRegister() {
  settingsMsg.value = null;
  try {
    const r = await api<{ allow_register: boolean }>(
      "/api/admin/settings/update",
      { allow_register: !allowRegister.value },
    );
    allowRegister.value = !!r.allow_register;
    settingsMsg.value = { kind: "ok", text: "已保存" };
  } catch (e: any) {
    settingsMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}

async function setRole(u: AdminUser, role: "admin" | "user") {
  if (u.role === role) return;
  if (!confirm(`确认把 ${u.username} 设置为「${role}」？`)) return;
  try {
    await api("/api/admin/users/set_role", { user_id: u.id, role });
    await loadUsers();
  } catch (e: any) {
    usersMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}

async function resetPassword(u: AdminUser) {
  const pw = prompt(`为 ${u.username} 设置新密码（至少 6 位）`);
  if (!pw) return;
  if (pw.length < 6) {
    usersMsg.value = { kind: "err", text: "密码至少 6 位" };
    return;
  }
  try {
    await api("/api/admin/users/reset_password", {
      user_id: u.id,
      new_password: pw,
    });
    usersMsg.value = { kind: "ok", text: `已重置 ${u.username} 的密码` };
  } catch (e: any) {
    usersMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}

async function deleteUser(u: AdminUser) {
  if (u.id === auth.user?.id) {
    usersMsg.value = { kind: "err", text: "不能删除自己" };
    return;
  }
  if (!confirm(`确认删除用户 ${u.username}？该用户的全部 persona、对话、记忆会一并清理，无法恢复。`)) return;
  try {
    await api("/api/admin/users/delete", { user_id: u.id });
    await loadUsers();
    usersMsg.value = { kind: "ok", text: "已删除" };
  } catch (e: any) {
    usersMsg.value = { kind: "err", text: e?.message || String(e) };
  }
}
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto space-y-6">
    <h1 class="text-2xl font-semibold">管理员面板</h1>

    <section class="card p-6 space-y-3">
      <div class="text-lg font-medium">全局设置</div>
      <div class="flex items-center justify-between">
        <div>
          <div class="text-sm">开放注册</div>
          <div class="text-xs text-ink-400">关闭后新用户无法通过 /login 注册（已有用户不受影响）</div>
        </div>
        <button
          class="btn-ghost"
          :class="allowRegister ? 'text-green-400' : 'text-red-400'"
          @click="toggleAllowRegister"
        >
          {{ allowRegister ? "已开启" : "已关闭" }}
        </button>
      </div>
      <div v-if="settingsMsg" :class="settingsMsg.kind === 'ok' ? 'text-green-400 text-sm' : 'text-red-400 text-sm'">
        {{ settingsMsg.text }}
      </div>
    </section>

    <section class="card divide-y divide-ink-800">
      <div class="px-5 py-3 flex items-center justify-between">
        <div class="text-lg font-medium">用户列表</div>
        <div v-if="usersMsg" :class="usersMsg.kind === 'ok' ? 'text-green-400 text-xs' : 'text-red-400 text-xs'">
          {{ usersMsg.text }}
        </div>
      </div>
      <div
        v-for="u in users"
        :key="u.id"
        class="px-5 py-4 flex items-center justify-between gap-4"
      >
        <div class="min-w-0">
          <div class="font-medium truncate">
            {{ u.display_name || u.username }}
            <span class="text-xs text-ink-400">({{ u.username }})</span>
            <span v-if="u.id === auth.user?.id" class="text-xs text-accent-400 ml-1">[我]</span>
          </div>
          <div class="text-xs text-ink-500">{{ u.role }} · {{ u.created_at }}</div>
        </div>
        <div class="flex flex-wrap gap-2">
          <button
            v-if="u.role !== 'admin'"
            class="btn-ghost text-xs"
            @click="setRole(u, 'admin')"
          >升为管理员</button>
          <button
            v-else
            class="btn-ghost text-xs"
            @click="setRole(u, 'user')"
          >降为用户</button>
          <button class="btn-ghost text-xs" @click="resetPassword(u)">重置密码</button>
          <button
            class="btn-ghost text-xs text-red-400 hover:text-red-300"
            :disabled="u.id === auth.user?.id"
            @click="deleteUser(u)"
          >删除</button>
        </div>
      </div>
    </section>
  </div>
</template>
