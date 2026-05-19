<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { api } from "@/api";

interface MCPServer {
  id: string;
  name: string;
  description: string;
  transport: "stdio" | "streamable_http";
  command: string;
  args: string[];
  env: Record<string, string>;
  url: string;
  headers: Record<string, string>;
  enabled: boolean;
  timeout_ms: number;
  created_at: string;
}

interface TestResult {
  ok: boolean;
  error: string;
  tools: Array<{ name: string; description: string }>;
  pending?: boolean;
}

const items = ref<MCPServer[]>([]);
const err = ref("");
const busy = ref(false);
const testing = ref<string | null>(null);
const testResults = ref<Record<string, TestResult>>({});
const showImport = ref(false);
const importJSON = ref("");
const importReplace = ref(false);
const importResult = ref<{
  imported: number;
  skipped: number;
  errors: string[];
} | null>(null);

const editing = ref<string | null>(null);
const showForm = ref(false);

interface FormState {
  name: string;
  description: string;
  transport: "stdio" | "streamable_http";
  command: string;
  argsText: string;
  envText: string;
  url: string;
  headersText: string;
  enabled: boolean;
  timeout_ms: number;
}

const emptyForm = (): FormState => ({
  name: "",
  description: "",
  transport: "stdio",
  command: "",
  argsText: "",
  envText: "",
  url: "",
  headersText: "",
  enabled: true,
  timeout_ms: 30000,
});

const form = reactive<FormState>(emptyForm());

function reset() {
  Object.assign(form, emptyForm());
  editing.value = null;
  showForm.value = false;
}

function parseLines(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t || t.startsWith("#")) continue;
    const idx = t.indexOf("=");
    if (idx <= 0) continue;
    out[t.slice(0, idx).trim()] = t.slice(idx + 1);
  }
  return out;
}

function formatKV(obj: Record<string, string>): string {
  return Object.entries(obj || {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

async function refresh() {
  const r = await api<{ items: MCPServer[] }>("/api/mcp/list");
  items.value = r.items || [];
}

async function submit() {
  err.value = "";
  busy.value = true;
  try {
    const body: Record<string, unknown> = {
      name: form.name.trim(),
      description: form.description,
      transport: form.transport,
      enabled: form.enabled,
      timeout_ms: form.timeout_ms,
    };
    if (form.transport === "stdio") {
      body.command = form.command.trim();
      body.args = parseLines(form.argsText);
      body.env = parseKV(form.envText);
    } else {
      body.url = form.url.trim();
      body.headers = parseKV(form.headersText);
    }
    if (editing.value) {
      await api("/api/mcp/update", { id: editing.value, ...body });
    } else {
      await api("/api/mcp/create", body);
    }
    reset();
    await refresh();
  } catch (e: any) {
    err.value = e?.message || String(e);
  } finally {
    busy.value = false;
  }
}

function edit(s: MCPServer) {
  editing.value = s.id;
  showForm.value = true;
  Object.assign(form, {
    name: s.name,
    description: s.description,
    transport: s.transport,
    command: s.command,
    argsText: (s.args || []).join("\n"),
    envText: formatKV(s.env || {}),
    url: s.url,
    headersText: formatKV(s.headers || {}),
    enabled: s.enabled,
    timeout_ms: s.timeout_ms || 30000,
  });
}

async function remove(s: MCPServer) {
  if (!confirm(`删除 MCP 服务「${s.name}」？已引用此服务的角色也会自动去掉勾选。`))
    return;
  await api("/api/mcp/delete", { id: s.id });
  await refresh();
}

async function test(s: MCPServer) {
  testing.value = s.id;
  testResults.value = {
    ...testResults.value,
    [s.id]: { ok: false, error: "", tools: [], pending: true },
  };
  try {
    const r = await api<TestResult>("/api/mcp/test", { id: s.id });
    testResults.value = { ...testResults.value, [s.id]: { ...r, pending: false } };
  } catch (e: any) {
    testResults.value = {
      ...testResults.value,
      [s.id]: {
        ok: false,
        error: e?.message || String(e),
        tools: [],
        pending: false,
      },
    };
  } finally {
    testing.value = null;
  }
}

async function runImport() {
  importResult.value = null;
  err.value = "";
  try {
    const r = await api<{ imported: number; skipped: number; errors: string[] }>(
      "/api/mcp/import",
      { json: importJSON.value, replace: importReplace.value },
    );
    importResult.value = r;
    if (r.imported > 0) {
      await refresh();
    }
  } catch (e: any) {
    err.value = e?.message || String(e);
  }
}

const transportPlaceholderJSON = `{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    },
    "remote-search": {
      "type": "streamable_http",
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer xxx" }
    }
  }
}`;

const enabledItems = computed(() => items.value.filter((i) => i.enabled));

onMounted(refresh);
</script>

<template>
  <div class="px-8 py-8 max-w-5xl mx-auto">
    <div class="flex items-baseline justify-between mb-2">
      <h1 class="text-2xl font-semibold">MCP 服务</h1>
      <div class="flex gap-2">
        <button class="btn-ghost" @click="showImport = !showImport">
          {{ showImport ? "收起导入" : "导入 mcpServers JSON" }}
        </button>
        <button
          class="btn-primary"
          @click="
            showForm = !showForm;
            if (!showForm) reset();
          "
        >
          {{ showForm ? "取消" : "新建 MCP" }}
        </button>
      </div>
    </div>
    <p class="text-xs text-ink-400 mb-6">
      Model Context Protocol。让 TA 在对话时可以调用外部工具：本地命令（stdio）或远端 HTTP 服务。
      配置后到「角色」详情页勾选要启用的服务即可。
    </p>

    <div v-if="showImport" class="card p-6 mb-6 space-y-3">
      <div class="text-sm">
        粘贴标准的 <code class="bg-ink-800 px-1 rounded">mcpServers</code> 配置（Claude Desktop / Cursor 通用）。
        支持 <code class="bg-ink-800 px-1 rounded">{"mcpServers": {...}}</code>
        或直接的 <code class="bg-ink-800 px-1 rounded">{...}</code>。
      </div>
      <textarea
        v-model="importJSON"
        rows="10"
        class="textarea font-mono text-xs"
        :placeholder="transportPlaceholderJSON"
      />
      <label class="flex items-center gap-2 text-xs text-ink-300">
        <input v-model="importReplace" type="checkbox" class="rounded" />
        同名服务存在时覆盖（默认跳过）
      </label>
      <div class="flex gap-2 justify-end">
        <button class="btn-primary" :disabled="!importJSON.trim()" @click="runImport">
          导入
        </button>
      </div>
      <div v-if="importResult" class="text-xs">
        已导入 <span class="text-emerald-400">{{ importResult.imported }}</span>
        ，跳过 <span class="text-amber-400">{{ importResult.skipped }}</span>
        <div v-if="importResult.errors.length > 0" class="mt-1 text-rose-400">
          错误：
          <ul class="ml-4 list-disc">
            <li v-for="(e, i) in importResult.errors" :key="i">{{ e }}</li>
          </ul>
        </div>
      </div>
    </div>

    <div v-if="showForm" class="card p-6 mb-6 space-y-4">
      <div class="flex items-baseline justify-between">
        <h2 class="text-lg font-medium">{{ editing ? "编辑 MCP 服务" : "新建 MCP 服务" }}</h2>
        <button v-if="editing" class="btn-ghost text-xs" @click="reset">取消编辑</button>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label class="label">名字</label>
          <input v-model="form.name" class="input mt-1" placeholder="filesystem / search …" />
        </div>
        <div>
          <label class="label">传输方式</label>
          <select v-model="form.transport" class="input mt-1">
            <option value="stdio">stdio（本地子进程）</option>
            <option value="streamable_http">streamable_http（远端 HTTP）</option>
          </select>
        </div>
        <div class="md:col-span-2">
          <label class="label">说明（可选）</label>
          <input v-model="form.description" class="input mt-1" placeholder="一句话解释 TA 调这个工具能做什么" />
        </div>
      </div>

      <template v-if="form.transport === 'stdio'">
        <div>
          <label class="label">command（必填）</label>
          <input v-model="form.command" class="input mt-1" placeholder="npx / python / docker / 绝对路径" />
        </div>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label class="label">args（每行一个）</label>
            <textarea v-model="form.argsText" rows="5" class="textarea mt-1 font-mono text-xs" placeholder="-y&#10;@modelcontextprotocol/server-filesystem&#10;/Users/me/notes" />
          </div>
          <div>
            <label class="label">环境变量（KEY=VAL，每行一个）</label>
            <textarea v-model="form.envText" rows="5" class="textarea mt-1 font-mono text-xs" placeholder="API_KEY=sk-xxx&#10;LOG_LEVEL=info" />
          </div>
        </div>
      </template>

      <template v-else>
        <div>
          <label class="label">URL（必填）</label>
          <input v-model="form.url" class="input mt-1" placeholder="https://example.com/mcp" />
        </div>
        <div>
          <label class="label">请求头（KEY=VAL，每行一个，常用 Authorization）</label>
          <textarea v-model="form.headersText" rows="4" class="textarea mt-1 font-mono text-xs" placeholder="Authorization=Bearer xxx" />
        </div>
      </template>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label class="label">单次工具调用超时（毫秒）</label>
          <input
            v-model.number="form.timeout_ms"
            type="number"
            min="1000"
            max="600000"
            class="input mt-1"
          />
        </div>
        <label class="flex items-center gap-2 text-sm text-ink-300 mt-6">
          <input v-model="form.enabled" type="checkbox" class="rounded" />
          启用（关闭后所有角色都不再调用此服务）
        </label>
      </div>

      <div v-if="err" class="text-rose-400 text-sm">{{ err }}</div>

      <div class="flex justify-end gap-2">
        <button class="btn-ghost" @click="reset">取消</button>
        <button class="btn-primary" :disabled="busy" @click="submit">
          {{ editing ? "保存" : "添加" }}
        </button>
      </div>
    </div>

    <div v-if="items.length === 0" class="card p-12 text-center text-ink-400">
      还没有任何 MCP 服务。点击右上角新建，或直接「导入 mcpServers JSON」。
    </div>

    <div v-else class="space-y-3">
      <div v-for="s in items" :key="s.id" class="card p-5">
        <div class="flex items-start gap-3">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 flex-wrap">
              <div class="font-medium">{{ s.name }}</div>
              <span class="badge text-[10px]">{{ s.transport }}</span>
              <span v-if="!s.enabled" class="badge text-[10px] text-amber-300 border-amber-500/40">已禁用</span>
            </div>
            <div v-if="s.description" class="text-xs text-ink-400 mt-1">{{ s.description }}</div>
            <div class="text-xs text-ink-500 mt-1 font-mono truncate">
              <template v-if="s.transport === 'stdio'">
                <span class="text-ink-400">$ </span>{{ s.command }} {{ (s.args || []).join(" ") }}
              </template>
              <template v-else>{{ s.url }}</template>
            </div>
          </div>
          <div class="flex gap-2 flex-shrink-0">
            <button class="btn-ghost" :disabled="testing === s.id" @click="test(s)">
              {{ testing === s.id ? "测试中…" : "测试" }}
            </button>
            <button class="btn-ghost" @click="edit(s)">编辑</button>
            <button class="btn-ghost" @click="remove(s)">删除</button>
          </div>
        </div>
        <div v-if="testResults[s.id]" class="text-xs mt-3">
          <div v-if="testResults[s.id].pending" class="text-ink-400">连接中…</div>
          <div v-else-if="testResults[s.id].ok" class="text-emerald-400">
            连通正常，可用工具 {{ testResults[s.id].tools.length }} 个
            <ul
              v-if="testResults[s.id].tools.length > 0"
              class="mt-1 grid grid-cols-1 md:grid-cols-2 gap-1"
            >
              <li v-for="t in testResults[s.id].tools" :key="t.name" class="text-ink-400">
                <span class="text-ink-300 font-mono">{{ t.name }}</span>
                <span v-if="t.description" class="ml-1 text-ink-500">— {{ t.description }}</span>
              </li>
            </ul>
          </div>
          <div v-else class="text-rose-400">连接失败：{{ testResults[s.id].error }}</div>
        </div>
      </div>
    </div>

    <div v-if="enabledItems.length > 0" class="mt-6 text-xs text-ink-500">
      共 {{ enabledItems.length }} 个 MCP 服务可被角色启用。
    </div>
  </div>
</template>
