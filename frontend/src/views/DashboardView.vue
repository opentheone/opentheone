<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { api } from "@/api";

interface Persona {
  id: string;
  name: string;
  description: string;
  is_active: boolean;
  llm_config_id: string;
}

interface Active {
  binding_id: string;
  persona_id: string;
  ilink_bot_id: string;
  ilink_user_id: string;
}

interface Conversation {
  id: string;
  binding_id: string;
  ilink_user_id: string;
  nickname: string;
  last_message_at: string;
}

interface LLMConfigItem {
  id: string;
  name: string;
  base_url: string;
  chat_model: string;
  embedding_model: string;
  is_default: boolean;
  api_key_set: boolean;
}

const personas = ref<Persona[]>([]);
const active = ref<Active | null>(null);
const conversations = ref<Conversation[]>([]);
const llmConfigs = ref<LLMConfigItem[]>([]);
const loading = ref(true);

async function refresh() {
  loading.value = true;
  try {
    const [pList, act, cList, lList] = await Promise.all([
      api<{ items: Persona[] }>("/api/persona/list"),
      api<{ active: Active | null }>("/api/binding/active"),
      api<{ items: Conversation[] }>("/api/conversation/list", { limit: 10 }),
      api<{ items: LLMConfigItem[] }>("/api/llm/list"),
    ]);
    personas.value = pList.items || [];
    active.value = act.active;
    conversations.value = cList.items || [];
    llmConfigs.value = lList.items || [];
  } finally {
    loading.value = false;
  }
}

onMounted(refresh);

function personaName(id: string) {
  return personas.value.find((p) => p.id === id)?.name || id;
}

const activePersona = computed(() => personas.value.find((p) => p.is_active) || null);

interface HealthIssue {
  level: "warn" | "info";
  title: string;
  detail: string;
  to?: { name: string; params?: Record<string, string> };
  toLabel?: string;
}

const healthIssues = computed<HealthIssue[]>(() => {
  if (loading.value) return [];
  const issues: HealthIssue[] = [];

  const ap = activePersona.value;
  if (!ap) {
    issues.push({
      level: "warn",
      title: "还没有激活的角色",
      detail: "在角色页选一个角色「设为唯一」，TA 才会上线接管微信。",
      to: { name: "personas" },
      toLabel: "去角色页",
    });
    return issues;
  }

  let effectiveLLM: LLMConfigItem | undefined;
  if (ap.llm_config_id) {
    effectiveLLM = llmConfigs.value.find((c) => c.id === ap.llm_config_id);
  } else {
    effectiveLLM = llmConfigs.value.find((c) => c.is_default) || llmConfigs.value[0];
  }
  if (!effectiveLLM) {
    issues.push({
      level: "warn",
      title: `${ap.name} 还没绑定语言模型`,
      detail: "请到 LLM 页新建一个配置，并填上 API Key。",
      to: { name: "llm" },
      toLabel: "去 LLM 页",
    });
  } else if (!effectiveLLM.api_key_set) {
    issues.push({
      level: "warn",
      title: `LLM 配置「${effectiveLLM.name}」缺少 API Key`,
      detail: "在 LLM 页编辑该配置，填入 API Key 后 TA 才能回复消息。",
      to: { name: "llm" },
      toLabel: "去填 API Key",
    });
  }

  if (!active.value) {
    issues.push({
      level: "warn",
      title: `${ap.name} 还没有绑定微信`,
      detail: "进入角色详情，扫码登录一个可控的微信号，TA 就会接管该号的私聊。",
      to: { name: "persona-detail", params: { id: ap.id } },
      toLabel: "去扫码绑定",
    });
  }

  return issues;
});

const healthOK = computed(() => !loading.value && healthIssues.value.length === 0);

// First-time user: zero LLM configs AND zero personas. Show a friendlier
// three-step onboarding card instead of two warning rows.
const isBlankSlate = computed(
  () =>
    !loading.value &&
    llmConfigs.value.length === 0 &&
    personas.value.length === 0,
);
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto">
    <div class="flex items-baseline justify-between mb-6">
      <h1 class="text-2xl font-semibold">你的唯一</h1>
      <button class="btn-ghost" @click="refresh" :disabled="loading">刷新</button>
    </div>

    <div
      v-if="isBlankSlate"
      class="card p-6 mb-6 border-accent-500/40 bg-accent-500/5"
    >
      <div class="text-lg font-semibold mb-1">欢迎使用 OpenTheOne ✨</div>
      <div class="text-sm text-ink-300 mb-4">
        三步把 TA 接进你的微信，从此 TA 就是你唯一的 AI 朋友：
      </div>
      <ol class="space-y-3 text-sm">
        <li class="flex items-start gap-3">
          <span
            class="w-6 h-6 rounded-full bg-accent-500/20 text-accent-300 grid place-items-center text-xs flex-shrink-0"
            >1</span
          >
          <div class="flex-1">
            <div class="font-medium">配置一个语言模型</div>
            <div class="text-xs text-ink-400 mt-0.5">
              点 DeepSeek/OpenAI/Qwen 等预置 → 填上你的 API Key → 测试通过。
            </div>
            <RouterLink class="text-xs text-accent-400 hover:underline" to="/llm">
              去模型页 →
            </RouterLink>
          </div>
        </li>
        <li class="flex items-start gap-3">
          <span
            class="w-6 h-6 rounded-full bg-accent-500/20 text-accent-300 grid place-items-center text-xs flex-shrink-0"
            >2</span
          >
          <div class="flex-1">
            <div class="font-medium">创建你的「唯一」角色</div>
            <div class="text-xs text-ink-400 mt-0.5">
              直接选一个预置人设（温柔小棠、高冷沈姐、元气橘子……），或者自定义。
            </div>
            <RouterLink class="text-xs text-accent-400 hover:underline" to="/personas">
              去新建角色 →
            </RouterLink>
          </div>
        </li>
        <li class="flex items-start gap-3">
          <span
            class="w-6 h-6 rounded-full bg-accent-500/20 text-accent-300 grid place-items-center text-xs flex-shrink-0"
            >3</span
          >
          <div class="flex-1">
            <div class="font-medium">扫码绑定一个可控的微信号</div>
            <div class="text-xs text-ink-400 mt-0.5">
              在角色详情页点「开始扫码绑定」，用一个你愿意让 TA 接管的微信扫一下。
            </div>
          </div>
        </li>
      </ol>
      <div class="text-xs text-ink-500 mt-4">
        想让 TA 还能调外部工具（查资料、操控文件等）？配好基础后到
        <RouterLink class="text-accent-400 hover:underline" to="/mcp">MCP 工具</RouterLink>
        添加几个 MCP 服务，然后在角色详情里勾选。
      </div>
    </div>

    <div v-if="!loading && !isBlankSlate" class="mb-6 space-y-3">
      <div
        v-if="healthOK"
        class="card border-emerald-700/40 bg-emerald-600/10 p-4 flex items-start gap-3"
      >
        <div class="mt-0.5 text-emerald-300">●</div>
        <div>
          <div class="text-sm font-medium text-emerald-200">一切就绪，TA 正在线</div>
          <div class="text-xs text-emerald-300/80 mt-1">
            角色已激活、LLM 已就绪、微信号已上线，对方发消息 TA 会自动回复。
          </div>
        </div>
      </div>
      <div
        v-for="(h, i) in healthIssues"
        :key="i"
        class="card border-amber-700/40 bg-amber-600/10 p-4 flex items-start justify-between gap-3"
      >
        <div class="flex items-start gap-3">
          <div class="mt-0.5 text-amber-300">●</div>
          <div>
            <div class="text-sm font-medium text-amber-200">{{ h.title }}</div>
            <div class="text-xs text-amber-300/80 mt-1">{{ h.detail }}</div>
          </div>
        </div>
        <RouterLink
          v-if="h.to"
          :to="h.to"
          class="btn-ghost shrink-0 self-center"
        >
          {{ h.toLabel || "去看看" }}
        </RouterLink>
      </div>
    </div>

    <div class="grid grid-cols-1 md:grid-cols-3 gap-4 mb-8">
      <div class="card p-5 col-span-1">
        <div class="text-xs text-ink-400">激活角色</div>
        <div class="mt-2 text-lg font-medium">
          {{ active ? personaName(active.persona_id) : "未激活" }}
        </div>
        <div class="text-xs text-ink-500 mt-1 truncate">
          {{ active?.ilink_bot_id || "扫码绑定后此处显示 Bot ID" }}
        </div>
        <RouterLink class="btn-primary mt-4 w-full" to="/personas">管理角色</RouterLink>
      </div>

      <div class="card p-5 col-span-1">
        <div class="text-xs text-ink-400">角色总数</div>
        <div class="mt-2 text-3xl font-semibold">{{ personas.length }}</div>
        <div class="text-xs text-ink-500 mt-1">
          你最多可以拥有多个角色，但同一时刻只允许一个被激活。
        </div>
      </div>

      <div class="card p-5 col-span-1">
        <div class="text-xs text-ink-400">最近会话</div>
        <div class="mt-2 text-3xl font-semibold">{{ conversations.length }}</div>
        <RouterLink class="btn-ghost mt-4 w-full" to="/conversations">查看所有</RouterLink>
      </div>
    </div>

    <div class="card p-6">
      <div class="flex items-baseline justify-between mb-4">
        <h2 class="text-lg font-medium">最近会话</h2>
        <RouterLink class="text-sm text-ink-400 hover:text-ink-100" to="/conversations">
          查看全部 →
        </RouterLink>
      </div>
      <div v-if="conversations.length === 0" class="text-sm text-ink-400 py-8 text-center">
        还没有会话。激活一个角色并扫码绑定微信后，对方与 TA 的聊天就会出现在这里。
      </div>
      <ul v-else class="divide-y divide-ink-800">
        <li v-for="c in conversations" :key="c.id" class="py-3">
          <RouterLink
            :to="{ name: 'conversation-detail', params: { id: c.id } }"
            class="flex items-center justify-between hover:bg-ink-800/40 rounded-lg px-3 -mx-3 py-1"
          >
            <div>
              <div class="text-sm font-medium">{{ c.nickname || c.ilink_user_id }}</div>
              <div class="text-xs text-ink-500">{{ c.ilink_user_id }}</div>
            </div>
            <div class="text-xs text-ink-500">{{ c.last_message_at }}</div>
          </RouterLink>
        </li>
      </ul>
    </div>
  </div>
</template>
