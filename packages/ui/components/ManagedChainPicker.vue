<script setup lang="ts">
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
  const next = [...hops.value]
  const target = index + offset
  if (target < 0 || target >= next.length) return
  const [hop] = next.splice(index, 1)
  if (!hop) return
  next.splice(target, 0, hop)
  hops.value = next
}
</script>

<template>
  <div
    class="grid overflow-hidden rounded-box border border-base-300 md:grid-cols-2"
  >
    <section class="min-w-0 p-3">
      <h3 class="mb-2 font-medium">可选节点</h3>
      <div class="mb-2 flex flex-wrap gap-2">
        <input
          v-model="query"
          aria-label="搜索节点"
          class="input min-w-28 flex-1 input-sm"
          placeholder="搜索节点"
          type="search"
        />
        <select
          v-model="source"
          aria-label="节点来源"
          class="select max-w-40 select-sm"
        >
          <option value="">全部来源</option>
          <option v-for="item in sources" :key="item">{{ item }}</option>
        </select>
      </div>
      <ul class="max-h-60 space-y-1 overflow-auto">
        <li v-for="node in matching.slice(0, 100)" :key="node.name">
          <button
            type="button"
            class="btn h-auto min-h-8 w-full justify-start btn-ghost py-1 text-left btn-sm"
            :disabled="hops.includes(node.name) || hops.length >= 8"
            @click="add(node.name)"
          >
            <span class="min-w-0 flex-1 truncate" :title="node.label">{{
              node.label
            }}</span>
            <span class="shrink-0 text-xs font-normal opacity-70">{{
              node.source
            }}</span>
            <span aria-hidden="true">{{
              hops.includes(node.name) ? '✓' : '+'
            }}</span>
          </button>
        </li>
      </ul>
      <p v-if="!matching.length" class="text-sm opacity-70">没有匹配的节点。</p>
      <p v-else-if="matching.length > 100" class="mt-2 text-xs opacity-70">
        显示前 100 个节点，可搜索或按来源筛选。
      </p>
    </section>
    <section
      class="min-w-0 border-t border-base-300 bg-base-200/60 p-3 md:border-t-0 md:border-l"
    >
      <h3 class="mb-2 font-medium">连接顺序</h3>
      <p v-if="!hops.length" class="text-sm opacity-70">
        按连接顺序点击节点。第一个是入口，最后一个是出口。
      </p>
      <ol v-else aria-label="连接顺序" class="space-y-2">
        <li
          v-for="(name, index) in hops"
          :key="name"
          class="flex items-center gap-2 rounded-field bg-base-100 p-2"
        >
          <span class="w-5 shrink-0 text-center text-sm tabular-nums">{{
            index + 1
          }}</span>
          <span class="min-w-0 flex-1">
            <span
              class="block truncate"
              :title="byName.get(name)?.label ?? name"
            >
              {{ byName.get(name)?.label ?? name }}
            </span>
            <span class="text-xs opacity-70">
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
          <button
            type="button"
            class="btn btn-ghost btn-xs"
            :aria-label="`上移第 ${index + 1} 跳`"
            :disabled="index === 0"
            @click="move(index, -1)"
          >
            ↑
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-xs"
            :aria-label="`下移第 ${index + 1} 跳`"
            :disabled="index === hops.length - 1"
            @click="move(index, 1)"
          >
            ↓
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-xs"
            :aria-label="`移除第 ${index + 1} 跳`"
            @click="hops = hops.filter((_, i) => i !== index)"
          >
            ×
          </button>
        </li>
      </ol>
      <p v-if="hops.length === 1" class="mt-2 text-sm opacity-70">
        再选择一个出口节点。
      </p>
    </section>
  </div>
</template>
