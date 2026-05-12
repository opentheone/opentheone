<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "@/api";

interface LLMConfig {
  id: string;
  name: string;
  base_url: string;
  chat_model: string;
  embedding_model: string;
  temperature: number;
  max_tokens: number;
  is_default: boolean;
  api_key_set: boolean;
  created_at: string;
}

interface Provider {
  id: string;
  name: string;
  base_url: string;
  chat_model: string;
  embedding_model: string;
  supports_embedding: boolean;
  signup_url: string;
  note: string;
}

const items = ref<LLMConfig[]>([]);
const providers = ref<Provider[]>([]);
const selectedPreset = ref<string>("deepseek");

const emptyForm = () => ({
  name: "DeepSeek",
  base_url: "https://api.deepseek.com/v1",
  api_key: "",
  chat_model: "deepseek-v4-pro",
  embedding_model: "",
  temperature: 0.8,
  max_tokens: 1024,
  is_default: false,
});

const form = ref(emptyForm());
const editing = ref<string | null>(null);
const err = ref("");
const testing = ref<string | null>(null);
const testResults = ref<Record<string, { ok: boolean; msg: string }>>({});

async function refresh() {
  const r = await api<{ items: LLMConfig[] }>("/api/llm/list");
  items.value = r.items || [];
}

async function loadProviders() {
  try {
    const r = await api<{ items: Provider[] }>("/api/llm/providers");
    providers.value = r.items || [];
  } catch {
    providers.value = [];
  }
}

function applyPreset(p: Provider) {
  selectedPreset.value = p.id;
  form.value.name = p.name;
  form.value.base_url = p.base_url;
  form.value.chat_model = p.chat_model;
  form.value.embedding_model = p.embedding_model;
}

async function submit() {
  err.value = "";
  try {
    if (editing.value) {
      await api("/api/llm/update", { id: editing.value, ...form.value });
    } else {
      await api("/api/llm/create", { ...form.value });
    }
    reset();
    await refresh();
  } catch (e: any) {
    err.value = e?.message || String(e);
  }
}

function reset() {
  editing.value = null;
  selectedPreset.value = "deepseek";
  form.value = emptyForm();
}

function edit(c: LLMConfig) {
  editing.value = c.id;
  form.value = {
    name: c.name,
    base_url: c.base_url,
    api_key: "",
    chat_model: c.chat_model,
    embedding_model: c.embedding_model,
    temperature: c.temperature,
    max_tokens: c.max_tokens,
    is_default: c.is_default,
  };
  const matched = providers.value.find(
    (p) => p.base_url === c.base_url,
  );
  selectedPreset.value = matched?.id ?? "custom";
}

async function remove(c: LLMConfig) {
  if (!confirm(`删除「${c.name}」？`)) return;
  await api("/api/llm/delete", { id: c.id });
  await refresh();
}

async function test(c: LLMConfig) {
  if (!c.api_key_set) {
    testResults.value[c.id] = {
      ok: false,
      msg: "尚未配置 API Key，无法测试。",
    };
    return;
  }
  testing.value = c.id;
  testResults.value[c.id] = { ok: true, msg: "测试中…" };
  try {
    await api("/api/llm/test", { id: c.id });
    testResults.value[c.id] = { ok: true, msg: "连通正常" };
  } catch (e: any) {
    testResults.value[c.id] = {
      ok: false,
      msg: e?.message || String(e),
    };
  } finally {
    testing.value = null;
  }
}

onMounted(async () => {
  await Promise.all([refresh(), loadProviders()]);
});
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto">
    <h1 class="text-2xl font-semibold mb-6">模型配置</h1>

    <div class="card p-6 mb-6 space-y-4">
      <div class="flex items-baseline justify-between">
        <h2 class="text-lg font-medium">
          {{ editing ? "编辑模型" : "添加 OpenAI 兼容模型" }}
        </h2>
        <button v-if="editing" class="btn-ghost" @click="reset">取消编辑</button>
      </div>

      <div v-if="providers.length > 0">
        <div class="label mb-2">快速预置</div>
        <div class="flex flex-wrap gap-2">
          <button
            v-for="p in providers"
            :key="p.id"
            type="button"
            class="btn-ghost text-xs"
            :class="selectedPreset === p.id ? 'ring-1 ring-accent-500 text-accent-300' : ''"
            @click="applyPreset(p)"
          >
            {{ p.name }}
          </button>
          <button
            type="button"
            class="btn-ghost text-xs"
            :class="selectedPreset === 'custom' ? 'ring-1 ring-accent-500 text-accent-300' : ''"
            @click="selectedPreset = 'custom'"
          >
            自定义
          </button>
        </div>
        <div
          v-if="selectedPreset !== 'custom'"
          class="mt-2 text-xs text-ink-400"
        >
          <template v-for="p in providers" :key="p.id">
            <span v-if="p.id === selectedPreset">
              {{ p.note }}
              <a
                v-if="p.signup_url"
                :href="p.signup_url"
                target="_blank"
                class="text-accent-400 hover:underline ml-1"
              >获取 API Key →</a>
            </span>
          </template>
        </div>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label class="label">名字</label>
          <input v-model="form.name" class="input mt-1" placeholder="比如：DeepSeek 主账号" />
        </div>
        <div>
          <label class="label">Base URL</label>
          <input v-model="form.base_url" class="input mt-1" placeholder="https://api.deepseek.com/v1" />
        </div>
        <div class="md:col-span-2">
          <label class="label">API Key {{ editing ? "（留空表示不修改）" : "" }}</label>
          <input v-model="form.api_key" type="password" class="input mt-1" placeholder="sk-..." />
        </div>
        <div>
          <label class="label">Chat 模型</label>
          <input v-model="form.chat_model" class="input mt-1" placeholder="deepseek-v4-pro" />
        </div>
        <div>
          <label class="label">Embedding 模型（可留空）</label>
          <input v-model="form.embedding_model" class="input mt-1" placeholder="留空时长期记忆按时间排序" />
        </div>
        <div>
          <label class="label">Temperature</label>
          <input v-model.number="form.temperature" type="number" min="0" max="2" step="0.1" class="input mt-1" />
        </div>
        <div>
          <label class="label">Max tokens</label>
          <input v-model.number="form.max_tokens" type="number" min="64" max="32768" class="input mt-1" />
        </div>
        <label class="flex items-center gap-2 text-sm text-ink-300 col-span-2">
          <input v-model="form.is_default" type="checkbox" class="rounded" />
          设为默认（未指定模型的角色会使用此配置）
        </label>
      </div>
      <div v-if="err" class="text-rose-400 text-sm">{{ err }}</div>
      <div class="flex gap-2 justify-end">
        <button class="btn-primary" @click="submit">{{ editing ? "保存" : "添加" }}</button>
      </div>
    </div>

    <div class="space-y-3">
      <div v-for="c in items" :key="c.id" class="card p-5">
        <div class="flex items-start gap-3">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 flex-wrap">
              <div class="font-medium">{{ c.name }}</div>
              <span v-if="c.is_default" class="badge-active">默认</span>
              <span
                v-if="!c.api_key_set"
                class="badge text-[10px] text-amber-300 border-amber-500/40"
              >未设置 API Key</span>
              <span
                v-else
                class="badge text-[10px] text-emerald-300 border-emerald-500/40"
              >API Key 已设置</span>
            </div>
            <div class="text-xs text-ink-400 mt-1">
              {{ c.chat_model }} · embedding: {{ c.embedding_model || "无" }} · temp {{ c.temperature }}
            </div>
            <div class="text-xs text-ink-500 truncate">{{ c.base_url }}</div>
          </div>
          <div class="flex gap-2 flex-shrink-0">
            <button class="btn-ghost" @click="test(c)" :disabled="testing === c.id">
              {{ testing === c.id ? "测试中…" : "测试" }}
            </button>
            <button class="btn-ghost" @click="edit(c)">编辑</button>
            <button class="btn-ghost" @click="remove(c)">删除</button>
          </div>
        </div>
        <div
          v-if="testResults[c.id]"
          class="text-xs mt-2"
          :class="testResults[c.id].ok ? 'text-emerald-400' : 'text-rose-400'"
        >
          {{ testResults[c.id].msg }}
        </div>
      </div>
    </div>
  </div>
</template>
