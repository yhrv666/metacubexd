<script setup lang="ts">
import {
  IconArrowDown,
  IconArrowUp,
  IconCheck,
  IconGripVertical,
  IconPlus,
  IconSearch,
  IconX,
} from '@tabler/icons-vue'

interface NodeChoice {
  name: string
  label: string
  type: string
  source: string
}

const { nodes } = defineProps<{ nodes: NodeChoice[] }>()
const hops = defineModel<string[]>({ required: true })
const query = ref('')
const source = ref('')
const draggingIndex = ref<number | null>(null)
const dragOverIndex = ref<number | null>(null)

const sources = computed(() => [...new Set(nodes.map((node) => node.source))])
const byName = computed(() => new Map(nodes.map((node) => [node.name, node])))
const matching = computed(() => {
  const term = query.value.trim().toLowerCase()
  return nodes.filter(
    (node) =>
      (!source.value || node.source === source.value) &&
      (!term || node.label.toLowerCase().includes(term)),
  )
})

function add(name: string) {
  if (!hops.value.includes(name) && hops.value.length < 8)
    hops.value = [...hops.value, name]
}

function move(index: number, offset: number) {
  const target = index + offset
  if (target < 0 || target >= hops.value.length) return
  reorder(index, target)
}

function reorder(from: number, to: number) {
  if (from === to || from < 0 || to < 0 || to >= hops.value.length) return
  const next = [...hops.value]
  const [hop] = next.splice(from, 1)
  if (!hop) return
  next.splice(to, 0, hop)
  hops.value = next
}

function onDrop(targetIndex: number) {
  if (draggingIndex.value !== null) {
    reorder(draggingIndex.value, targetIndex)
  }
  draggingIndex.value = null
  dragOverIndex.value = null
}

function onDragLeave(index: number) {
  if (dragOverIndex.value === index) dragOverIndex.value = null
}

function onDragEnd() {
  draggingIndex.value = null
  dragOverIndex.value = null
}
</script>

<template>
  <div
    class="grid overflow-hidden rounded-xl border border-base-content/10 bg-base-100 md:grid-cols-2"
  >
    <section class="flex min-w-0 flex-col gap-3 p-4 sm:p-5">
      <div class="flex items-center justify-between gap-2">
        <h3 class="text-sm font-semibold">可选节点</h3>
        <span class="text-xs text-base-content/55 tabular-nums">
          {{ matching.length }}
        </span>
      </div>
      <div class="flex flex-wrap gap-2">
        <div class="relative min-w-32 flex-1">
          <IconSearch
            class="pointer-events-none absolute top-1/2 left-2.5 z-10 h-3.5 w-3.5 -translate-y-1/2 text-base-content/45"
            aria-hidden="true"
          />
          <input
            v-model="query"
            aria-label="搜索节点"
            class="input-bordered input w-full pl-8 input-sm"
            placeholder="搜索节点"
            type="search"
          />
        </div>
        <select
          v-model="source"
          aria-label="节点来源"
          class="select-bordered select max-w-44 select-sm"
        >
          <option value="">全部来源</option>
          <option v-for="item in sources" :key="item">{{ item }}</option>
        </select>
      </div>
      <ul class="max-h-80 space-y-1.5 overflow-auto pr-1">
        <li v-for="node in matching.slice(0, 100)" :key="node.name">
          <button
            type="button"
            class="flex min-h-10 w-full cursor-pointer items-center gap-2.5 rounded-lg border border-transparent px-3 py-2 text-left text-sm transition-colors hover:border-base-content/10 hover:bg-base-200/75 disabled:cursor-not-allowed disabled:opacity-45"
            :class="{
              'border-primary/25 bg-primary/8': hops.includes(node.name),
            }"
            :disabled="hops.includes(node.name) || hops.length >= 8"
            @click="add(node.name)"
          >
            <span
              class="min-w-0 flex-1 truncate font-medium"
              :title="node.label"
            >
              {{ node.label }}
            </span>
            <span
              class="shrink-0 rounded-md bg-base-200 px-1.5 py-0.5 text-xs text-base-content/65"
            >
              {{ node.source }}
            </span>
            <span
              class="flex h-5 w-5 shrink-0 items-center justify-center rounded-md text-base-content/60"
              aria-hidden="true"
            >
              <IconCheck
                v-if="hops.includes(node.name)"
                class="h-3.5 w-3.5 text-primary"
              />
              <IconPlus v-else class="h-3.5 w-3.5" />
            </span>
          </button>
        </li>
      </ul>
      <p
        v-if="!matching.length"
        class="rounded-lg border border-dashed border-base-content/12 py-6 text-center text-sm text-base-content/60"
      >
        没有匹配的节点。
      </p>
      <p v-else-if="matching.length > 100" class="text-xs text-base-content/55">
        显示前 100 个节点，可搜索或按来源筛选。
      </p>
    </section>

    <section
      class="flex min-w-0 flex-col gap-3 border-t border-base-content/10 bg-base-200/45 p-4 sm:p-5 md:border-t-0 md:border-l"
    >
      <div class="flex items-center justify-between gap-2">
        <h3 class="text-sm font-semibold">连接顺序</h3>
        <span class="text-xs text-base-content/55 tabular-nums">
          {{ hops.length }} / 8
        </span>
      </div>
      <p
        v-if="!hops.length"
        class="rounded-xl border border-dashed border-base-content/15 bg-base-100/60 px-4 py-8 text-center text-sm text-base-content/60"
      >
        按连接顺序点击左侧节点。第一个是入口，最后一个是出口。
      </p>
      <ol v-else aria-label="连接顺序" class="space-y-2.5">
        <li
          v-for="(name, index) in hops"
          :key="name"
          draggable="true"
          class="flex items-center gap-2.5 rounded-xl border border-base-content/10 bg-base-100 px-3 py-2.5 shadow-2xs transition-colors"
          :class="{
            'border-primary/60 ring-1 ring-primary/25':
              dragOverIndex === index && draggingIndex !== index,
            'opacity-50': draggingIndex === index,
          }"
          @dragstart="draggingIndex = index"
          @dragover.prevent="dragOverIndex = index"
          @dragleave="onDragLeave(index)"
          @drop.prevent="onDrop(index)"
          @dragend="onDragEnd"
        >
          <IconGripVertical
            class="h-4 w-4 shrink-0 cursor-grab text-base-content/35"
            aria-hidden="true"
          />
          <span
            class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/15 text-xs font-semibold text-primary tabular-nums"
          >
            {{ index + 1 }}
          </span>
          <span class="min-w-0 flex-1">
            <span
              class="block truncate text-sm font-medium"
              :title="byName.get(name)?.label ?? name"
            >
              {{ byName.get(name)?.label ?? name }}
            </span>
            <span
              class="mt-1 badge badge-xs"
              :class="
                !byName.has(name)
                  ? 'badge-soft badge-error'
                  : index === 0
                    ? 'badge-soft badge-primary'
                    : index === hops.length - 1
                      ? 'badge-soft badge-secondary'
                      : 'badge-ghost'
              "
            >
              {{
                !byName.has(name)
                  ? '节点已不可用'
                  : index === 0
                    ? '入口'
                    : index === hops.length - 1
                      ? '出口'
                      : '中转'
              }}
            </span>
          </span>
          <div class="flex shrink-0 items-center gap-0.5">
            <button
              type="button"
              class="btn btn-square btn-ghost btn-xs"
              :aria-label="`上移第 ${index + 1} 跳`"
              :disabled="index === 0"
              @click="move(index, -1)"
            >
              <IconArrowUp class="h-3.5 w-3.5" aria-hidden="true" />
            </button>
            <button
              type="button"
              class="btn btn-square btn-ghost btn-xs"
              :aria-label="`下移第 ${index + 1} 跳`"
              :disabled="index === hops.length - 1"
              @click="move(index, 1)"
            >
              <IconArrowDown class="h-3.5 w-3.5" aria-hidden="true" />
            </button>
            <button
              type="button"
              class="btn btn-square btn-ghost text-base-content/60 btn-xs hover:text-error"
              :aria-label="`移除第 ${index + 1} 跳`"
              @click="hops = hops.filter((_, i) => i !== index)"
            >
              <IconX class="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          </div>
        </li>
      </ol>
      <p
        v-if="hops.length === 1"
        class="rounded-lg bg-warning/10 px-3 py-2 text-xs text-warning"
      >
        再选择一个出口节点。
      </p>
    </section>
  </div>
</template>
