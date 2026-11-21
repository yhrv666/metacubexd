<script setup lang="ts">
import {
  IconAlertTriangle,
  IconFileStack,
  IconHome,
  IconLock,
  IconLogout,
  IconNetwork,
  IconRefresh,
  IconRuler,
  IconServerCog,
} from '@tabler/icons-vue'

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
  { path: '/overview', label: 'overview', icon: IconHome },
  { path: '/connections', label: 'connections', icon: IconNetwork },
  { path: '/rules', label: 'rules', icon: IconRuler },
  { path: '/logs', label: 'logs', icon: IconFileStack },
] as const

watch(
  () => config.curTheme,
  (theme) => {
    if (import.meta.client) {
      document.documentElement.setAttribute('data-theme', theme)
    }
  },
  { immediate: true },
)

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
    releaseTag.value = ''
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
    class="flex h-dvh flex-col bg-base-100 text-base-content antialiased"
    :class="config.enableTwemoji ? 'font-twemoji' : 'font-default'"
    :data-theme="config.curTheme"
  >
    <div v-if="!ready" class="loading m-auto loading-spinner" role="status" />
    <div
      v-else-if="!signedIn"
      class="flex flex-1 items-center justify-center bg-[radial-gradient(ellipse_at_top,color-mix(in_oklab,var(--color-primary)_14%,transparent),transparent_62%)] p-6"
    >
      <form
        class="card w-full max-w-md gap-6 rounded-2xl border border-base-content/10 bg-base-200/80 p-8 shadow-xl backdrop-blur-sm"
        @submit.prevent="signIn"
      >
        <div class="flex items-center gap-3.5">
          <div
            class="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary/15 text-primary"
            aria-hidden="true"
          >
            <IconServerCog class="h-6 w-6" />
          </div>
          <div class="min-w-0">
            <h1 class="text-xl font-bold tracking-tight">
              MetaCubeXD 服务器版
            </h1>
          </div>
        </div>
        <label class="grid gap-2 text-sm font-medium" for="admin-password">
          <span>管理员密码</span>
          <div class="relative">
            <IconLock
              class="pointer-events-none absolute top-1/2 left-3.5 z-10 h-4 w-4 -translate-y-1/2 text-base-content/45"
              aria-hidden="true"
            />
            <input
              id="admin-password"
              v-model="password"
              type="password"
              autocomplete="current-password"
              required
              placeholder="输入面板管理员密码"
              class="input-bordered input w-full pl-10"
            />
          </div>
        </label>
        <div
          v-if="signInError"
          role="alert"
          class="alert rounded-xl alert-soft py-2.5 text-sm alert-error"
        >
          <IconAlertTriangle class="h-4 w-4 shrink-0" aria-hidden="true" />
          <span>{{ signInError }}</span>
        </div>
        <button class="btn w-full btn-primary" :disabled="submitting">
          登录
        </button>
      </form>
    </div>
    <template v-else>
      <header
        class="sticky top-0 z-30 flex flex-wrap items-center gap-3 border-b border-base-content/10 bg-base-200/85 px-4 py-2.5 backdrop-blur-md sm:px-6"
      >
        <NuxtLink
          to="/overview"
          class="flex items-center gap-2.5 rounded-lg pr-1 no-underline"
        >
          <span
            class="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/15 text-primary"
            aria-hidden="true"
          >
            <IconServerCog class="h-4.5 w-4.5" />
          </span>
          <strong
            class="bg-linear-to-r from-primary to-secondary bg-clip-text text-base font-bold tracking-tight text-transparent"
          >
            MetaCubeXD
          </strong>
        </NuxtLink>

        <div
          class="flex flex-wrap items-center gap-1 rounded-xl border border-base-content/8 bg-base-100/75 p-1"
        >
          <nav class="flex flex-wrap items-center gap-1" aria-label="监控">
            <NuxtLink
              v-for="link in links"
              :key="link.path"
              :to="link.path"
              class="btn gap-1.5 rounded-lg btn-ghost px-3 font-medium text-base-content/75 btn-sm hover:text-base-content"
              active-class="bg-primary/15! text-primary! hover:bg-primary/20!"
            >
              <component
                :is="link.icon"
                class="h-4 w-4 shrink-0"
                aria-hidden="true"
              />
              <span>{{ t(link.label) }}</span>
            </NuxtLink>
          </nav>
          <span
            class="mx-0.5 hidden h-4 w-px bg-base-content/10 sm:inline-block"
            aria-hidden="true"
          />
          <NuxtLink
            to="/managed"
            class="btn gap-1.5 rounded-lg btn-ghost px-3 font-medium text-base-content/75 btn-sm hover:text-base-content"
            active-class="bg-primary/15! text-primary! hover:bg-primary/20!"
          >
            <IconServerCog class="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>托管配置</span>
          </NuxtLink>
        </div>

        <div class="ml-auto flex flex-wrap items-center justify-end gap-2.5">
          <span
            v-if="version"
            class="inline-flex items-center gap-1.5 rounded-lg border border-base-content/10 bg-base-100/80 px-2.5 py-1 text-xs font-medium tabular-nums"
          >
            <span class="h-2 w-2 rounded-full bg-success" aria-hidden="true" />
            <span>Mihomo {{ version }}</span>
          </span>
          <span
            v-if="releaseTag"
            class="badge badge-outline border-primary/40 bg-primary/5 px-2.5 py-2.5 font-mono text-xs text-primary"
          >
            {{ releaseTag }}
          </span>
          <ThemeSwitcher />
          <button
            class="btn gap-1.5 btn-ghost text-base-content/75 btn-sm hover:text-error"
            @click="signOut"
          >
            <IconLogout class="h-4 w-4" aria-hidden="true" />
            <span>退出</span>
          </button>
        </div>
      </header>
      <div
        v-if="controllerError"
        role="status"
        class="alert flex items-center justify-between rounded-none border-b border-warning/20 alert-soft px-4 py-2.5 text-sm alert-warning sm:px-6"
      >
        <span class="flex items-center gap-2">
          <IconAlertTriangle class="h-4 w-4 shrink-0" aria-hidden="true" />
          {{ controllerError }}
        </span>
        <button class="btn gap-1 btn-warning btn-xs" @click="checkController">
          <IconRefresh class="h-3.5 w-3.5" aria-hidden="true" />
          重试
        </button>
      </div>
      <main class="min-h-0 flex-1 overflow-auto p-4 sm:p-6">
        <slot />
      </main>
      <ProtectedResources />
    </template>
  </div>
</template>
