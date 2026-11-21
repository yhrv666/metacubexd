<script setup lang="ts">
import {
  IconAlertTriangle,
  IconCheck,
  IconCloudDownload,
  IconEye,
  IconHistory,
  IconLink,
  IconListDetails,
  IconPlus,
  IconRefresh,
  IconRocket,
  IconRotateClockwise2,
  IconSearch,
  IconSettings,
  IconShieldLock,
  IconTrash,
} from '@tabler/icons-vue'

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
  { id: 'subscriptions', label: '订阅与节点', icon: IconCloudDownload },
  { id: 'chains', label: '链式代理', icon: IconLink },
  { id: 'history', label: '版本记录', icon: IconHistory },
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
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-6 pb-8">
    <div
      class="flex flex-wrap items-center justify-between gap-4 border-b border-base-content/10 pb-4"
    >
      <div class="flex flex-wrap items-center gap-3">
        <h1 class="text-2xl font-bold tracking-tight">托管配置</h1>
        <span
          v-if="state"
          class="badge gap-1.5 badge-sm"
          :class="
            state.writable
              ? 'badge-soft badge-success'
              : 'badge-soft badge-neutral'
          "
        >
          <span
            class="h-1.5 w-1.5 rounded-full"
            :class="state.writable ? 'bg-success' : 'bg-base-content/50'"
            aria-hidden="true"
          />
          {{ state.writable ? '可写入' : '只读' }}
        </span>
        <span
          v-if="state?.version"
          class="rounded-md bg-base-200 px-2 py-0.5 font-mono text-xs text-base-content/65"
        >
          {{ state.version.slice(0, 8) }}
        </span>
      </div>
      <button
        v-if="!state || failed"
        type="button"
        class="btn gap-1.5 btn-outline btn-sm"
        :disabled="pending"
        @click="loadState(true)"
      >
        <IconRefresh class="h-4 w-4" aria-hidden="true" />
        重新读取托管状态
      </button>
    </div>

    <div
      v-if="state?.diagnostic"
      class="alert rounded-xl alert-soft text-sm alert-warning"
      role="alert"
    >
      <IconAlertTriangle class="h-4 w-4 shrink-0" aria-hidden="true" />
      <span>{{ state.diagnostic }}</span>
    </div>
    <div
      v-else-if="state && !state.canPreview"
      class="alert rounded-xl border border-base-content/10 bg-base-200/70 text-sm"
    >
      <IconShieldLock class="h-4 w-4 shrink-0 opacity-70" aria-hidden="true" />
      <span>当前实例以只读模式运行。</span>
    </div>
    <div
      v-if="notice"
      role="status"
      class="alert rounded-xl alert-soft text-sm"
      :class="failed ? 'alert-error' : 'alert-success'"
    >
      <IconAlertTriangle
        v-if="failed"
        class="h-4 w-4 shrink-0"
        aria-hidden="true"
      />
      <IconCheck v-else class="h-4 w-4 shrink-0" aria-hidden="true" />
      <span>{{ notice }}</span>
    </div>

    <template v-if="state?.canPreview || state?.version">
      <nav
        class="inline-flex w-fit flex-wrap gap-1 rounded-xl border border-base-content/10 bg-base-200/70 p-1"
        aria-label="托管配置"
      >
        <button
          v-for="item in tabs"
          :key="item.id"
          type="button"
          class="btn gap-1.5 rounded-lg btn-ghost px-3.5 font-medium btn-sm"
          :class="
            tab === item.id
              ? 'bg-base-100 text-primary shadow-xs'
              : 'text-base-content/70 hover:text-base-content'
          "
          :aria-pressed="tab === item.id"
          @click="tab = item.id"
        >
          <component :is="item.icon" class="h-4 w-4" aria-hidden="true" />
          {{ item.label }}
        </button>
      </nav>

      <fieldset
        v-if="tab === 'subscriptions'"
        :disabled="pending || !state.writable"
        class="grid items-start gap-6 lg:grid-cols-12"
      >
        <div class="grid gap-5 lg:col-span-5">
          <section
            class="grid gap-4 rounded-2xl border border-base-content/10 bg-base-200/40 p-5"
          >
            <h2 class="text-base font-semibold">导入订阅</h2>
            <form
              class="grid gap-3"
              autocomplete="off"
              @submit.prevent="
                updateSubscription('import', '', {
                  url: subscriptionURL.trim(),
                })
              "
            >
              <label class="grid gap-1.5 text-sm font-medium">
                <span>HTTPS 订阅链接</span>
                <input
                  v-model="subscriptionURL"
                  type="password"
                  class="input-bordered input w-full"
                  required
                  placeholder="粘贴 Clash YAML 订阅链接"
                  autocomplete="off"
                />
              </label>
              <button
                class="btn w-full btn-primary sm:w-fit"
                :disabled="!subscriptionURL.trim()"
              >
                <IconCloudDownload class="h-4 w-4" aria-hidden="true" />
                导入订阅
              </button>
            </form>
          </section>

          <section
            class="grid gap-3 rounded-2xl border border-base-content/10 bg-base-200/40 p-5"
          >
            <div class="flex items-center justify-between gap-2">
              <h2 class="text-base font-semibold">已导入订阅</h2>
              <span class="text-xs text-base-content/55 tabular-nums">
                {{ state.subscriptions?.length ?? 0 }}
              </span>
            </div>
            <p
              v-if="!state.subscriptions?.length"
              class="rounded-xl border border-dashed border-base-content/12 py-6 text-center text-sm text-base-content/60"
            >
              还没有导入订阅。
            </p>
            <ul v-else class="grid gap-2.5">
              <li
                v-for="item in state.subscriptions"
                :key="item.id"
                class="flex flex-wrap items-center gap-3 rounded-xl border border-base-content/10 bg-base-100 px-4 py-3 transition-colors"
                :class="{
                  'border-primary/60 bg-primary/5 ring-1 ring-primary/20':
                    editingSubscription === item.id,
                }"
              >
                <div class="mr-auto min-w-36">
                  <strong class="block text-sm font-semibold">
                    {{ item.id }}
                  </strong>
                  <p class="mt-0.5 text-xs text-base-content/60 tabular-nums">
                    {{ item.nodes.length }} 个节点
                    <template v-if="item.refreshed">
                      · {{ new Date(item.refreshed).toLocaleString() }}
                    </template>
                  </p>
                </div>
                <div class="flex items-center gap-1.5">
                  <button
                    type="button"
                    class="btn btn-sm"
                    :class="
                      editingSubscription === item.id
                        ? 'btn-soft btn-primary'
                        : 'border-base-content/15 btn-outline'
                    "
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
                </div>
              </li>
            </ul>
          </section>
        </div>

        <section
          v-if="selectedSubscription"
          class="grid gap-4 rounded-2xl border border-base-content/10 bg-base-200/40 p-5 sm:p-6 lg:col-span-7"
        >
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h2 class="text-base font-semibold">
              {{ selectedSubscription.id }} 的节点
            </h2>
            <span class="badge badge-ghost badge-sm tabular-nums">
              {{ editedNodes.length }} 个节点
            </span>
          </div>

          <form
            class="flex flex-wrap items-end gap-3 rounded-xl border border-base-content/8 bg-base-100/80 p-4"
            autocomplete="off"
            @submit.prevent="
              updateSubscription('node-add', editingSubscription, {
                import: socks.trim(),
              })
            "
          >
            <label class="grid min-w-52 flex-1 gap-1.5 text-sm font-medium">
              <span>SOCKS5</span>
              <input
                v-model="socks"
                type="password"
                class="input-bordered input w-full"
                required
                autocomplete="off"
                placeholder="socks5://用户名:密码@主机:端口"
              />
            </label>
            <button class="btn btn-primary" :disabled="!socks.trim()">
              <IconPlus class="h-4 w-4" aria-hidden="true" />
              添加节点
            </button>
          </form>

          <div class="relative max-w-sm">
            <IconSearch
              class="pointer-events-none absolute top-1/2 left-3 z-10 h-4 w-4 -translate-y-1/2 text-base-content/45"
              aria-hidden="true"
            />
            <input
              v-model="nodeQuery"
              type="search"
              aria-label="搜索订阅节点"
              class="input-bordered input w-full pl-9 input-sm"
              placeholder="搜索订阅节点"
            />
          </div>

          <ul
            class="max-h-96 divide-y divide-base-content/8 overflow-auto rounded-xl border border-base-content/10 bg-base-100"
          >
            <li
              v-for="node in editedNodes"
              :key="node.name"
              class="flex min-h-11 items-center gap-2.5 px-4 py-2 text-sm"
            >
              <span class="mr-auto truncate font-medium" :title="node.label">{{
                node.label
              }}</span>
              <span class="badge badge-ghost font-mono badge-xs uppercase">
                {{ node.type }}
              </span>
              <span
                v-if="node.local"
                class="badge badge-soft badge-xs badge-primary"
              >
                手动添加
              </span>
              <button
                v-if="node.local"
                type="button"
                class="btn btn-ghost text-base-content/65 btn-xs hover:text-error"
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
          <p
            v-if="!editedNodes.length"
            class="rounded-xl border border-dashed border-base-content/12 py-6 text-center text-sm text-base-content/60"
          >
            没有匹配的节点。
          </p>

          <details
            class="rounded-xl border border-base-content/10 bg-base-100/70 p-4 text-sm"
          >
            <summary
              class="flex cursor-pointer items-center gap-2 font-medium select-none"
            >
              <IconSettings class="h-4 w-4 opacity-70" aria-hidden="true" />
              <span>订阅设置</span>
            </summary>
            <form
              class="mt-4 flex flex-wrap items-end gap-3"
              autocomplete="off"
              @submit.prevent="
                updateSubscription('import', editingSubscription, {
                  url: replacementURL.trim(),
                })
              "
            >
              <label class="grid min-w-52 flex-1 gap-1.5 text-sm">
                <span>替换订阅链接</span>
                <input
                  v-model="replacementURL"
                  type="password"
                  class="input-bordered input w-full input-sm"
                  autocomplete="off"
                  required
                />
              </label>
              <button class="btn btn-sm" :disabled="!replacementURL.trim()">
                保存并刷新
              </button>
              <button
                type="button"
                class="btn gap-1 btn-ghost text-error btn-sm"
                @click="updateSubscription('remove', editingSubscription)"
              >
                <IconTrash class="h-3.5 w-3.5" aria-hidden="true" />
                删除订阅
              </button>
            </form>
          </details>
        </section>

        <section
          v-else-if="state.subscriptions?.length"
          class="flex min-h-56 items-center justify-center rounded-2xl border border-dashed border-base-content/15 bg-base-200/20 p-8 text-center text-sm text-base-content/60 lg:col-span-7"
        >
          点左侧订阅旁的「编辑节点」可查看节点列表或粘贴添加 SOCKS5 出口。
        </section>
      </fieldset>

      <form
        v-if="tab === 'chains'"
        autocomplete="off"
        @submit.prevent="submitPreview"
      >
        <fieldset :disabled="pending" class="grid gap-6">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap items-center gap-2.5">
              <button
                type="button"
                class="btn gap-1.5 btn-primary btn-sm"
                @click="addChain"
              >
                <IconPlus class="h-4 w-4" aria-hidden="true" />
                添加链路
              </button>
              <button
                type="button"
                class="btn gap-1.5 border-base-content/15 btn-outline btn-sm"
                @click="tab = 'subscriptions'"
              >
                <IconListDetails class="h-4 w-4" aria-hidden="true" />
                管理节点
              </button>
            </div>
            <span
              v-if="chains.length"
              class="text-xs text-base-content/60 tabular-nums"
            >
              {{ chains.length }} 条链路 · {{ rules.length }} 条规则
            </span>
          </div>

          <p
            v-if="!chains.length"
            class="rounded-2xl border border-dashed border-base-content/15 bg-base-200/25 px-6 py-10 text-center text-sm text-base-content/65"
          >
            添加链路后，按入口到出口的顺序选择节点。
          </p>

          <section
            v-for="(chain, index) in chains"
            :key="chain.key"
            class="grid gap-5 rounded-2xl border border-base-content/10 bg-base-200/35 p-5 sm:p-6"
          >
            <div
              class="flex flex-wrap items-end gap-3 border-b border-base-content/8 pb-4"
            >
              <label class="mr-auto grid gap-1.5 text-sm font-medium">
                <span>链路名称</span>
                <input
                  v-model="chain.id"
                  class="input-bordered input w-56 font-mono input-sm"
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
                class="btn gap-1 btn-ghost text-base-content/70 btn-sm hover:text-error"
                @click="chains.splice(index, 1)"
              >
                <IconTrash class="h-3.5 w-3.5" aria-hidden="true" />
                删除链路
              </button>
            </div>

            <ManagedChainPicker
              v-if="chain.input === 'nodes'"
              v-model="chain.hops"
              :nodes="nodes"
            />
            <div v-else class="grid gap-4 sm:grid-cols-2">
              <label class="grid gap-1.5 text-sm font-medium">
                <span>固定中转节点</span>
                <input
                  v-model="chain.transit"
                  class="input-bordered input w-full"
                  required
                />
              </label>
              <label class="grid gap-1.5 text-sm font-medium">
                <span>家宽录入</span>
                <select
                  v-model="chain.input"
                  class="select-bordered select w-full"
                >
                  <option v-if="chain.saved" value="keep">
                    保留已保存的凭据
                  </option>
                  <option value="import">替换代理字符串</option>
                  <option value="fields">替换代理字段</option>
                  <option value="nodes">从节点列表选择</option>
                </select>
              </label>
              <label
                v-if="chain.input === 'import'"
                class="grid gap-1.5 text-sm font-medium sm:col-span-2"
              >
                <span>代理字符串</span>
                <input
                  v-model="chain.import"
                  class="input-bordered input w-full"
                  type="password"
                  required
                />
              </label>
              <template v-if="chain.input === 'fields'">
                <label class="grid gap-1.5 text-sm font-medium">
                  <span>协议</span>
                  <select
                    v-model="chain.type"
                    class="select-bordered select w-full"
                  >
                    <option>socks5</option>
                    <option>http</option>
                    <option>https</option>
                  </select>
                </label>
                <label class="grid gap-1.5 text-sm font-medium">
                  <span>主机</span>
                  <input
                    v-model="chain.host"
                    class="input-bordered input w-full"
                    required
                  />
                </label>
                <label class="grid gap-1.5 text-sm font-medium">
                  <span>端口</span>
                  <input
                    v-model.number="chain.port"
                    class="input-bordered input w-full"
                    type="number"
                    min="1"
                    max="65535"
                    required
                  />
                </label>
                <label class="grid gap-1.5 text-sm font-medium">
                  <span>用户名</span>
                  <input
                    v-model="chain.username"
                    class="input-bordered input w-full"
                  />
                </label>
                <label class="grid gap-1.5 text-sm font-medium">
                  <span>密码</span>
                  <input
                    v-model="chain.password"
                    class="input-bordered input w-full"
                    type="password"
                  />
                </label>
              </template>
            </div>
          </section>

          <section
            class="grid gap-4 rounded-2xl border border-base-content/10 bg-base-200/35 p-5 sm:p-6"
          >
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h2 class="text-base font-semibold">域名和来源规则</h2>
              <button
                type="button"
                class="btn gap-1.5 border-base-content/15 btn-outline btn-sm"
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
                <IconPlus class="h-4 w-4" aria-hidden="true" />
                添加规则
              </button>
            </div>
            <p
              v-if="!rules.length"
              class="rounded-xl border border-dashed border-base-content/12 py-6 text-center text-sm text-base-content/60"
            >
              暂未配置分流规则。
            </p>
            <fieldset
              v-for="(rule, index) in rules"
              :key="index"
              class="grid gap-4 rounded-xl border border-base-content/10 bg-base-100 p-4 sm:grid-cols-2 sm:p-5"
            >
              <label class="grid gap-1.5 text-sm font-medium">
                <span>目标链路</span>
                <select
                  v-model="rule.chain"
                  class="select-bordered select w-full"
                  required
                >
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
              <div class="flex flex-wrap items-end gap-3">
                <label
                  class="inline-flex cursor-pointer items-center gap-2 rounded-lg border border-base-content/10 bg-base-200/50 px-3.5 py-2.5 text-sm transition-colors hover:border-primary/40"
                >
                  <input
                    v-model="rule.presets"
                    type="checkbox"
                    class="checkbox checkbox-sm checkbox-primary"
                    value="openai"
                  />
                  <span>OpenAI</span>
                </label>
                <label
                  class="inline-flex cursor-pointer items-center gap-2 rounded-lg border border-base-content/10 bg-base-200/50 px-3.5 py-2.5 text-sm transition-colors hover:border-primary/40"
                >
                  <input
                    v-model="rule.presets"
                    type="checkbox"
                    class="checkbox checkbox-sm checkbox-primary"
                    value="antigravity"
                  />
                  <span>Antigravity</span>
                </label>
              </div>
              <label class="grid gap-1.5 text-sm font-medium">
                <span>域名后缀</span>
                <input
                  v-model="rule.domains"
                  class="input-bordered input w-full"
                  placeholder="example.com example.org"
                />
              </label>
              <label class="grid gap-1.5 text-sm font-medium">
                <span>来源网段</span>
                <input
                  v-model="rule.sources"
                  class="input-bordered input w-full"
                  placeholder="192.0.2.0/24"
                />
              </label>
              <div class="flex justify-end sm:col-span-2">
                <button
                  type="button"
                  class="btn gap-1 btn-ghost text-base-content/70 btn-sm hover:text-error"
                  @click="rules.splice(index, 1)"
                >
                  <IconTrash class="h-3.5 w-3.5" aria-hidden="true" />
                  删除规则
                </button>
              </div>
            </fieldset>
          </section>

          <section
            class="grid gap-4 rounded-2xl border border-base-content/10 bg-base-200/50 p-5"
          >
            <div class="flex flex-wrap items-center gap-3">
              <button
                class="btn gap-1.5 btn-primary"
                :disabled="
                  chains.some(
                    (chain) => chain.input === 'nodes' && chain.hops.length < 2,
                  )
                "
              >
                <IconEye class="h-4 w-4" aria-hidden="true" />
                脱敏预览
              </button>
              <button
                v-if="draft && state.writable"
                type="button"
                class="btn gap-1.5 btn-warning"
                @click="apply('publish', draft)"
              >
                <IconRocket class="h-4 w-4" aria-hidden="true" />
                发布预览
              </button>
            </div>
            <ul
              v-if="preview.length"
              class="grid gap-2.5 border-t border-base-content/10 pt-4"
            >
              <li
                v-for="chain in preview"
                :key="chain.id"
                class="flex flex-wrap items-center gap-2 rounded-xl border border-base-content/10 bg-base-100 px-4 py-3 text-sm"
              >
                <strong class="font-mono text-primary">{{ chain.id }}</strong>
                <span v-if="chain.hops" class="text-base-content/85">
                  ·
                  {{
                    chain.hops
                      .map((hop) => nodeLabels.get(hop) ?? hop)
                      .join(' → ')
                  }}
                </span>
                <span v-else class="text-base-content/85">
                  · {{ chain.transit }} → {{ chain.type }} ·
                  {{ chain.authenticated ? '需认证' : '无需认证' }}
                </span>
              </li>
            </ul>
          </section>
        </fieldset>
      </form>

      <section v-if="tab === 'history'" class="grid gap-6">
        <div
          class="grid gap-4 rounded-2xl border border-base-content/10 bg-base-200/40 p-5 sm:p-6"
        >
          <h2 class="text-base font-semibold">配置回滚</h2>
          <p v-if="!state.history?.length" class="text-sm text-base-content/60">
            暂无可回滚版本。
          </p>
          <div v-else class="flex flex-wrap items-end gap-3">
            <label
              class="grid max-w-md min-w-64 flex-1 gap-1.5 text-sm font-medium"
            >
              <span>回滚版本</span>
              <select
                v-model="rollbackTarget"
                class="select-bordered select w-full"
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
              class="btn gap-1.5 btn-outline"
              :disabled="!state.writable || pending || !rollbackTarget"
              @click="apply('rollback', rollbackTarget)"
            >
              <IconRotateClockwise2 class="h-4 w-4" aria-hidden="true" />
              回滚并验证
            </button>
          </div>
        </div>

        <details
          v-if="state.audit?.length"
          open
          class="rounded-2xl border border-base-content/10 bg-base-200/40 p-5 sm:p-6"
        >
          <summary class="cursor-pointer text-base font-semibold select-none">
            最近操作记录
          </summary>
          <ul
            class="mt-4 divide-y divide-base-content/8 overflow-hidden rounded-xl border border-base-content/10 bg-base-100 text-sm"
          >
            <li
              v-for="event in [...state.audit].reverse()"
              :key="event.Time"
              class="flex flex-wrap items-center gap-3 px-4 py-2.5"
            >
              <span class="font-mono text-xs text-base-content/55 tabular-nums">
                {{ new Date(event.Time).toLocaleString() }}
              </span>
              <span class="badge badge-ghost font-mono badge-sm">
                {{ event.Action }}
              </span>
              <span class="text-base-content/85">{{ event.Result }}</span>
            </li>
          </ul>
        </details>
      </section>
    </template>
  </div>
</template>
