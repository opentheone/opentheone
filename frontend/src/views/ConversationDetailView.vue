<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { api } from "@/api";

const props = defineProps<{ id: string }>();
const router = useRouter();

interface Message {
  id: string;
  direction: "inbound" | "outbound" | "tool_call" | "tool_result";
  type: string;
  text: string;
  status: string;
  created_at: string;
  media_url?: string;
  tool_name?: string;
  tool_call_id?: string;
  tool_args?: string;
  tool_result?: string;
}

function prettyJSON(raw?: string): string {
  if (!raw) return "";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

function shortToolName(name?: string): string {
  if (!name) return "tool";
  // mcp__s0__list_files -> list_files; keep raw name as tooltip elsewhere
  const m = name.match(/^mcp__[^_]+__(.+)$/);
  return m ? m[1] : name;
}

const messages = ref<Message[]>([]);
const hasMore = ref(false);
const loading = ref(false);
const loadingMore = ref(false);
const manual = ref("");
const scrollEl = ref<HTMLElement | null>(null);
const attachments = ref<Record<string, string>>({});
const errMsg = ref("");
const summary = ref("");
const summaryUpdatedAt = ref("");
const summaryOpen = ref(false);
const rebuildingSummary = ref(false);

async function load() {
  loading.value = true;
  errMsg.value = "";
  try {
    const r = await api<{
      messages: Message[];
      has_more: boolean;
      summary?: string;
      summary_updated_at?: string;
    }>("/api/conversation/messages", { conversation_id: props.id, limit: 50 });
    messages.value = r.messages || [];
    hasMore.value = r.has_more;
    summary.value = r.summary || "";
    summaryUpdatedAt.value = r.summary_updated_at || "";
    await loadAttachments(messages.value);
    await nextTick();
    if (scrollEl.value) scrollEl.value.scrollTop = scrollEl.value.scrollHeight;
  } catch (e: any) {
    errMsg.value = e?.message || String(e);
  } finally {
    loading.value = false;
  }
}

async function rebuildSummary() {
  if (rebuildingSummary.value) return;
  rebuildingSummary.value = true;
  try {
    const r = await api<{ summary: string; summary_updated_at: string }>(
      "/api/conversation/rebuild_summary",
      { conversation_id: props.id },
    );
    summary.value = r.summary || "";
    summaryUpdatedAt.value = r.summary_updated_at || "";
    summaryOpen.value = true;
  } catch (e: any) {
    errMsg.value = e?.message || String(e);
  } finally {
    rebuildingSummary.value = false;
  }
}

async function loadMore() {
  if (loadingMore.value || messages.value.length === 0) return;
  loadingMore.value = true;
  try {
    const oldest = messages.value[0];
    const r = await api<{ messages: Message[]; has_more: boolean }>(
      "/api/conversation/messages",
      { conversation_id: props.id, limit: 50, before: oldest.created_at },
    );
    const olderHeightBefore = scrollEl.value?.scrollHeight ?? 0;
    messages.value = [...(r.messages || []), ...messages.value];
    hasMore.value = r.has_more;
    await loadAttachments(r.messages || []);
    await nextTick();
    if (scrollEl.value) {
      scrollEl.value.scrollTop =
        scrollEl.value.scrollHeight - olderHeightBefore;
    }
  } finally {
    loadingMore.value = false;
  }
}

function isImageMessage(m: Message): boolean {
  if (m.type === "image") return true;
  if (m.media_url) {
    return /\.(jpe?g|png|gif|webp|bmp)$/i.test(m.media_url);
  }
  return false;
}

async function loadAttachments(msgs: Message[]) {
  for (const m of msgs) {
    if (m.direction !== "inbound" && m.direction !== "outbound") continue;
    if (!isImageMessage(m) || attachments.value[m.id]) continue;
    try {
      const r = await api<{ mime: string; data_base64: string }>(
        "/api/attachment/get",
        { message_id: m.id },
      );
      if (r?.data_base64) {
        attachments.value[m.id] =
          `data:${r.mime || "application/octet-stream"};base64,${r.data_base64}`;
      }
    } catch {
      // 附件可能未下载完毕、过大或被清理，忽略即可
    }
  }
}

async function sendManual() {
  if (!manual.value.trim()) return;
  try {
    await api("/api/conversation/send_manual", {
      conversation_id: props.id,
      text: manual.value,
    });
    manual.value = "";
    await load();
  } catch (e: any) {
    errMsg.value = e?.message || String(e);
  }
}

async function exportConv(fmt: "json" | "markdown") {
  const r = await api<{ format: string; content: string }>(
    "/api/conversation/export",
    { conversation_id: props.id, format: fmt },
  );
  const blob = new Blob([r.content], { type: "text/plain" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `conversation-${props.id}.${fmt === "markdown" ? "md" : "json"}`;
  a.click();
  URL.revokeObjectURL(url);
}

async function deleteConv() {
  if (!confirm("确认删除该对话？该对话的所有消息和附件都会被永久删除。")) return;
  try {
    await api("/api/conversation/delete", { conversation_id: props.id });
    router.push("/conversations");
  } catch (e: any) {
    errMsg.value = e?.message || String(e);
  }
}

const grouped = computed(() => messages.value);

watch(() => props.id, load);
onMounted(load);
</script>

<template>
  <div class="px-8 py-8 max-w-3xl mx-auto h-screen flex flex-col">
    <div class="flex items-baseline justify-between mb-4">
      <div>
        <button class="text-sm text-ink-400 hover:text-ink-100" @click="router.back()">← 返回</button>
        <h1 class="text-xl font-semibold mt-1">对话详情</h1>
      </div>
      <div class="flex gap-2">
        <button class="btn-ghost" @click="exportConv('markdown')">导出 MD</button>
        <button class="btn-ghost" @click="exportConv('json')">导出 JSON</button>
        <button class="btn-ghost text-red-400 hover:text-red-300" @click="deleteConv">删除对话</button>
      </div>
    </div>

    <div v-if="errMsg" class="mb-2 text-sm text-red-400">{{ errMsg }}</div>

    <div v-if="summary" class="card p-3 mb-3 bg-ink-900/70 border-ink-800">
      <div class="w-full flex items-center justify-between gap-2">
        <button
          class="flex-1 flex items-center gap-2 text-xs text-ink-400 text-left hover:text-ink-200"
          @click="summaryOpen = !summaryOpen"
        >
          <span>{{ summaryOpen ? "▾" : "▸" }}</span>
          <span>对话记忆摘要（涵盖此时间点之前的对话）</span>
          <span v-if="summaryUpdatedAt" class="text-ink-500">· 更新于 {{ summaryUpdatedAt }}</span>
        </button>
        <button
          class="btn-ghost text-xs shrink-0"
          :disabled="rebuildingSummary"
          @click="rebuildSummary"
        >
          {{ rebuildingSummary ? "重生成中…" : "重新生成" }}
        </button>
      </div>
      <div v-if="summaryOpen" class="mt-2 text-sm text-ink-200 leading-relaxed whitespace-pre-wrap">
        {{ summary }}
      </div>
    </div>

    <div
      ref="scrollEl"
      class="card flex-1 overflow-y-auto p-4 space-y-3"
    >
      <div v-if="loading" class="text-center text-ink-400 py-4">加载中…</div>
      <div v-else-if="grouped.length === 0" class="text-center text-ink-400 py-12">
        还没有消息。
      </div>
      <div v-if="hasMore && !loading" class="flex justify-center">
        <button class="btn-ghost text-xs" :disabled="loadingMore" @click="loadMore">
          {{ loadingMore ? "加载中…" : "加载更多历史" }}
        </button>
      </div>
      <template v-for="m in grouped" :key="m.id">
        <div
          v-if="m.direction === 'tool_call'"
          class="flex justify-center"
        >
          <details class="w-full max-w-[80%] rounded-lg border border-ink-800 bg-ink-900/60 text-[11px]">
            <summary class="px-3 py-1.5 cursor-pointer text-ink-400 hover:text-ink-200 select-none">
              <span class="badge text-[10px] mr-1.5">tool call</span>
              <span class="font-mono text-accent-300">{{ shortToolName(m.tool_name) }}</span>
              <span class="text-ink-500 ml-2">{{ m.created_at }}</span>
            </summary>
            <div class="px-3 py-2 border-t border-ink-800">
              <div class="text-ink-500 mb-1">arguments:</div>
              <pre class="text-ink-300 whitespace-pre-wrap break-words font-mono text-[10px]">{{ prettyJSON(m.tool_args) || "(empty)" }}</pre>
            </div>
          </details>
        </div>
        <div
          v-else-if="m.direction === 'tool_result'"
          class="flex justify-center"
        >
          <details class="w-full max-w-[80%] rounded-lg border border-ink-800 bg-ink-900/60 text-[11px]">
            <summary class="px-3 py-1.5 cursor-pointer text-ink-400 hover:text-ink-200 select-none">
              <span
                class="badge text-[10px] mr-1.5"
                :class="m.status === 'failed' ? 'text-rose-300 border-rose-500/40' : 'text-emerald-300 border-emerald-500/40'"
              >tool {{ m.status === 'failed' ? 'error' : 'result' }}</span>
              <span class="font-mono text-accent-300">{{ shortToolName(m.tool_name) }}</span>
              <span class="text-ink-500 ml-2">{{ m.created_at }}</span>
            </summary>
            <div class="px-3 py-2 border-t border-ink-800">
              <pre class="text-ink-300 whitespace-pre-wrap break-words font-mono text-[10px] max-h-64 overflow-auto">{{ m.tool_result || "(empty)" }}</pre>
            </div>
          </details>
        </div>
        <div
          v-else
          :class="m.direction === 'outbound' ? 'flex justify-end' : 'flex justify-start'"
        >
          <div
            class="max-w-[80%] rounded-2xl px-4 py-2.5 text-sm whitespace-pre-wrap break-words"
            :class="m.direction === 'outbound'
              ? 'bg-accent-500 text-white rounded-br-md'
              : 'bg-ink-800 text-ink-100 rounded-bl-md'"
          >
            <div v-if="attachments[m.id]" class="mb-2">
              <img :src="attachments[m.id]" class="max-w-full max-h-64 rounded-md object-contain" />
            </div>
            <div v-if="m.text">{{ m.text }}</div>
            <div
              class="text-[10px] mt-1 opacity-60"
              :class="m.direction === 'outbound' ? 'text-white/70' : 'text-ink-400'"
            >
              {{ m.created_at }} · {{ m.type }} · {{ m.status }}
            </div>
          </div>
        </div>
      </template>
    </div>

    <div class="card p-3 mt-4 flex gap-2">
      <textarea
        v-model="manual"
        rows="2"
        class="textarea flex-1 resize-none"
        placeholder="原文直接发送（手动模式，不走 LLM；将以 TA 的微信号发出去）"
      />
      <button class="btn-primary" @click="sendManual">发送</button>
    </div>
  </div>
</template>
