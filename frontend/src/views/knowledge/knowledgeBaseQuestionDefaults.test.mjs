import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const source = readFileSync(new URL('./KnowledgeBaseEditorModal.vue', import.meta.url), 'utf8')

test('new KB defaults question generation off without overriding existing KB settings', () => {
  const create = source.slice(source.indexOf('const initFormData ='), source.indexOf('const loadKBData ='))
  assert.match(create, /questionGenerationConfig:\s*\{\s*enabled: false,/)
  assert.match(source, /enabled: kb\.question_generation_config\?\.enabled \|\| false/)
  assert.match(source, /summary_model_id: formData\.value\.modelConfig\.llmModelId/)
  assert.match(source, /embedding_model_id: formData\.value\.modelConfig\.embeddingModelId/)
})
