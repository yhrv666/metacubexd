<script setup lang="ts">
const { t } = useI18n()
const config = useConfigStore()
const signedIn = ref(false)
const ready = ref(false)
const password = ref('')
const signInError = ref('')
const controllerError = ref('')
const version = ref('')
const releaseTag = ref('')
const submitting = ref(false)

const links = [
  { path: '/overview', label: 'overview' },
  { path: '/connections', label: 'connections' },
  { path: '/rules', label: 'rules' },
  { path: '/logs', label: 'logs' },
] as const

async function checkSession() {
  const response = await fetch('/api/session')
  signedIn.value = response.ok
  releaseTag.value = response.ok
    ? ((await response.json()).releaseTag ?? '')
    : ''
}

async function checkController() {
  if (!signedIn.value) return
  try {
    const response = await fetch('/api/monitor/version')
    if (response.status === 401) {
      signedIn.value = false
      return
    }
    const result = await response.json()
    controllerError.value = response.ok ? '' : result.error
    version.value = response.ok ? result.version : ''
  } catch {
    controllerError.value = '无法连接服务器面板'
  }
}

async function signIn() {
  submitting.value = true
  signInError.value = ''
  try {
    const response = await fetch('/api/session', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: password.value }),
    })
    signedIn.value = response.ok
    if (!response.ok) signInError.value = (await response.json()).error
    else {
      await checkSession()
      void checkController()
    }
  } catch {
    signInError.value = '无法连接服务器面板'
  } finally {
    password.value = ''
    submitting.value = false
  }
}

async function signOut() {
  const response = await fetch('/api/session', { method: 'DELETE' })
  if (response.ok || response.status === 401) {
    signedIn.value = false
    useLogsStore().clearLogs()
    useGlobalStore().clearChartHistory()
    useGlobalStore().setLatestTraffic(null)
    useGlobalStore().setLatestMemory(null)
    useConnectionsStore().clearMonitor()
  }
}

let timer: ReturnType<typeof setInterval> | undefined
onMounted(async () => {
  try {
    await checkSession()
    await checkController()
  } catch {
    signInError.value = '无法连接服务器面板'
  } finally {
    ready.value = true
  }
  timer = setInterval(checkController, 5000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div
    class="flex h-dvh flex-col bg-base-100 text-base-content"
    :data-theme="config.curTheme"
  >
    <div v-if="!ready" class="loading m-auto loading-spinner" role="status" />
    <form
      v-else-if="!signedIn"
      class="card m-auto w-full max-w-sm gap-4 bg-base-200 p-6"
      @submit.prevent="signIn"
    >
      <h1 class="text-xl font-semibold">MetaCubeXD 服务器版</h1>
      <label class="grid gap-2" for="admin-password">
        管理员密码
        <input
          id="admin-password"
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          class="input w-full"
        />
      </label>
      <p v-if="signInError" role="alert">{{ signInError }}</p>
      <button class="btn btn-primary" :disabled="submitting">登录</button>
    </form>
    <template v-else>
      <header
        class="flex flex-wrap items-center gap-2 border-b border-base-300 px-4 py-2"
      >
        <strong class="mr-2">MetaCubeXD</strong>
        <nav class="flex flex-wrap gap-1" aria-label="监控">
          <NuxtLink
            v-for="link in links"
            :key="link.path"
            :to="link.path"
            class="btn btn-ghost btn-sm"
            active-class="btn-active"
            >{{ t(link.label) }}</NuxtLink
          >
        </nav>
        <NuxtLink
          to="/managed"
          class="btn btn-ghost btn-sm"
          active-class="btn-active"
          >托管配置</NuxtLink
        >
        <div class="ml-auto flex flex-wrap items-center justify-end gap-2">
          <span v-if="version" class="text-sm">Mihomo {{ version }}</span>
          <span v-if="releaseTag" class="badge badge-outline text-xs">
            {{ releaseTag }}
          </span>
          <button class="btn btn-ghost btn-sm" @click="signOut">退出</button>
        </div>
      </header>
      <div
        v-if="controllerError"
        role="status"
        class="alert rounded-none alert-warning"
      >
        {{ controllerError }}
        <button class="btn btn-sm" @click="checkController">重试</button>
      </div>
      <main class="min-h-0 flex-1 overflow-auto p-4">
        <slot />
      </main>
      <ProtectedResources />
    </template>
  </div>
</template>
