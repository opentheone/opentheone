<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "@/api";
import { useRouter } from "vue-router";

const props = defineProps<{ id: string }>();
const router = useRouter();

interface Persona {
  id: string;
  name: string;
  avatar: string;
  description: string;
  system_prompt: string;
  greeting: string;
  speaking_style: string;
  proactive_cron: string;
  proactive_prompt: string;
  is_active: boolean;
  llm_config_id: string;
  enabled_mcp_ids: string;
}

interface LLMConfig {
  id: string;
  name: string;
  chat_model: string;
}

interface MCPServerLite {
  id: string;
  name: string;
  transport: string;
  enabled: boolean;
  description: string;
}

interface MCPToolInfo {
  name: string;
  description: string;
}

interface MCPToolsResult {
  ok: boolean;
  error: string;
  tools: MCPToolInfo[];
}

interface Memory {
  id: string;
  kind: string; // persona | episodic | instruction (+ legacy: fact / preference / event / summary)
  content: string;
  importance: number;
  scene_id: string;
  created_at: string;
}

interface MemoryScene {
  id: string;
  title: string;
  summary: string;
  atom_count: number;
  heat: number;
  updated_at: string;
}

interface UserProfile {
  id: string;
  content: string;
  scene_count_at_gen: number;
  atoms_at_gen: number;
  generated_at: string;
  request_reason: string;
}

interface BindingStatus {
  binding_id: string;
  state: string;
  phase?: string;
  qrcode_image_url: string;
  ilink_bot_id: string;
  ilink_user_id: string;
}

const phaseLabel: Record<string, string> = {
  wait: "等待扫码",
  scaned: "已扫码，请在微信里确认",
  scanned: "已扫码，请在微信里确认",
  confirmed: "已绑定",
  expired: "二维码 / 会话已过期",
  pending_scan: "等待扫码",
  active: "在线中",
  paused: "已暂停",
  revoked: "已解绑",
};

const persona = ref<Persona | null>(null);
const llmList = ref<LLMConfig[]>([]);
const mcpList = ref<MCPServerLite[]>([]);
const enabledMCPIDs = ref<string[]>([]);
const mcpExpanded = ref<Record<string, boolean>>({});
const mcpToolsLoading = ref<Record<string, boolean>>({});
const mcpToolsCache = ref<Record<string, MCPToolsResult>>({});
const memories = ref<Memory[]>([]);
const memoryKindFilter = ref<string>("all");
const scenes = ref<MemoryScene[]>([]);
const maxScenes = ref<number>(15);
const expandedSceneID = ref<string | null>(null);
const expandedSceneAtoms = ref<Memory[]>([]);
const expandedSceneLoading = ref(false);
const profile = ref<UserProfile | null>(null);
const profileBusy = ref(false);
const profileResult = ref<{ ok: boolean; msg: string } | null>(null);
const err = ref("");
const saving = ref(false);

const binding = ref<BindingStatus | null>(null);
const polling = ref(false);
let pollTimer: ReturnType<typeof setInterval> | null = null;

const testBusy = ref(false);
const testResult = ref<{ ok: boolean; msg: string } | null>(null);

async function loadPersona() {
  const p = await api<Persona>("/api/persona/get", { id: props.id });
  persona.value = p;
  try {
    enabledMCPIDs.value = p.enabled_mcp_ids ? JSON.parse(p.enabled_mcp_ids) : [];
  } catch {
    enabledMCPIDs.value = [];
  }
}

async function loadMCP() {
  try {
    const r = await api<{ items: MCPServerLite[] }>("/api/mcp/list");
    mcpList.value = r.items || [];
  } catch {
    mcpList.value = [];
  }
}

function toggleMCP(id: string) {
  const set = new Set(enabledMCPIDs.value);
  if (set.has(id)) set.delete(id);
  else set.add(id);
  enabledMCPIDs.value = Array.from(set);
}

async function toggleMCPTools(id: string) {
  const next = !mcpExpanded.value[id];
  mcpExpanded.value = { ...mcpExpanded.value, [id]: next };
  if (next && !mcpToolsCache.value[id] && !mcpToolsLoading.value[id]) {
    mcpToolsLoading.value = { ...mcpToolsLoading.value, [id]: true };
    try {
      const r = await api<MCPToolsResult>("/api/mcp/tools", { id });
      mcpToolsCache.value = { ...mcpToolsCache.value, [id]: r };
    } catch (e: any) {
      mcpToolsCache.value = {
        ...mcpToolsCache.value,
        [id]: { ok: false, error: e?.message || String(e), tools: [] },
      };
    } finally {
      mcpToolsLoading.value = { ...mcpToolsLoading.value, [id]: false };
    }
  }
}

async function loadExistingBinding() {
  const r = await api<{ binding: BindingStatus | null }>(
    "/api/binding/for_persona",
    { persona_id: props.id },
  );
  binding.value = r.binding || null;
  if (binding.value?.state === "pending_scan") {
    startPolling();
  }
}

async function loadLLM() {
  const r = await api<{ items: LLMConfig[] }>("/api/llm/list");
  llmList.value = r.items || [];
}

async function loadMemories() {
  const r = await api<{ items: Memory[] }>("/api/memory/list", { persona_id: props.id, limit: 100 });
  memories.value = r.items || [];
}

const kindLabels: Record<string, string> = {
  persona: "稳定属性",
  episodic: "事件",
  instruction: "对 AI 指令",
  // legacy kinds — still shown if they predate the new taxonomy:
  fact: "事实(旧)",
  preference: "偏好(旧)",
  event: "事件(旧)",
  summary: "摘要(旧)",
};

const filteredMemories = computed(() => {
  if (memoryKindFilter.value === "all") return memories.value;
  return memories.value.filter((m) => normaliseKind(m.kind) === memoryKindFilter.value);
});

function normaliseKind(k: string): string {
  switch (k) {
    case "preference":
    case "fact":
    case "summary":
      return "persona";
    case "event":
      return "episodic";
    case "rule":
      return "instruction";
    default:
      return k || "persona";
  }
}

async function loadScenes() {
  try {
    const r = await api<{ items: MemoryScene[]; max_scenes: number }>("/api/scene/list", { persona_id: props.id });
    scenes.value = r.items || [];
    if (r.max_scenes) maxScenes.value = r.max_scenes;
  } catch {
    scenes.value = [];
  }
}

async function toggleScene(s: MemoryScene) {
  if (expandedSceneID.value === s.id) {
    expandedSceneID.value = null;
    expandedSceneAtoms.value = [];
    return;
  }
  expandedSceneID.value = s.id;
  expandedSceneLoading.value = true;
  try {
    const r = await api<{ scene: MemoryScene; atoms: Memory[] }>("/api/scene/get", {
      persona_id: props.id,
      scene_id: s.id,
    });
    expandedSceneAtoms.value = r.atoms || [];
  } catch {
    expandedSceneAtoms.value = [];
  } finally {
    expandedSceneLoading.value = false;
  }
}

async function deleteScene(s: MemoryScene) {
  if (!confirm(`删除场景「${s.title}」？（其中归属的记忆会保留，但会被重新归类。）`)) return;
  await api("/api/scene/delete", { persona_id: props.id, scene_id: s.id });
  if (expandedSceneID.value === s.id) {
    expandedSceneID.value = null;
    expandedSceneAtoms.value = [];
  }
  await loadScenes();
}

async function loadProfile() {
  try {
    const r = await api<{ profile: UserProfile | null }>("/api/profile/get", { persona_id: props.id });
    profile.value = r.profile;
  } catch {
    profile.value = null;
  }
}

async function regenerateProfile() {
  profileBusy.value = true;
  profileResult.value = null;
  try {
    const r = await api<{ profile: UserProfile }>("/api/profile/regenerate", {
      persona_id: props.id,
      reason: "manual",
    });
    profile.value = r.profile;
    profileResult.value = { ok: true, msg: `已重新生成，基于 ${r.profile.atoms_at_gen} 条记忆 / ${r.profile.scene_count_at_gen} 个场景。` };
  } catch (e: any) {
    profileResult.value = { ok: false, msg: e?.message || String(e) };
  } finally {
    profileBusy.value = false;
  }
}

async function save() {
  if (!persona.value) return;
  saving.value = true;
  err.value = "";
  try {
    await api("/api/persona/update", {
      id: persona.value.id,
      name: persona.value.name,
      avatar: persona.value.avatar ?? "",
      description: persona.value.description,
      system_prompt: persona.value.system_prompt,
      greeting: persona.value.greeting,
      speaking_style: persona.value.speaking_style,
      proactive_cron: persona.value.proactive_cron,
      proactive_prompt: persona.value.proactive_prompt,
      llm_config_id: persona.value.llm_config_id,
      enabled_mcp_ids: enabledMCPIDs.value,
    });
  } catch (e: any) {
    err.value = e?.message || String(e);
  } finally {
    saving.value = false;
  }
}

async function activate() {
  if (!persona.value) return;
  err.value = "";
  try {
    await api("/api/persona/activate", { id: persona.value.id });
    await Promise.all([loadPersona(), loadExistingBinding()]);
  } catch (e: any) {
    err.value = e?.message || String(e);
  }
}

async function deactivate() {
  if (!persona.value) return;
  if (!confirm("确认让 TA 暂时下线？所有 active 状态会被清空，需要时再「设为唯一」恢复（无需重新扫码）。")) return;
  err.value = "";
  try {
    await api("/api/persona/deactivate");
    await Promise.all([loadPersona(), loadExistingBinding()]);
  } catch (e: any) {
    err.value = e?.message || String(e);
  }
}

const proactiveBusy = ref(false);
const proactiveResult = ref<{ ok: boolean; msg: string } | null>(null);

async function triggerProactive() {
  if (!persona.value) return;
  proactiveBusy.value = true;
  proactiveResult.value = null;
  try {
    await api("/api/persona/trigger_proactive", { id: persona.value.id });
    proactiveResult.value = { ok: true, msg: "已触发，请稍候查看微信端。" };
  } catch (e: any) {
    proactiveResult.value = { ok: false, msg: e?.message || String(e) };
  } finally {
    proactiveBusy.value = false;
  }
}

async function startBinding() {
  err.value = "";
  try {
    binding.value = await api<BindingStatus>("/api/binding/start", { persona_id: props.id });
    startPolling();
  } catch (e: any) {
    err.value = e?.message || String(e);
  }
}

function startPolling() {
  stopPolling();
  if (!binding.value) return;
  polling.value = true;
  pollTimer = setInterval(async () => {
    if (!binding.value) return;
    try {
      const s = await api<BindingStatus>("/api/binding/status", { binding_id: binding.value.binding_id });
      binding.value = s;
      if (s.state === "active" || s.state === "expired" || s.state === "revoked") {
        stopPolling();
      }
    } catch {}
  }, 1500);
}

function stopPolling() {
  polling.value = false;
  if (pollTimer) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

async function revoke() {
  if (!binding.value) return;
  if (!confirm("确认解绑？将停止接收消息。")) return;
  await api("/api/binding/revoke", { binding_id: binding.value.binding_id });
  binding.value = null;
}

async function sendTestMessage() {
  if (!binding.value) return;
  testBusy.value = true;
  testResult.value = null;
  try {
    const list = await api<{ items: Array<{ id: string }> }>(
      "/api/conversation/list",
      { binding_id: binding.value.binding_id, limit: 1 },
    );
    const conv = list.items?.[0];
    if (!conv) {
      testResult.value = {
        ok: false,
        msg: "还没有任何会话。请让 TA 在微信里先收到一条消息（context_token 来自入站消息）。",
      };
      return;
    }
    await api("/api/conversation/send_manual", {
      conversation_id: conv.id,
      text: "（这是一条来自 OpenTheOne 后台的测试消息）",
    });
    testResult.value = { ok: true, msg: "已发送，请检查微信端。" };
  } catch (e: any) {
    testResult.value = { ok: false, msg: e?.message || String(e) };
  } finally {
    testBusy.value = false;
  }
}

async function forgetMemory(m: Memory) {
  if (!confirm("删除这条记忆？")) return;
  await api("/api/memory/delete", { id: m.id });
  await loadMemories();
}

const newMemory = ref({ content: "", importance: 5, kind: "persona" });
async function addMemory() {
  if (!newMemory.value.content.trim()) return;
  await api("/api/memory/upsert_manual", { persona_id: props.id, ...newMemory.value });
  newMemory.value = { content: "", importance: 5, kind: "persona" };
  await loadMemories();
}

watch(() => props.id, async () => {
  await Promise.all([loadPersona(), loadMemories(), loadScenes(), loadProfile(), loadExistingBinding()]);
});

onMounted(async () => {
  try {
    await Promise.all([
      loadPersona(),
      loadLLM(),
      loadMCP(),
      loadMemories(),
      loadScenes(),
      loadProfile(),
      loadExistingBinding(),
    ]);
  } catch (e: any) {
    err.value = e?.message || String(e);
  }
});

onUnmounted(stopPolling);

function back() {
  router.push({ name: "personas" });
}
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto" v-if="persona">
    <div class="flex items-baseline justify-between mb-6">
      <div>
        <button class="text-sm text-ink-400 hover:text-ink-100" @click="back">← 返回</button>
        <h1 class="text-2xl font-semibold flex items-center gap-2 mt-1">
          {{ persona.name }}
          <span v-if="persona.is_active" class="badge-active">激活</span>
        </h1>
      </div>
      <div class="flex gap-2 flex-wrap">
        <button v-if="!persona.is_active" class="btn-primary" @click="activate">设为唯一</button>
        <button v-else class="btn-ghost" @click="deactivate">让 TA 下线</button>
      </div>
    </div>

    <div v-if="err" class="text-rose-400 text-sm mb-4">{{ err }}</div>

    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <div class="space-y-6">
        <div class="card p-6 space-y-4">
          <h2 class="text-lg font-medium">人设</h2>
          <div class="grid grid-cols-[80px_1fr] gap-4">
            <div>
              <label class="label">头像</label>
              <input
                v-model="persona.avatar"
                class="input mt-1 text-center text-2xl"
                placeholder="🌸"
                maxlength="4"
              />
            </div>
            <div>
              <label class="label">名字</label>
              <input v-model="persona.name" class="input mt-1" />
            </div>
          </div>
          <div>
            <label class="label">简介</label>
            <input v-model="persona.description" class="input mt-1" />
          </div>
          <div>
            <label class="label">完整 system prompt</label>
            <textarea v-model="persona.system_prompt" rows="8" class="textarea mt-1" />
          </div>
          <div>
            <label class="label">说话风格</label>
            <input v-model="persona.speaking_style" class="input mt-1" />
          </div>
          <div>
            <label class="label">开场白</label>
            <input v-model="persona.greeting" class="input mt-1" />
          </div>
          <div>
            <label class="label">主动消息 cron</label>
            <input v-model="persona.proactive_cron" class="input mt-1" placeholder="0 9 * * *" />
          </div>
          <div>
            <label class="label">主动消息 prompt</label>
            <textarea v-model="persona.proactive_prompt" rows="3" class="textarea mt-1" />
          </div>
          <div>
            <label class="label">使用的模型</label>
            <select v-model="persona.llm_config_id" class="input mt-1">
              <option value="">默认</option>
              <option v-for="l in llmList" :key="l.id" :value="l.id">
                {{ l.name }} — {{ l.chat_model }}
              </option>
            </select>
          </div>

          <div>
            <label class="label">启用的 MCP 工具</label>
            <p class="text-xs text-ink-500 mt-1 mb-2">
              勾选后，TA 在对话时可以调用对应 MCP 服务里的工具（agent loop）。没勾的不会出现在 TA 的工具列表里。
            </p>
            <div v-if="mcpList.length === 0" class="text-xs text-ink-400">
              还没有 MCP 服务。先到
              <RouterLink to="/mcp" class="text-accent-400 hover:underline">「MCP 工具」</RouterLink>
              里添加几个。
            </div>
            <ul v-else class="space-y-2">
              <li
                v-for="s in mcpList"
                :key="s.id"
                class="rounded-lg border border-ink-800 bg-ink-900"
              >
                <div class="flex items-start gap-3 px-3 py-2">
                  <input
                    type="checkbox"
                    class="mt-1 rounded"
                    :checked="enabledMCPIDs.includes(s.id)"
                    :disabled="!s.enabled"
                    @change="toggleMCP(s.id)"
                  />
                  <div class="flex-1 min-w-0">
                    <div class="flex items-center gap-2 flex-wrap">
                      <span class="text-sm font-medium">{{ s.name }}</span>
                      <span class="badge text-[10px]">{{ s.transport }}</span>
                      <span
                        v-if="!s.enabled"
                        class="badge text-[10px] text-amber-300 border-amber-500/40"
                      >全局已禁用</span>
                      <button
                        type="button"
                        class="ml-auto text-[11px] text-ink-400 hover:text-ink-100"
                        :disabled="!s.enabled"
                        @click="toggleMCPTools(s.id)"
                      >
                        {{ mcpExpanded[s.id] ? "收起工具 ▴" : "查看工具 ▾" }}
                      </button>
                    </div>
                    <div v-if="s.description" class="text-[11px] text-ink-500 mt-0.5">
                      {{ s.description }}
                    </div>
                  </div>
                </div>
                <div
                  v-if="mcpExpanded[s.id]"
                  class="border-t border-ink-800 px-3 py-2 bg-ink-950"
                >
                  <div v-if="mcpToolsLoading[s.id]" class="text-[11px] text-ink-500">
                    加载中…
                  </div>
                  <div v-else-if="!mcpToolsCache[s.id]" class="text-[11px] text-ink-500">
                    点击「查看工具」加载列表
                  </div>
                  <div
                    v-else-if="!mcpToolsCache[s.id].ok"
                    class="text-[11px] text-rose-400"
                  >
                    无法连接：{{ mcpToolsCache[s.id].error }}
                  </div>
                  <div
                    v-else-if="mcpToolsCache[s.id].tools.length === 0"
                    class="text-[11px] text-ink-500"
                  >
                    服务连接正常，但未声明任何工具。
                  </div>
                  <ul v-else class="space-y-1.5">
                    <li
                      v-for="t in mcpToolsCache[s.id].tools"
                      :key="t.name"
                      class="text-[11px]"
                    >
                      <div class="font-mono text-accent-300">{{ t.name }}</div>
                      <div v-if="t.description" class="text-ink-400 ml-3 leading-snug">
                        {{ t.description }}
                      </div>
                    </li>
                  </ul>
                </div>
              </li>
            </ul>
          </div>

          <button class="btn-primary" :disabled="saving" @click="save">保存</button>
        </div>
      </div>

      <div class="space-y-6">
        <div class="card p-6">
          <h2 class="text-lg font-medium mb-2">接入微信</h2>
          <p class="text-xs text-ink-400 mb-4">
            扫码即可让 TA 成为你微信里的联系人。绑定后通过官方 ClawBot/iLink 协议长轮询接收消息。
          </p>

          <div v-if="!binding || ['revoked', 'expired'].includes(binding.state)">
            <p v-if="binding && binding.state === 'expired'" class="text-amber-400 text-xs mb-2">
              上次会话过期，需要重新扫码。
            </p>
            <button class="btn-primary" @click="startBinding">开始扫码绑定</button>
          </div>
          <div v-else class="space-y-3">
            <div class="flex items-center gap-2 text-sm">
              <span class="text-ink-400">状态：</span>
              <span class="badge" :class="{
                'badge-active': binding.state === 'active',
                'badge-pending': binding.state === 'pending_scan' || binding.state === 'paused',
                'badge-error': binding.state === 'expired' || binding.state === 'revoked',
              }">{{ phaseLabel[binding.phase || binding.state] || binding.state }}</span>
            </div>
            <div
              v-if="binding.qrcode_image_url && binding.state === 'pending_scan'"
              class="bg-white rounded-xl p-4 text-center"
            >
              <img :src="binding.qrcode_image_url" alt="qrcode" class="w-48 h-48 mx-auto" />
              <div class="text-xs text-ink-700 mt-2">请用微信扫一扫</div>
            </div>
            <div v-if="binding.state === 'paused'" class="text-xs text-amber-300">
              该绑定已被暂停。把此角色「设为唯一」即可恢复长轮询；无需重新扫码。
            </div>
            <div v-if="['active', 'paused'].includes(binding.state)" class="text-xs text-ink-400 space-y-1">
              <div v-if="binding.ilink_bot_id"><span class="text-ink-500">bot id：</span>{{ binding.ilink_bot_id }}</div>
              <div v-if="binding.ilink_user_id"><span class="text-ink-500">user id：</span>{{ binding.ilink_user_id }}</div>
            </div>
            <div class="flex gap-2 pt-2 flex-wrap">
              <button v-if="binding.state === 'pending_scan'" class="btn-ghost" @click="startBinding">
                重新生成二维码
              </button>
              <button v-if="binding.state === 'active'" class="btn-ghost" :disabled="testBusy" @click="sendTestMessage">
                {{ testBusy ? "发送中…" : "发送测试消息" }}
              </button>
              <button
                v-if="binding.state === 'active' && persona.proactive_cron"
                class="btn-ghost"
                :disabled="proactiveBusy"
                @click="triggerProactive"
              >
                {{ proactiveBusy ? "触发中…" : "立即主动一条" }}
              </button>
              <button v-if="['active', 'paused'].includes(binding.state)" class="btn-danger" @click="revoke">解绑</button>
            </div>
            <div
              v-if="testResult"
              class="text-xs"
              :class="testResult.ok ? 'text-emerald-400' : 'text-rose-400'"
            >
              {{ testResult.msg }}
            </div>
            <div
              v-if="proactiveResult"
              class="text-xs"
              :class="proactiveResult.ok ? 'text-emerald-400' : 'text-rose-400'"
            >
              {{ proactiveResult.msg }}
            </div>
          </div>
        </div>

        <div class="card p-6">
          <div class="flex items-baseline justify-between mb-2">
            <h2 class="text-lg font-medium">用户画像 (L3)</h2>
            <button
              class="btn-ghost text-xs"
              :disabled="profileBusy"
              @click="regenerateProfile"
            >{{ profileBusy ? "生成中…" : "重新生成" }}</button>
          </div>
          <p class="text-xs text-ink-400 mb-3">
            由 LLM 把所有长期记忆 + 主题场景汇总成的简洁画像，会自动注入到每次对话的系统提示头部。
          </p>
          <div
            v-if="profileResult"
            class="text-[11px] mb-2"
            :class="profileResult.ok ? 'text-emerald-400' : 'text-rose-400'"
          >{{ profileResult.msg }}</div>
          <div v-if="!profile" class="text-xs text-ink-400 py-4 text-center">
            尚未生成。多聊几轮后 TA 会自动生成；也可以现在手动「重新生成」。
          </div>
          <div v-else>
            <div class="text-[11px] text-ink-500 mb-2">
              最近生成：{{ profile.generated_at }} · {{ profile.atoms_at_gen }} 条记忆 / {{ profile.scene_count_at_gen }} 个场景 · 触发：{{ profile.request_reason }}
            </div>
            <pre class="text-xs whitespace-pre-wrap font-sans bg-ink-950 border border-ink-800 rounded-lg p-3 max-h-72 overflow-y-auto">{{ profile.content }}</pre>
          </div>
        </div>

        <div class="card p-6">
          <div class="flex items-baseline justify-between mb-2">
            <h2 class="text-lg font-medium">主题场景 (L2)</h2>
            <span class="text-xs text-ink-500">{{ scenes.length }} / {{ maxScenes }}</span>
          </div>
          <p class="text-xs text-ink-400 mb-3">
            LLM 把相关的原子记忆归到主题场景里。每个场景的标题/摘要会自动注入系统提示，作为「记忆目录」让 TA 知道什么时候应该深读。
          </p>
          <div v-if="scenes.length === 0" class="text-xs text-ink-400 py-4 text-center">
            尚无场景。
          </div>
          <ul v-else class="divide-y divide-ink-800">
            <li v-for="s in scenes" :key="s.id" class="py-2">
              <div class="flex items-start gap-2">
                <div class="flex-1 min-w-0">
                  <div class="text-sm font-medium">{{ s.title }}</div>
                  <div class="text-[11px] text-ink-400 mt-0.5">{{ s.summary || "（待生成摘要）" }}</div>
                  <div class="text-[10px] text-ink-500 mt-1">
                    {{ s.atom_count }} 条 · heat {{ s.heat }} · {{ s.updated_at }}
                  </div>
                </div>
                <div class="flex flex-col gap-1 items-end">
                  <button class="text-ink-400 hover:text-ink-100 text-xs" @click="toggleScene(s)">
                    {{ expandedSceneID === s.id ? "收起" : "查看" }}
                  </button>
                  <button class="text-ink-500 hover:text-rose-400 text-xs" @click="deleteScene(s)">删除</button>
                </div>
              </div>
              <div v-if="expandedSceneID === s.id" class="mt-2 pl-2 border-l-2 border-ink-800">
                <div v-if="expandedSceneLoading" class="text-[11px] text-ink-500">加载中…</div>
                <ul v-else-if="expandedSceneAtoms.length === 0" class="text-[11px] text-ink-500">
                  （场景内尚无活跃原子）
                </ul>
                <ul v-else class="space-y-1">
                  <li v-for="a in expandedSceneAtoms" :key="a.id" class="text-[11px]">
                    <span class="text-ink-500">[{{ kindLabels[a.kind] || a.kind }} · imp {{ a.importance }}]</span>
                    {{ a.content }}
                  </li>
                </ul>
              </div>
            </li>
          </ul>
        </div>

        <div class="card p-6">
          <h2 class="text-lg font-medium mb-2">原子记忆 (L1)</h2>
          <p class="text-xs text-ink-400 mb-4">
            对话过程中，TA 会自动归纳值得长期记住的事实。你也可以手动写入或删除。
          </p>

          <div class="space-y-2 mb-4">
            <textarea v-model="newMemory.content" rows="2" class="textarea" placeholder="想让 TA 永远记住的一句话…" />
            <div class="flex items-center gap-2 text-xs">
              <select v-model="newMemory.kind" class="input w-32 text-xs py-1">
                <option value="persona">稳定属性</option>
                <option value="episodic">事件</option>
                <option value="instruction">对 AI 指令</option>
              </select>
              <label class="text-ink-400">重要性</label>
              <input v-model.number="newMemory.importance" type="number" min="1" max="10" class="input w-16 text-xs py-1" />
              <button class="btn-primary ml-auto" @click="addMemory">写入</button>
            </div>
          </div>

          <div class="flex items-center gap-2 text-xs mb-2">
            <span class="text-ink-400">筛选：</span>
            <button
              v-for="k in ['all', 'persona', 'episodic', 'instruction']"
              :key="k"
              class="px-2 py-0.5 rounded border"
              :class="memoryKindFilter === k
                ? 'bg-accent-600 border-accent-600 text-white'
                : 'border-ink-700 text-ink-300 hover:border-ink-500'"
              @click="memoryKindFilter = k"
            >{{ k === 'all' ? '全部' : kindLabels[k] }}</button>
          </div>

          <div v-if="filteredMemories.length === 0" class="text-xs text-ink-400 py-4 text-center">
            尚无记忆。开始聊天后 TA 会自动归纳重要内容到这里。
          </div>
          <ul v-else class="divide-y divide-ink-800 max-h-96 overflow-y-auto">
            <li v-for="m in filteredMemories" :key="m.id" class="py-2 flex items-start gap-2">
              <span class="badge text-[10px] mt-0.5">{{ kindLabels[m.kind] || m.kind }}</span>
              <div class="flex-1 text-sm">
                <div>{{ m.content }}</div>
                <div class="text-[10px] text-ink-500 mt-1">
                  重要性 {{ m.importance }} · {{ m.created_at }}
                  <span v-if="m.scene_id"> · scene</span>
                </div>
              </div>
              <button class="text-ink-500 hover:text-rose-400 text-xs" @click="forgetMemory(m)">删除</button>
            </li>
          </ul>
        </div>
      </div>
    </div>
  </div>
</template>
