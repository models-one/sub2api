<template>
  <div class="min-h-screen bg-gray-50 text-gray-900 dark:bg-dark-950 dark:text-white">
    <header class="border-b border-gray-200 bg-white/95 dark:border-dark-800 dark:bg-dark-900/95">
      <div class="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-4 sm:px-6">
        <RouterLink to="/home" class="flex min-w-0 items-center gap-3">
          <span class="flex h-10 w-10 flex-shrink-0 items-center justify-center overflow-hidden rounded-xl bg-white shadow-sm ring-1 ring-gray-200 dark:bg-dark-800 dark:ring-dark-700">
            <img :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full object-contain" />
          </span>
          <span class="truncate text-base font-semibold text-gray-950 dark:text-white">
            {{ siteName }}
          </span>
        </RouterLink>
        <div class="flex flex-shrink-0 items-center gap-2">
          <RouterLink
            v-for="entry in entries"
            :key="entry.slug"
            :to="`/docs/${entry.slug}`"
            class="hidden rounded-md px-3 py-1.5 text-sm font-medium transition sm:inline-flex"
            :class="entry.slug === currentSlug
              ? 'text-primary-700 dark:text-primary-300'
              : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'"
          >
            {{ convert(t(entry.titleKey)) }}
          </RouterLink>
          <LocaleSwitcher :reload-on-switch="false" />
          <RouterLink
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="inline-flex items-center justify-center rounded-lg bg-primary-600 px-4 py-2 text-sm font-semibold text-white shadow-sm shadow-primary-600/20 transition hover:bg-primary-700"
          >
            {{ t('home.dashboard') }}
          </RouterLink>
          <RouterLink
            v-else
            to="/login"
            class="inline-flex items-center justify-center rounded-lg bg-primary-600 px-4 py-2 text-sm font-semibold text-white shadow-sm shadow-primary-600/20 transition hover:bg-primary-700"
          >
            {{ t('home.login') }}
          </RouterLink>
        </div>
      </div>
    </header>

    <main class="mx-auto grid max-w-6xl gap-8 px-4 py-8 sm:px-6 lg:grid-cols-[240px_minmax(0,1fr)] lg:py-10">
      <aside class="hidden lg:block">
        <nav class="sticky top-6 max-h-[calc(100vh-3rem)] overflow-y-auto rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <p class="mb-2 px-1 text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-400">
            {{ convert(t('apiDocs.tocLabel')) }}
          </p>
          <ul v-if="tocItems.length" class="flex flex-col gap-0.5">
            <li v-for="item in tocItems" :key="item.id">
              <a
                :href="`#${item.id}`"
                class="block truncate rounded px-2 py-1.5 text-sm transition"
                :class="[
                  item.level === 1 ? 'font-semibold text-gray-900 dark:text-white' :
                  item.level === 2 ? 'pl-3 text-gray-700 dark:text-dark-200' :
                  'pl-6 text-gray-500 dark:text-dark-400',
                  activeId === item.id
                    ? 'bg-primary-50 text-primary-700 dark:bg-primary-500/10 dark:text-primary-200'
                    : 'hover:bg-gray-100 dark:hover:bg-dark-800'
                ]"
                @click="onTocClick(item.id, $event)"
              >
                {{ item.text }}
              </a>
            </li>
          </ul>
          <p v-else class="px-1 py-2 text-xs text-gray-400 dark:text-dark-500">
            {{ convert(t('apiDocs.tocEmpty')) }}
          </p>
        </nav>
      </aside>

      <article class="min-w-0">
        <div
          v-if="renderedHtml"
          ref="contentEl"
          class="api-doc-content"
          v-html="renderedHtml"
        ></div>
        <div
          v-else
          class="rounded-lg border border-dashed border-gray-300 bg-white px-6 py-14 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-dark-400"
        >
          {{ convert(t('apiDocs.empty')) }}
        </div>
      </article>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useHead } from '@unhead/vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import { useAuthStore, useAppStore, useMerchantStore } from '@/stores'
import quickstartZhMd from '@/assets/docs/quickstart.md?raw'
import apiGuideZhMd from '@/assets/docs/api-guide.md?raw'
import quickstartEnMd from '@/assets/docs/quickstart.en.md?raw'
import apiGuideEnMd from '@/assets/docs/api-guide.en.md?raw'

const route = useRoute()
const { t, locale } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()
const merchantStore = useMerchantStore()

interface DocEntry {
  slug: string
  titleKey: string
  // 按 locale 取源文件：en → 英文版；zh-TW 走 opencc 简→繁转换；zh 直接用简体
  sources: { en: string; zh: string }
}

const entries: DocEntry[] = [
  {
    slug: 'quickstart',
    titleKey: 'apiDocs.entries.quickstart.navLabel',
    sources: { en: quickstartEnMd, zh: quickstartZhMd },
  },
  {
    slug: 'api-guide',
    titleKey: 'apiDocs.entries.apiGuide.navLabel',
    sources: { en: apiGuideEnMd, zh: apiGuideZhMd },
  },
]

const currentSlug = computed(() => {
  const slug = String(route.params.slug || '').trim()
  return entries.some((e) => e.slug === slug) ? slug : entries[0].slug
})

const currentEntry = computed<DocEntry | null>(() => {
  return entries.find((e) => e.slug === currentSlug.value) ?? null
})

const rawContent = computed(() => {
  const entry = currentEntry.value
  if (!entry) return ''
  // en → 直接读英文版；zh / zh-TW → 简体源（zh-TW 后续走 opencc 转换）
  const src = locale.value === 'en' ? entry.sources.en : entry.sources.zh
  return src.trim()
})

// 简→繁 转换器：只在 zh-TW 时按需加载 opencc-js，加载完成前先用原文渲染，加载完后再覆盖。
const s2tConvert = ref<((text: string) => string) | null>(null)
watch(
  () => locale.value,
  async (loc) => {
    if (loc === 'zh-TW') {
      if (!s2tConvert.value) {
        try {
          const mod = await import('opencc-js')
          s2tConvert.value = mod.Converter({ from: 'cn', to: 'tw' })
        } catch {
          // 加载失败就保留原文，不影响阅读
          s2tConvert.value = null
        }
      }
    }
  },
  { immediate: true },
)

function convert(text: string): string {
  if (locale.value !== 'zh-TW' || !s2tConvert.value) return text
  return s2tConvert.value(text)
}

// 提取 H1/H2/H3 作为 TOC，跳过代码块内的 #
interface TocItem {
  id: string
  level: 1 | 2 | 3
  text: string
}

const tocItems = computed<TocItem[]>(() => {
  const md = rawContent.value
  if (!md) return []
  const items: TocItem[] = []
  const lines = md.split('\n')
  let inCodeBlock = false
  let idx = 0
  for (const line of lines) {
    if (line.startsWith('```')) {
      inCodeBlock = !inCodeBlock
      continue
    }
    if (inCodeBlock) continue
    const m = line.match(/^(#{1,3})\s+(.+?)\s*$/)
    if (!m) continue
    const level = m[1].length as 1 | 2 | 3
    const rawText = m[2].replace(/<[^>]+>/g, '').trim()
    if (!rawText) continue
    idx += 1
    items.push({
      id: `doc-h-${idx}`,
      level,
      text: convert(rawText),
    })
  }
  return items
})

// 渲染 HTML：先用 marked 渲染，再 post-process 给 h1/h2/h3 注入 id（与 TOC 同序），
// 最后按需简→繁、sanitize。
marked.setOptions({ breaks: true, gfm: true })

const renderedHtml = computed(() => {
  const md = rawContent.value
  if (!md) return ''
  let html = marked.parse(md) as string
  let counter = 0
  html = html.replace(/<(h[1-3])(\s[^>]*)?>/g, (_, tag, attrs = '') => {
    counter += 1
    return `<${tag} id="doc-h-${counter}"${attrs || ''}>`
  })
  if (locale.value === 'zh-TW' && s2tConvert.value) {
    html = s2tConvert.value(html)
  }
  return DOMPurify.sanitize(html, { ADD_ATTR: ['id'] })
})

const activeId = ref<string>('')
const contentEl = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | null = null

function setupObserver() {
  if (observer) {
    observer.disconnect()
    observer = null
  }
  if (typeof window === 'undefined' || !contentEl.value) return
  const headings = contentEl.value.querySelectorAll<HTMLElement>('h1[id], h2[id], h3[id]')
  if (!headings.length) {
    activeId.value = ''
    return
  }
  observer = new IntersectionObserver(
    (entries) => {
      // 选择"最靠近顶部"的可见标题作为高亮目标
      const visible = entries
        .filter((e) => e.isIntersecting)
        .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)
      if (visible.length) {
        activeId.value = visible[0].target.id
      }
    },
    { rootMargin: '-72px 0px -70% 0px', threshold: [0, 1] },
  )
  headings.forEach((h) => observer!.observe(h))
  activeId.value = headings[0].id
}

watch([renderedHtml], async () => {
  await nextTick()
  setupObserver()
}, { immediate: false })

function onTocClick(id: string, ev: MouseEvent) {
  ev.preventDefault()
  const el = document.getElementById(id)
  if (!el) return
  el.scrollIntoView({ behavior: 'smooth', block: 'start' })
  // 同步 URL hash（不触发滚动）
  history.replaceState(null, '', `${route.path}#${id}`)
  activeId.value = id
}

const siteName = computed(() =>
  (merchantStore.isMerchantSite && merchantStore.siteName) ||
  appStore.cachedPublicSettings?.site_name ||
  appStore.siteName ||
  'Sub2API'
)
const siteLogo = computed(() =>
  (merchantStore.isMerchantSite && merchantStore.siteLogo) ||
  appStore.cachedPublicSettings?.site_logo ||
  appStore.siteLogo ||
  ''
)
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => isAdmin.value ? '/admin/dashboard' : '/dashboard')

const docTitle = computed(() => {
  const entry = currentEntry.value
  return entry ? t(entry.titleKey) : t('apiDocs.pageTitle')
})

useHead(() => ({
  title: `${docTitle.value} | ${siteName.value}`,
  htmlAttrs: { lang: locale.value },
  meta: [
    { name: 'description', content: `${docTitle.value} — ${siteName.value}` },
    { property: 'og:type', content: 'article' },
    { property: 'og:title', content: `${docTitle.value} | ${siteName.value}` },
    { property: 'og:site_name', content: siteName.value },
  ],
}))

onMounted(async () => {
  authStore.checkAuth()
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
  await nextTick()
  setupObserver()
  // 进入时若 URL 带 hash，滚到对应位置
  const hash = window.location.hash.replace('#', '')
  if (hash) {
    const el = document.getElementById(hash)
    if (el) el.scrollIntoView({ block: 'start' })
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})
</script>

<style scoped>
.api-doc-content {
  line-height: 1.75;
  overflow-wrap: anywhere;
  color: inherit;
}

.api-doc-content :deep(h1) {
  @apply mb-4 mt-8 border-b border-gray-200 pb-3 text-3xl font-bold scroll-mt-24 dark:border-dark-700;
}
.api-doc-content :deep(h1:first-child) { @apply mt-0; }

.api-doc-content :deep(h2) {
  @apply mb-3 mt-8 text-2xl font-bold scroll-mt-24;
}

.api-doc-content :deep(h3) {
  @apply mb-2 mt-6 text-xl font-semibold scroll-mt-24;
}

.api-doc-content :deep(h4) {
  @apply mb-2 mt-5 text-lg font-semibold scroll-mt-24;
}

.api-doc-content :deep(p) {
  @apply mb-4 text-gray-700 dark:text-dark-200;
}

.api-doc-content :deep(a) {
  @apply text-primary-600 underline underline-offset-4 hover:text-primary-700 dark:text-primary-300 dark:hover:text-primary-200;
}

.api-doc-content :deep(ul) {
  @apply mb-4 list-disc pl-6;
}

.api-doc-content :deep(ol) {
  @apply mb-4 list-decimal pl-6;
}

.api-doc-content :deep(li) {
  @apply mb-1 text-gray-700 dark:text-dark-200;
}

.api-doc-content :deep(blockquote) {
  @apply my-5 border-l-4 border-gray-300 pl-4 text-gray-600 dark:border-dark-600 dark:text-dark-300;
}

.api-doc-content :deep(code) {
  @apply rounded bg-gray-100 px-1.5 py-0.5 font-mono text-sm dark:bg-dark-800;
}

.api-doc-content :deep(pre) {
  @apply my-5 overflow-x-auto rounded-lg bg-gray-950 p-4 text-gray-100;
}

.api-doc-content :deep(pre code) {
  @apply bg-transparent p-0 text-inherit;
}

.api-doc-content :deep(table) {
  @apply my-5 block w-full overflow-x-auto border-collapse;
}

.api-doc-content :deep(th) {
  @apply border border-gray-300 bg-gray-50 px-3 py-2 text-left font-semibold dark:border-dark-600 dark:bg-dark-800;
}

.api-doc-content :deep(td) {
  @apply border border-gray-300 px-3 py-2 dark:border-dark-600;
}

.api-doc-content :deep(img) {
  @apply my-5 h-auto max-w-full rounded-lg;
}

.api-doc-content :deep(hr) {
  @apply my-7 border-gray-200 dark:border-dark-700;
}
</style>
