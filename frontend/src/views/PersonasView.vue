<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { api } from "@/api";

interface Persona {
  id: string;
  name: string;
  avatar: string;
  description: string;
  speaking_style: string;
  is_active: boolean;
  proactive_cron: string;
}

interface Template {
  id: string;
  name: string;
  avatar: string;
  tagline: string;
  description: string;
  system_prompt: string;
  speaking_style: string;
  greeting: string;
  proactive_cron: string;
  proactive_prompt: string;
}

const router = useRouter();
const items = ref<Persona[]>([]);
const templates = ref<Template[]>([]);
const showForm = ref(false);
const selectedTemplate = ref<string>("");
const form = ref({
  name: "",
  avatar: "",
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

async function loadTemplates() {
  try {
    const r = await api<{ items: Template[] }>("/api/persona/templates");
    templates.value = r.items || [];
  } catch {
    templates.value = [];
  }
}

function applyTemplate(t: Template) {
  selectedTemplate.value = t.id;
  form.value = {
    name: t.name,
    avatar: t.avatar,
    description: t.description,
    system_prompt: t.system_prompt,
    greeting: t.greeting,
    speaking_style: t.speaking_style,
    proactive_cron: t.proactive_cron,
    proactive_prompt: t.proactive_prompt,
  };
}

function resetForm() {
  selectedTemplate.value = "";
  form.value = {
    name: "",
    avatar: "",
    description: "",
    system_prompt: "",
    greeting: "",
    speaking_style: "",
    proactive_cron: "",
    proactive_prompt: "",
  };
}

function openForm() {
  resetForm();
  showForm.value = true;
}

function closeForm() {
  showForm.value = false;
  resetForm();
}

async function create() {
  busy.value = true;
  err.value = "";
  try {
    const r = await api<{ id: string }>("/api/persona/create", { ...form.value });
    closeForm();
    await refresh();
    if (r?.id) {
      router.push({ name: "persona-detail", params: { id: r.id } });
    }
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
  if (
    !confirm(
      `确认删除角色「${p.name}」？\n\n这会一并删除该角色名下所有微信绑定、对话历史、长期记忆和已下载的图片/语音文件，且不可恢复。`,
    )
  )
    return;
  await api("/api/persona/delete", { id: p.id });
  await refresh();
}

onMounted(async () => {
  await Promise.all([refresh(), loadTemplates()]);
});
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto">
    <div class="flex items-baseline justify-between mb-6">
      <h1 class="text-2xl font-semibold">角色</h1>
      <button class="btn-primary" @click="showForm ? closeForm() : openForm()">
        {{ showForm ? "取消" : "新建角色" }}
      </button>
    </div>

    <div v-if="showForm" class="card p-6 mb-6 space-y-4">
      <div v-if="templates.length > 0">
        <div class="label mb-2">快速预置（点一下用整套人设）</div>
        <div class="grid grid-cols-2 md:grid-cols-4 gap-2">
          <button
            v-for="t in templates"
            :key="t.id"
            type="button"
            class="text-left rounded-lg border border-ink-700 bg-ink-900 hover:border-accent-500 px-3 py-2 transition"
            :class="selectedTemplate === t.id ? 'border-accent-500 ring-1 ring-accent-500/40' : ''"
            @click="applyTemplate(t)"
          >
            <div class="flex items-center gap-2">
              <span class="text-xl">{{ t.avatar }}</span>
              <span class="text-sm font-medium">{{ t.name }}</span>
            </div>
            <div class="text-[11px] text-ink-400 mt-1 leading-snug">{{ t.tagline }}</div>
          </button>
          <button
            type="button"
            class="text-left rounded-lg border border-ink-700 bg-ink-900 hover:border-accent-500 px-3 py-2 transition"
            :class="selectedTemplate === '' ? 'border-accent-500 ring-1 ring-accent-500/40' : ''"
            @click="resetForm"
          >
            <div class="flex items-center gap-2">
              <span class="text-xl">✏️</span>
              <span class="text-sm font-medium">从零开始</span>
            </div>
            <div class="text-[11px] text-ink-400 mt-1 leading-snug">完全自定义人设</div>
          </button>
        </div>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-[80px_1fr] gap-4">
        <div>
          <label class="label">头像</label>
          <input
            v-model="form.avatar"
            class="input mt-1 text-center text-2xl"
            placeholder="🌸"
            maxlength="4"
          />
        </div>
        <div>
          <label class="label">名字</label>
          <input v-model="form.name" class="input mt-1" placeholder="比如：小棠" />
        </div>
      </div>
      <div>
        <label class="label">一句话简介</label>
        <input
          v-model="form.description"
          class="input mt-1"
          placeholder="一个温柔、耐心、稍微有点话痨的女孩子"
        />
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
        <input v-model="form.greeting" class="input mt-1" placeholder="嗨～是我呀，找到你的微信啦" />
      </div>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label class="label">主动消息 cron（可选）</label>
          <input
            v-model="form.proactive_cron"
            class="input mt-1"
            placeholder="0 9 * * *  每天早 9 点"
          />
        </div>
        <div>
          <label class="label">主动消息 prompt（可选）</label>
          <input
            v-model="form.proactive_prompt"
            class="input mt-1"
            placeholder="用你的口吻，主动发一句早安"
          />
        </div>
      </div>

      <div v-if="err" class="text-rose-400 text-sm">{{ err }}</div>

      <div class="flex justify-end gap-2">
        <button class="btn-ghost" @click="closeForm">取消</button>
        <button class="btn-primary" :disabled="busy" @click="create">创建并进入详情</button>
      </div>
    </div>

    <div v-if="items.length === 0" class="card p-12 text-center text-ink-400">
      还没有角色。点击右上角「新建角色」开始吧，可以一键套用预置模板。
    </div>

    <div v-else class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <div v-for="p in items" :key="p.id" class="card p-5">
        <div class="flex items-start gap-3">
          <div
            class="w-12 h-12 rounded-xl bg-ink-800 grid place-items-center text-2xl flex-shrink-0"
          >
            {{ p.avatar || "🙂" }}
          </div>
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2">
              <div class="text-base font-semibold truncate">{{ p.name }}</div>
              <span v-if="p.is_active" class="badge-active">激活</span>
            </div>
            <div class="text-xs text-ink-400 mt-1 line-clamp-2">{{ p.description || "—" }}</div>
          </div>
        </div>

        <div class="text-xs text-ink-500 mt-3 space-y-1">
          <div v-if="p.speaking_style">
            <span class="text-ink-400">风格：</span>{{ p.speaking_style }}
          </div>
          <div v-if="p.proactive_cron">
            <span class="text-ink-400">主动：</span>{{ p.proactive_cron }}
          </div>
        </div>

        <div class="mt-4 flex flex-wrap gap-2">
          <RouterLink
            :to="{ name: 'persona-detail', params: { id: p.id } }"
            class="btn-ghost"
          >
            详情 / 绑定
          </RouterLink>
          <button v-if="!p.is_active" class="btn-primary" @click="activate(p)">
            设为唯一
          </button>
          <button class="btn-ghost ml-auto" @click="remove(p)">删除</button>
        </div>
      </div>
    </div>
  </div>
</template>
