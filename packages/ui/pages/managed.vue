<script setup lang="ts">
interface NodeChoice {
  name: string
  label: string
  type: string
  local?: boolean
}

interface ChainPreview {
  id: string
  transit?: string
  type?: string
  authenticated: boolean
  hops?: string[]
}

interface ChainForm {
  key: number
  id: string
  transit: string
  input: 'nodes' | 'keep' | 'import' | 'fields'
  hops: string[]
  import: string
  type: string
  host: string
  port: number
  username: string
  password: string
  saved: boolean
}

interface RuleForm {
  chain: string
  domains: string
  sources: string
  presets: string[]
}

interface ManagedState {
  version?: string
  writable: boolean
  canPreview: boolean
  diagnostic?: string
  chains?: ChainPreview[]
  rules?: {
    chain: string
    domains?: string[]
    sources?: string[]
    presets?: string[]
  }[]
  nodes?: NodeChoice[]
  history?: { id: string; created: string }[]
  subscriptions?: { id: string; refreshed?: string; nodes: NodeChoice[] }[]
  audit?: { Time: string; Action: string; Result: string }[]
}

type SubscriptionAction =
  'import' | 'refresh' | 'remove' | 'node-add' | 'node-remove'

const tabs = [
  { id: 'subscriptions', label: '订阅与节点' },
  { id: 'chains', label: '链式代理' },
  { id: 'history', label: '版本记录' },
] as const
const tab = ref<(typeof tabs)[number]['id']>('subscriptions')
const state = ref<ManagedState>()
const pending = ref(false)
const notice = ref('')
const failed = ref(false)
const chains = ref<ChainForm[]>([])
const rules = ref<RuleForm[]>([])
const preview = ref<ChainPreview[]>([])
const draft = ref('')
const rollbackTarget = ref('')
const subscriptionURL = ref('')
const replacementURL = ref('')
const socks = ref('')
const editingSubscription = ref('')
const nodeQuery = ref('')
let chainKey = 0

const nodes = computed(() => [
  ...(state.value?.nodes ?? []).map((node) => ({
    ...node,
    source: '服务器配置',
  })),
  ...(state.value?.subscriptions ?? []).flatMap((subscription) =>
    subscription.nodes.map((node) => ({ ...node, source: subscription.id })),
  ),
])
const nodeLabels = computed(
  () => new Map(nodes.value.map((node) => [node.name, node.label])),
)
const selectedSubscription = computed(() =>
  state.value?.subscriptions?.find(
    (item) => item.id === editingSubscription.value,
  ),
)
const editedNodes = computed(() => {
  const term = nodeQuery.value.trim().toLowerCase()
  return (selectedSubscription.value?.nodes ?? []).filter(
    (node) => !term || node.label.toLowerCase().includes(term),
  )
})

function show(message: string, error = false) {
  notice.value = message
  failed.value = error
}

function makeChain(id: string, saved?: ChainPreview): ChainForm {
  return {
    key: ++chainKey,
    id,
    transit: saved?.transit ?? '',
    input: saved && !saved.hops ? 'keep' : 'nodes',
    hops: saved?.hops ? [...saved.hops] : [],
    import: '',
    type: saved?.type ?? 'socks5',
    host: '',
    port: 1080,
    username: '',
    password: '',
    saved: !!saved && !saved.hops,
  }
}

async function loadState(preserveEditor = false) {
  try {
    const response = await fetch('/api/managed/state')
    const result = await response.json()
    if (!response.ok) {
      show(result.error, true)
      return false
    }
    const prior = state.value
    state.value = result
    rollbackTarget.value = ''
    draft.value = ''
    preview.value = []
    if (
      preserveEditor &&
      prior &&
      JSON.stringify([prior.chains, prior.rules]) ===
        JSON.stringify([result.chains, result.rules])
    )
      return true
    chains.value = (state.value?.chains ?? []).map((chain) =>
      makeChain(chain.id, chain),
    )
    rules.value = (state.value?.rules ?? []).map((rule) => ({
      chain: rule.chain,
      domains: (rule.domains ?? []).join(' '),
      sources: (rule.sources ?? []).join(' '),
      presets: rule.presets ?? [],
    }))
    return true
  } catch {
    show('无法读取托管状态。', true)
    return false
  }
}

onMounted(() => loadState())
watch(
  [chains, rules],
  () => {
    draft.value = ''
    preview.value = []
  },
  { deep: true },
)

function editNodes(id: string) {
  editingSubscription.value = editingSubscription.value === id ? '' : id
  socks.value = ''
  replacementURL.value = ''
  nodeQuery.value = ''
}

async function updateSubscription(
  action: SubscriptionAction,
  id = '',
  input: { url?: string; import?: string; name?: string } = {},
) {
  if (!state.value?.writable || pending.value) return
  pending.value = true
  draft.value = ''
  preview.value = []
  show('')
  try {
    const response = await fetch(`/api/managed/subscription-${action}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ version: state.value.version, id, ...input }),
    })
    if (!response.ok) {
      show((await response.json()).error, true)
      if (response.status === 409) await loadState(true)
      return
    }
    if (action === 'remove' && editingSubscription.value === id) editNodes(id)
    if (await loadState(true)) {
      show(
        {
          import: id
            ? '订阅已更新。'
            : '订阅已导入，可以编辑节点或添加到链路。',
          refresh: '订阅已刷新。',
          remove: '订阅已删除。',
          'node-add': 'SOCKS5 节点已添加。',
          'node-remove': 'SOCKS5 节点已删除。',
        }[action],
      )
    }
  } catch {
    show('订阅请求中断。请重新读取状态确认结果。', true)
  } finally {
    subscriptionURL.value = ''
    replacementURL.value = ''
    socks.value = ''
    pending.value = false
  }
}

function addChain() {
  let number = 1
  while (chains.value.some((chain) => chain.id === `chain-${number}`)) number++
  chains.value.push(makeChain(`chain-${number}`))
}

const split = (value: string) => value.split(/[\s,]+/).filter(Boolean)

async function submitPreview() {
  if (pending.value || !state.value?.canPreview) return
  pending.value = true
  show('')
  draft.value = ''
  preview.value = []
  const payload = {
    version: state.value.version,
    chains: chains.value.map((chain) => ({
      id: chain.id,
      ...(chain.input === 'nodes'
        ? { hops: chain.hops }
        : {
            transit: chain.transit,
            ...(chain.input === 'import' && { import: chain.import }),
            ...(chain.input === 'fields' && {
              proxy: {
                type: chain.type,
                host: chain.host,
                port: chain.port,
                username: chain.username,
                password: chain.password,
              },
            }),
          }),
    })),
    rules: rules.value.map((rule) => ({
      ...rule,
      domains: split(rule.domains),
      sources: split(rule.sources),
    })),
  }
  for (const chain of chains.value) {
    chain.import = ''
    chain.host = ''
    chain.username = ''
    chain.password = ''
  }
  try {
    const response = await fetch('/api/managed/preview', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
    const result = await response.json()
    if (!response.ok) show(result.error, true)
    else {
      preview.value = result.chains
      draft.value = result.draft ?? ''
      show(draft.value ? '预览通过，请在五分钟内发布。' : '预览通过。')
    }
  } catch {
    show('无法连接服务器面板。', true)
  } finally {
    pending.value = false
  }
}

async function apply(action: 'publish' | 'rollback', id: string) {
  if (!state.value?.writable || !id || pending.value) return
  pending.value = true
  show('')
  try {
    const response = await fetch(`/api/managed/${action}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ version: state.value.version, id }),
    })
    if (!response.ok) show((await response.json()).error, true)
    else if (await loadState()) {
      show(
        action === 'publish'
          ? '发布完成，运行态已验证。'
          : '回滚完成，运行态已验证。',
      )
    }
  } catch {
    show('发布请求中断。请重新读取状态确认结果。', true)
  } finally {
    pending.value = false
  }
}
</script>

<template>
  <div class="mx-auto grid max-w-6xl gap-4">
    <h1 class="text-2xl font-semibold">托管配置</h1>
    <p v-if="state?.diagnostic" class="alert alert-warning" role="alert">
      {{ state.diagnostic }}
    </p>
    <p v-else-if="state && !state.canPreview" class="alert">
      当前实例以只读模式运行。
    </p>
    <p
      v-if="notice"
      role="status"
      class="alert"
      :class="failed ? 'alert-error' : 'alert-success'"
    >
      {{ notice }}
    </p>
    <button
      v-if="!state || failed"
      type="button"
      class="btn w-fit btn-sm"
      :disabled="pending"
      @click="loadState(true)"
    >
      重新读取托管状态
    </button>

    <template v-if="state?.canPreview || state?.version">
      <nav
        class="flex flex-wrap gap-1 border-b border-base-300 pb-2"
        aria-label="托管配置"
      >
        <button
          v-for="item in tabs"
          :key="item.id"
          type="button"
          class="btn btn-ghost btn-sm"
          :class="{ 'btn-active': tab === item.id }"
          :aria-pressed="tab === item.id"
          @click="tab = item.id"
        >
          {{ item.label }}
        </button>
      </nav>

      <fieldset
        v-if="tab === 'subscriptions'"
        :disabled="pending || !state.writable"
        class="grid gap-4"
      >
        <form
          class="flex flex-wrap items-end gap-2"
          autocomplete="off"
          @submit.prevent="
            updateSubscription('import', '', { url: subscriptionURL.trim() })
          "
        >
          <label class="grid max-w-xl min-w-52 flex-1 gap-1">
            HTTPS 订阅链接
            <input
              v-model="subscriptionURL"
              type="password"
              class="input w-full"
              required
              placeholder="粘贴 Clash YAML 订阅链接"
              autocomplete="off"
            />
          </label>
          <button class="btn btn-primary" :disabled="!subscriptionURL.trim()">
            导入订阅
          </button>
        </form>

        <p v-if="!state.subscriptions?.length" class="text-sm opacity-70">
          还没有导入订阅。
        </p>
        <ul v-else class="grid gap-2">
          <li
            v-for="item in state.subscriptions"
            :key="item.id"
            class="flex flex-wrap items-center gap-2 rounded-box border border-base-300 px-3 py-2"
            :class="{ 'border-primary': editingSubscription === item.id }"
          >
            <div class="mr-auto min-w-36">
              <strong>{{ item.id }}</strong>
              <p class="text-xs opacity-70">
                {{ item.nodes.length }} 个节点
                <template v-if="item.refreshed">
                  · {{ new Date(item.refreshed).toLocaleString() }}</template
                >
              </p>
            </div>
            <button
              type="button"
              class="btn btn-sm"
              :aria-expanded="editingSubscription === item.id"
              @click="editNodes(item.id)"
            >
              编辑节点
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              @click="updateSubscription('refresh', item.id)"
            >
              刷新
            </button>
          </li>
        </ul>

        <section
          v-if="selectedSubscription"
          class="grid gap-3 rounded-box border border-base-300 bg-base-200/60 p-4"
        >
          <h2 class="font-semibold">{{ selectedSubscription.id }} 的节点</h2>
          <form
            class="flex flex-wrap items-end gap-2"
            autocomplete="off"
            @submit.prevent="
              updateSubscription('node-add', editingSubscription, {
                import: socks.trim(),
              })
            "
          >
            <label class="grid max-w-xl min-w-52 flex-1 gap-1">
              SOCKS5
              <input
                v-model="socks"
                type="password"
                class="input w-full"
                required
                autocomplete="off"
                placeholder="socks5://用户名:密码@主机:端口"
              />
            </label>
            <button class="btn btn-primary" :disabled="!socks.trim()">
              添加节点
            </button>
          </form>
          <input
            v-model="nodeQuery"
            type="search"
            aria-label="搜索订阅节点"
            class="input w-full max-w-xs input-sm"
            placeholder="搜索订阅节点"
          />
          <ul
            class="max-h-72 divide-y divide-base-300 overflow-auto rounded-box bg-base-100"
          >
            <li
              v-for="node in editedNodes"
              :key="node.name"
              class="flex min-h-10 items-center gap-2 px-3 py-1"
            >
              <span class="mr-auto truncate" :title="node.label">{{
                node.label
              }}</span>
              <span class="text-xs opacity-70">{{ node.type }}</span>
              <span v-if="node.local" class="text-xs opacity-70">手动添加</span>
              <button
                v-if="node.local"
                type="button"
                class="btn btn-ghost btn-xs"
                :aria-label="`删除 ${node.label}`"
                @click="
                  updateSubscription('node-remove', editingSubscription, {
                    name: node.name,
                  })
                "
              >
                删除
              </button>
            </li>
          </ul>
          <p v-if="!editedNodes.length" class="text-sm opacity-70">
            没有匹配的节点。
          </p>
          <details class="text-sm">
            <summary class="cursor-pointer">订阅设置</summary>
            <form
              class="mt-2 flex flex-wrap items-end gap-2"
              autocomplete="off"
              @submit.prevent="
                updateSubscription('import', editingSubscription, {
                  url: replacementURL.trim(),
                })
              "
            >
              <label class="grid max-w-xl min-w-52 flex-1 gap-1">
                替换订阅链接
                <input
                  v-model="replacementURL"
                  type="password"
                  class="input w-full input-sm"
                  autocomplete="off"
                  required
                />
              </label>
              <button class="btn btn-sm" :disabled="!replacementURL.trim()">
                保存并刷新
              </button>
              <button
                type="button"
                class="btn btn-ghost text-error btn-sm"
                @click="updateSubscription('remove', editingSubscription)"
              >
                删除订阅
              </button>
            </form>
          </details>
        </section>
      </fieldset>

      <form
        v-if="tab === 'chains'"
        autocomplete="off"
        @submit.prevent="submitPreview"
      >
        <fieldset :disabled="pending" class="grid gap-4">
          <div class="flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="btn btn-primary btn-sm"
              @click="addChain"
            >
              添加链路
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              @click="tab = 'subscriptions'"
            >
              管理节点
            </button>
          </div>
          <p v-if="!chains.length" class="text-sm opacity-70">
            添加链路后，按入口到出口的顺序选择节点。
          </p>
          <section
            v-for="(chain, index) in chains"
            :key="chain.key"
            class="grid gap-3 rounded-box border border-base-300 p-4"
          >
            <div class="flex flex-wrap items-end gap-2">
              <label class="mr-auto grid gap-1">
                链路名称
                <input
                  v-model="chain.id"
                  class="input input-sm"
                  pattern="[a-zA-Z0-9][a-zA-Z0-9_\-]{0,47}"
                  required
                />
              </label>
              <button
                v-if="chain.input === 'nodes'"
                type="button"
                class="btn btn-ghost btn-sm"
                @click="chain.input = chain.saved ? 'keep' : 'import'"
              >
                手动录入出口
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-sm"
                @click="chains.splice(index, 1)"
              >
                删除链路
              </button>
            </div>
            <ManagedChainPicker
              v-if="chain.input === 'nodes'"
              v-model="chain.hops"
              :nodes="nodes"
            />
            <div v-else class="grid gap-2 sm:grid-cols-2">
              <label class="grid gap-1">
                固定中转节点
                <input v-model="chain.transit" class="input w-full" required />
              </label>
              <label class="grid gap-1">
                家宽录入
                <select v-model="chain.input" class="select w-full">
                  <option v-if="chain.saved" value="keep">
                    保留已保存的凭据
                  </option>
                  <option value="import">替换代理字符串</option>
                  <option value="fields">替换代理字段</option>
                  <option value="nodes">从节点列表选择</option>
                </select>
              </label>
              <label v-if="chain.input === 'import'" class="grid gap-1">
                代理字符串
                <input
                  v-model="chain.import"
                  class="input w-full"
                  type="password"
                  required
                />
              </label>
              <template v-if="chain.input === 'fields'">
                <label class="grid gap-1">
                  协议
                  <select v-model="chain.type" class="select w-full">
                    <option>socks5</option>
                    <option>http</option>
                    <option>https</option>
                  </select>
                </label>
                <label class="grid gap-1"
                  >主机<input
                    v-model="chain.host"
                    class="input w-full"
                    required
                /></label>
                <label class="grid gap-1"
                  >端口<input
                    v-model.number="chain.port"
                    class="input w-full"
                    type="number"
                    min="1"
                    max="65535"
                    required
                /></label>
                <label class="grid gap-1"
                  >用户名<input v-model="chain.username" class="input w-full"
                /></label>
                <label class="grid gap-1"
                  >密码<input
                    v-model="chain.password"
                    class="input w-full"
                    type="password"
                /></label>
              </template>
            </div>
          </section>

          <section class="grid gap-3">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h2 class="text-lg font-semibold">域名和来源规则</h2>
              <button
                type="button"
                class="btn btn-sm"
                :disabled="!chains.length"
                @click="
                  rules.push({
                    chain: chains[0]?.id ?? '',
                    domains: '',
                    sources: '',
                    presets: [],
                  })
                "
              >
                添加规则
              </button>
            </div>
            <fieldset
              v-for="(rule, index) in rules"
              :key="index"
              class="grid gap-3 rounded-box border border-base-300 bg-base-200/60 p-4 sm:grid-cols-2"
            >
              <label class="grid gap-1">
                目标链路
                <select v-model="rule.chain" class="select w-full" required>
                  <option disabled value="">选择链路</option>
                  <option
                    v-for="chain in chains"
                    :key="chain.key"
                    :value="chain.id"
                  >
                    {{ chain.id }}
                  </option>
                </select>
              </label>
              <div class="flex flex-wrap items-end gap-4 pb-2">
                <label
                  ><input
                    v-model="rule.presets"
                    type="checkbox"
                    class="checkbox checkbox-sm align-middle"
                    value="openai"
                  />
                  OpenAI</label
                >
                <label
                  ><input
                    v-model="rule.presets"
                    type="checkbox"
                    class="checkbox checkbox-sm align-middle"
                    value="antigravity"
                  />
                  Antigravity</label
                >
              </div>
              <label class="grid gap-1"
                >域名后缀<input
                  v-model="rule.domains"
                  class="input w-full"
                  placeholder="example.com example.org"
              /></label>
              <label class="grid gap-1"
                >来源网段<input
                  v-model="rule.sources"
                  class="input w-full"
                  placeholder="192.0.2.0/24"
              /></label>
              <button
                type="button"
                class="btn w-fit btn-ghost btn-sm"
                @click="rules.splice(index, 1)"
              >
                删除规则
              </button>
            </fieldset>
          </section>
          <div class="flex flex-wrap gap-2">
            <button
              class="btn btn-primary"
              :disabled="
                chains.some(
                  (chain) => chain.input === 'nodes' && chain.hops.length < 2,
                )
              "
            >
              脱敏预览
            </button>
            <button
              v-if="draft && state.writable"
              type="button"
              class="btn btn-warning"
              @click="apply('publish', draft)"
            >
              发布预览
            </button>
          </div>
          <ul
            v-if="preview.length"
            class="grid gap-2 rounded-box border border-base-300 p-4"
          >
            <li v-for="chain in preview" :key="chain.id">
              <strong>{{ chain.id }}</strong>
              <span v-if="chain.hops">
                ·
                {{
                  chain.hops
                    .map((hop) => nodeLabels.get(hop) ?? hop)
                    .join(' → ')
                }}</span
              >
              <span v-else>
                · {{ chain.transit }} → {{ chain.type }} ·
                {{ chain.authenticated ? '需认证' : '无需认证' }}</span
              >
            </li>
          </ul>
        </fieldset>
      </form>

      <section v-if="tab === 'history'" class="grid gap-4">
        <p v-if="!state.history?.length" class="text-sm opacity-70">
          暂无可回滚版本。
        </p>
        <div v-else class="flex flex-wrap items-end gap-2">
          <label class="grid gap-1">
            回滚版本
            <select
              v-model="rollbackTarget"
              class="select w-full"
              :disabled="pending || !state.writable"
            >
              <option disabled value="">选择历史版本</option>
              <option
                v-for="item in state.history"
                :key="item.id"
                :value="item.id"
              >
                {{ new Date(item.created).toLocaleString() }} ·
                {{ item.id.slice(0, 8) }}
              </option>
            </select>
          </label>
          <button
            type="button"
            class="btn"
            :disabled="!state.writable || pending || !rollbackTarget"
            @click="apply('rollback', rollbackTarget)"
          >
            回滚并验证
          </button>
        </div>
        <details
          v-if="state.audit?.length"
          class="rounded-box border border-base-300 p-3"
        >
          <summary>最近操作记录</summary>
          <ul class="grid gap-2 pt-2 text-sm">
            <li v-for="event in [...state.audit].reverse()" :key="event.Time">
              {{ new Date(event.Time).toLocaleString() }} · {{ event.Action }} ·
              {{ event.Result }}
            </li>
          </ul>
        </details>
      </section>
    </template>
  </div>
</template>
