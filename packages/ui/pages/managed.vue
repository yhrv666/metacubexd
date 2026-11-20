<script setup lang="ts">
interface ChainForm {
  id: string
  transit: string
  input: 'keep' | 'import' | 'fields'
  import: string
  type: string
  host: string
  port: number
  username: string
  password: string
  saved?: boolean
}

interface RuleForm {
  chain: string
  domains: string
  sources: string
  presets: string[]
}

interface Preview {
  id: string
  transit: string
  type: string
  authenticated: boolean
}

const chains = ref<ChainForm[]>([])
const rules = ref<RuleForm[]>([])
const preview = ref<Preview[]>([])
const notice = ref('')
const pending = ref(false)
interface ManagedState {
  version?: string
  writable: boolean
  canPreview: boolean
  diagnostic?: string
  chains?: Preview[]
  rules?: {
    chain: string
    domains?: string[]
    sources?: string[]
    presets?: string[]
  }[]
  history?: { id: string; created: string }[]
  subscriptions?: { id: string; refreshed?: string }[]
  audit?: { Time: string; Action: string; Result: string }[]
}
const state = ref<ManagedState>()
const draft = ref('')
const rollbackTarget = ref('')
const subscriptionID = ref('')
const subscriptionURL = ref('')

async function updateSubscription(
  action: 'save' | 'refresh' | 'remove',
  id: string,
) {
  if (!state.value?.writable || !id || pending.value) return
  pending.value = true
  draft.value = ''
  try {
    const response = await fetch(`/api/managed/subscription-${  action}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        version: state.value.version,
        id,
        ...(action === 'save' && { url: subscriptionURL.value }),
      }),
    })
    notice.value = response.ok
      ? '订阅操作完成。'
      : (await response.json()).error
    await loadState()
  } catch {
    notice.value = '订阅请求中断。请重新读取状态确认结果。'
  } finally {
    subscriptionURL.value = ''
    pending.value = false
  }
}

async function loadState() {
  try {
    const response = await fetch('/api/managed/state')
    const result = await response.json()
    if (!response.ok) {
      notice.value = result.error
      return
    }
    state.value = result
    chains.value = (state.value?.chains ?? []).map((chain) => ({
      ...chain,
      input: 'keep',
      import: '',
      host: '',
      port: 1080,
      username: '',
      password: '',
      saved: true,
    }))
    rules.value = (state.value?.rules ?? []).map((rule) => ({
      chain: rule.chain,
      domains: (rule.domains ?? []).join(' '),
      sources: (rule.sources ?? []).join(' '),
      presets: rule.presets ?? [],
    }))
    rollbackTarget.value = ''
    draft.value = ''
    preview.value = []
  } catch {
    notice.value = '无法读取托管状态'
  }
}

onMounted(loadState)
const split = (value: string) => value.split(/[\s,]+/).filter(Boolean)

function addChain() {
  chains.value.push({
    id: '',
    transit: '',
    input: 'import',
    import: '',
    type: 'socks5',
    host: '',
    port: 1080,
    username: '',
    password: '',
  })
}

function addRule() {
  rules.value.push({ chain: '', domains: '', sources: '', presets: [] })
}

function managedPayload() {
  return {
    chains: chains.value.map((chain) => ({
      id: chain.id,
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
    })),
    rules: rules.value.map((rule) => ({
      ...rule,
      domains: split(rule.domains),
      sources: split(rule.sources),
    })),
  }
}

function clearCredentials() {
  for (const chain of chains.value) {
    chain.import = ''
    chain.host = ''
    chain.username = ''
    chain.password = ''
  }
}

async function submitPreview() {
  pending.value = true
  notice.value = ''
  preview.value = []
  draft.value = ''
  try {
    const response = await fetch('/api/managed/preview', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ...managedPayload(),
        version: state.value?.version,
      }),
    })
    const result = await response.json()
    if (!response.ok) notice.value = result.error
    else {
      preview.value = result.chains
      draft.value = result.draft ?? ''
      notice.value = draft.value ? '请在五分钟内发布当前预览。' : '预览通过。'
    }
  } catch {
    notice.value = '无法连接服务器面板'
  } finally {
    clearCredentials()
    pending.value = false
  }
}

async function apply(action: 'publish' | 'rollback', id: string) {
  if (!state.value?.writable || !id || pending.value) return
  pending.value = true
  notice.value = ''
  try {
    const response = await fetch(`/api/managed/${action}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ version: state.value.version, id }),
    })
    notice.value = response.ok
      ? action === 'publish'
        ? '发布完成，运行态已验证。'
        : '回滚完成，运行态已验证。'
      : (await response.json()).error
    await loadState()
  } catch {
    notice.value = '发布请求中断。请重新读取状态确认结果。'
  } finally {
    pending.value = false
  }
}
</script>

<template>
  <form
    class="mx-auto grid max-w-4xl gap-6"
    autocomplete="off"
    @submit.prevent="submitPreview"
    @input="draft = ''"
  >
    <h1 class="text-2xl font-semibold">托管配置</h1>
    <p v-if="state?.diagnostic" class="alert alert-warning" role="alert">
      {{ state.diagnostic }}
    </p>
    <p v-else-if="state && !state.canPreview" class="alert">
      当前实例以只读模式运行。
    </p>
    <button
      v-if="!state"
      type="button"
      class="btn justify-self-start"
      @click="loadState"
    >
      重新读取托管状态
    </button>
    <fieldset v-if="state?.canPreview" :disabled="pending" class="grid gap-6">
      <section class="grid gap-3">
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">两跳链路</h2>
          <button type="button" class="btn btn-sm" @click="addChain">
            添加链路
          </button>
        </div>
        <fieldset
          v-for="(chain, index) in chains"
          :key="index"
          class="grid gap-3 rounded-box border border-base-300 bg-base-200 p-4 sm:grid-cols-2"
        >
          <label class="grid gap-1"
            >链路 ID<input v-model="chain.id" class="input w-full" required
          /></label>
          <label class="grid gap-1"
            >固定中转节点<input
              v-model="chain.transit"
              class="input w-full"
              required
          /></label>
          <label class="grid gap-1"
            >家宽录入<select v-model="chain.input" class="select w-full">
              <option value="import">代理字符串</option>
              <option value="fields">分字段</option>
              <option v-if="chain.saved" value="keep">保留已保存的凭据</option>
            </select></label
          >
          <label v-if="chain.input === 'import'" class="grid gap-1"
            >代理字符串<input
              v-model="chain.import"
              class="input w-full"
              type="password"
              required
          /></label>
          <template v-if="chain.input === 'fields'">
            <label class="grid gap-1"
              >协议<select v-model="chain.type" class="select w-full">
                <option>socks5</option>
                <option>http</option>
                <option>https</option>
              </select></label
            >
            <label class="grid gap-1"
              >主机<input v-model="chain.host" class="input w-full" required
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
          <button
            type="button"
            class="btn justify-self-start btn-ghost btn-sm sm:col-span-2"
            @click="chains.splice(index, 1)"
          >
            删除链路
          </button>
        </fieldset>
      </section>
      <section class="grid gap-3">
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">域名和来源规则</h2>
          <button type="button" class="btn btn-sm" @click="addRule">
            添加规则
          </button>
        </div>
        <fieldset
          v-for="(rule, index) in rules"
          :key="index"
          class="grid gap-3 rounded-box border border-base-300 bg-base-200 p-4 sm:grid-cols-2"
        >
          <label class="grid gap-1"
            >目标链路<select
              v-model="rule.chain"
              class="select w-full"
              required
            >
              <option disabled value="">选择链路</option>
              <option v-for="chain in chains" :key="chain.id" :value="chain.id">
                {{ chain.id }}
              </option>
            </select></label
          >
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
            class="btn justify-self-start btn-ghost btn-sm sm:col-span-2"
            @click="rules.splice(index, 1)"
          >
            删除规则
          </button>
        </fieldset>
      </section>
      <div class="flex gap-2">
        <button class="btn btn-primary" :disabled="pending">脱敏预览</button>
        <button
          v-if="draft && state?.writable"
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
          {{ chain.id }} · {{ chain.transit }} · {{ chain.type }} ·
          {{ chain.authenticated ? '需认证' : '无需认证' }}
        </li>
      </ul>
    </fieldset>
    <section v-if="state?.history?.length" class="grid gap-2">
      <h2 class="text-lg font-semibold">配置历史</h2>
      <label class="grid gap-1"
        >回滚版本<select v-model="rollbackTarget" class="select w-full">
          <option disabled value="">选择历史版本</option>
          <option v-for="item in state.history" :key="item.id" :value="item.id">
            {{ new Date(item.created).toLocaleString() }} ·
            {{ item.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <button
        type="button"
        class="btn justify-self-start"
        :disabled="!state.writable || pending || !rollbackTarget"
        @click="apply('rollback', rollbackTarget)"
      >
        回滚并验证
      </button>
    </section>
    <section v-if="state?.version" class="grid gap-3">
      <h2 class="text-lg font-semibold">手动刷新订阅</h2>
      <ul class="grid gap-2">
        <li
          v-for="item in state.subscriptions"
          :key="item.id"
          class="flex flex-wrap items-center gap-2 rounded-box border border-base-300 p-2"
        >
          <strong>{{ item.id }}</strong>
          <span class="mr-auto text-sm">{{
            item.refreshed
              ? new Date(item.refreshed).toLocaleString()
              : '等待首次刷新'
          }}</span>
          <button
            type="button"
            class="btn btn-sm"
            :disabled="pending || !state.writable"
            @click="updateSubscription('refresh', item.id)"
          >
            刷新
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="pending || !state.writable"
            @click="updateSubscription('remove', item.id)"
          >
            删除
          </button>
        </li>
      </ul>
      <div class="grid gap-2 sm:grid-cols-2">
        <label class="grid gap-1"
          >订阅 ID<input v-model="subscriptionID" class="input w-full"
        /></label>
        <label class="grid gap-1"
          >HTTPS 订阅地址<input
            v-model="subscriptionURL"
            type="password"
            class="input w-full"
            autocomplete="off"
        /></label>
      </div>
      <button
        type="button"
        class="btn justify-self-start"
        :disabled="
          pending || !state.writable || !subscriptionID || !subscriptionURL
        "
        @click="updateSubscription('save', subscriptionID)"
      >
        新增或替换凭据
      </button>
    </section>
    <details
      v-if="state?.audit?.length"
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
    <p v-if="notice" role="status">{{ notice }}</p>
  </form>
</template>
