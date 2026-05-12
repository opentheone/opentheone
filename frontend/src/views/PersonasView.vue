<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "@/api";

interface Persona {
  id: string;
  name: string;
  description: string;
  speaking_style: string;
  is_active: boolean;
  proactive_cron: string;
}

const items = ref<Persona[]>([]);
const showForm = ref(false);
const form = ref({
  name: "",
  description: "",
  system_prompt: "",
  greeting: "",
  speaking_style: "",
  proactive_cron: "",
  proactive_prompt: "",
});
const err = ref("");
const busy = ref(false);

async function refresh() {
  const r = await api<{ items: Persona[] }>("/api/persona/list");
  items.value = r.items || [];
}

async function create() {
  busy.value = true;
  err.value = "";
  try {
    await api("/api/persona/create", { ...form.value });
    showForm.value = false;
    form.value = {
      name: "",
      description: "",
      system_prompt: "",
      greeting: "",
      speaking_style: "",
      proactive_cron: "",
      proactive_prompt: "",
    };
    await refresh();
  } catch (e: any) {
    err.value = e?.message || String(e);
  } finally {
    busy.value = false;
  }
}

async function activate(p: Persona) {
  await api("/api/persona/activate", { id: p.id });
  await refresh();
}

async function remove(p: Persona) {
  if (!confirm(`确认删除角色「${p.name}」？\n\n这会一并删除该角色名下所有微信绑定、对话历史、长期记忆和已下载的图片/语音文件，且不可恢复。`)) return;
  await api("/api/persona/delete", { id: p.id });
  await refresh();
}

onMounted(refresh);
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto">
    <div class="flex items-baseline justify-between mb-6">
      <h1 class="text-2xl font-semibold">角色</h1>
      <button class="btn-primary" @click="showForm = !showForm">
        {{ showForm ? "取消" : "新建角色" }}
      </button>
    </div>

    <div v-if="showForm" class="card p-6 mb-6 space-y-4">
      <div>
        <label class="label">名字</label>
        <input v-model="form.name" class="input mt-1" placeholder="比如：小棠" />
      </div>
      <div>
        <label class="label">一句话简介</label>
        <input v-model="form.description" class="input mt-1" placeholder="一个温柔、耐心、稍微有点话痨的女孩子" />
      </div>
      <div>
        <label class="label">完整人设（system prompt）</label>
        <textarea
          v-model="form.system_prompt"
          rows="6"
          class="textarea mt-1"
          placeholder="你是 ___，性别 ___，年龄 ___，背景 ___，喜欢 ___，对话时 ___。"
        />
      </div>
      <div>
        <label class="label">说话风格</label>
        <input
          v-model="form.speaking_style"
          class="input mt-1"
          placeholder="短句、轻声调侃、偶尔用 emoji"
        />
      </div>
      <div>
        <label class="label">开场白（首次主动消息）</label>
        <input
          v-model="form.greeting"
          class="input mt-1"
          placeholder="嗨～是我呀，找到你的微信啦"
        />
      </div>
      <div>
        <label class="label">主动消息 cron（可选，5 段标准 cron）</label>
        <input
          v-model="form.proactive_cron"
          class="input mt-1"
          placeholder="0 9 * * *  // 每天早 9 点找你聊一句"
        />
      </div>
      <div>
        <label class="label">主动消息 prompt（可选）</label>
        <textarea
          v-model="form.proactive_prompt"
          rows="3"
          class="textarea mt-1"
          placeholder="用你的口吻，主动发一句早安、关心一下对方"
        />
      </div>

      <div v-if="err" class="text-rose-400 text-sm">{{ err }}</div>

      <div class="flex justify-end gap-2">
        <button class="btn-ghost" @click="showForm = false">取消</button>
        <button class="btn-primary" :disabled="busy" @click="create">创建</button>
      </div>
    </div>

    <div v-if="items.length === 0" class="card p-12 text-center text-ink-400">
      还没有角色。点击右上角「新建角色」开始吧。
    </div>

    <div v-else class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <div v-for="p in items" :key="p.id" class="card p-5">
        <div class="flex items-start justify-between">
          <div>
            <div class="flex items-center gap-2">
              <div class="text-base font-semibold">{{ p.name }}</div>
              <span v-if="p.is_active" class="badge-active">激活</span>
            </div>
            <div class="text-xs text-ink-400 mt-1">{{ p.description || "—" }}</div>
          </div>
        </div>

        <div class="text-xs text-ink-500 mt-3 space-y-1">
          <div v-if="p.speaking_style"><span class="text-ink-400">风格：</span>{{ p.speaking_style }}</div>
          <div v-if="p.proactive_cron"><span class="text-ink-400">主动：</span>{{ p.proactive_cron }}</div>
        </div>

        <div class="mt-4 flex flex-wrap gap-2">
          <RouterLink :to="{ name: 'persona-detail', params: { id: p.id } }" class="btn-ghost">
            详情 / 绑定
          </RouterLink>
          <button v-if="!p.is_active" class="btn-primary" @click="activate(p)">
            设为唯一
          </button>
          <button class="btn-ghost ml-auto" @click="remove(p)">
            删除
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
