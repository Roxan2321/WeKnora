import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { computed, reactive, ref } from 'vue'
import { manualDraftContent } from '../utils/manualDraftContent'

const hookSource = readFileSync(new URL('./useKnowledgeBase.ts', import.meta.url), 'utf8')
const component = readFileSync(new URL('../components/doc-content.vue', import.meta.url), 'utf8')
const compiled = ts.transpileModule(hookSource, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText
const settle = () => new Promise<void>(resolve => setImmediate(resolve))

function fixture() {
  const requests = new Map<string, { resolve: (value: unknown) => void; reject: (error: Error) => void }>()
  const chunks: string[] = []
  const exports: any = {}
  const modules: Record<string, unknown> = {
    vue: { reactive, ref },
    pinia: { storeToRefs: () => ({ cardList: ref([]), total: ref(0) }) },
    '../utils/index': { formatStringDate: () => 'test date' },
    '@/stores/knowledge': { knowledgeStore: () => ({}) },
    '@/stores/ui': { useUIStore: () => ({ selectedTagIds: [] }) },
    'tdesign-vue-next': { MessagePlugin: {} },
    'vue-router': { useRoute: () => ({ params: {} }) },
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    '@/utils/manualDraftContent': { manualDraftContent },
    '@/api/knowledge-base/index': {
      getKnowledgeDetails: (id: string) => new Promise((resolve, reject) => requests.set(id, { resolve, reject })),
      getKnowledgeDetailsCon: async (id: string) => { chunks.push(id); return { success: true, data: [], total: 0 } },
    },
  }
  runInNewContext(compiled, { exports, require: (name: string) => {
    assert.ok(name in modules, `unexpected dependency ${name}`)
    return modules[name]
  }, console })
  return { hook: exports.default(), requests, chunks }
}

test('authorized manual drafts supply the read-only body without requesting chunks', async () => {
  const f = fixture()
  f.hook.getCardDetails({ id: 'draft' })
  f.requests.get('draft')!.resolve({ success: true, data: {
    id: 'draft', type: 'manual', parse_status: 'draft', metadata: { status: 'draft', content: '聊天正文 <script>not executed</script>' },
  } })
  await settle()
  assert.equal(f.hook.details.manualContent, '聊天正文 <script>not executed</script>')
  assert.equal(f.hook.details.total, 0)
  assert.equal(f.hook.details.chunkLoading, false)
  assert.deepEqual(f.chunks, [])
  assert.match(component, /return props\.details\.manualContent/)
  assert.match(component, /processMarkdown\(mergedContent\)/, 'drafts must retain the existing sanitized renderer')
  assert.match(component, /v-if="typeof details\.manualContent !== 'string'"/)
})

test('regular and published documents retain chunk previews', async () => {
  const f = fixture()
  f.hook.getCardDetails({ id: 'published' })
  f.requests.get('published')!.resolve({ success: true, data: {
    id: 'published', type: 'manual', parse_status: 'completed', metadata: { status: 'publish', content: 'not a draft' },
  } })
  await settle()
  assert.equal(f.hook.details.manualContent, null)
  assert.deepEqual(f.chunks, ['published'])
})

test('navigation clears the previous draft and ignores late detail responses', async () => {
  const f = fixture()
  f.hook.getCardDetails({ id: 'first' })
  f.hook.getCardDetails({ id: 'second' })
  f.requests.get('second')!.resolve({ success: true, data: { id: 'second', type: 'manual', parse_status: 'draft', metadata: { content: 'second body' } } })
  await settle()
  f.requests.get('first')!.resolve({ success: true, data: { id: 'first', type: 'manual', parse_status: 'draft', metadata: { content: 'stale body' } } })
  await settle()
  assert.equal(f.hook.details.id, 'second')
  assert.equal(f.hook.details.manualContent, 'second body')
  f.hook.getCardDetails({ id: 'forbidden' })
  assert.equal(f.hook.details.manualContent, null)
  f.requests.get('forbidden')!.reject(new Error('Forbidden'))
  await settle()
  assert.equal(f.hook.details.manualContent, null)
  assert.equal(f.hook.details.chunkLoadError, 'Forbidden')
  assert.deepEqual(f.chunks, [], 'failed authorization must not request a chunk fallback')
})

test('metadata parser is limited to draft text and tolerates invalid legacy data', () => {
  assert.equal(manualDraftContent({ type: 'manual', parse_status: 'draft', metadata: '{"content":"正文"}' }), '正文')
  assert.equal(manualDraftContent({ type: 'manual', parse_status: 'draft', metadata: { content: '' } }), '')
  for (const metadata of ['broken JSON', null, [], { content: 7 }, { status: 'publish', content: 'hidden' }]) {
    assert.equal(manualDraftContent({ type: 'manual', parse_status: 'draft', metadata }), null)
  }
  assert.equal(manualDraftContent({ type: 'file', parse_status: 'draft', metadata: { content: 'not manual' } }), null)
})

test('an explicitly read-only shared KB never inherits workspace-admin edit controls', () => {
  const start = component.indexOf('const canDeleteGeneratedQuestion = computed(')
  const end = component.indexOf('const canEditContent =', start)
  assert.ok(start >= 0 && end > start)
  const props: { canEditKB?: boolean } = { canEditKB: false }
  const state: any = {}
  runInNewContext(component.slice(start,end) + '\nstate.result = canDeleteGeneratedQuestion;', {
    props, computed, authStore: { hasRole: () => true }, state,
  })
  assert.equal(state.result.value, false)
})
