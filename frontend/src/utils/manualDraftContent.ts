// Drafts have no indexed chunks; only use the body returned by the authorized detail API.
export function manualDraftContent(knowledge: {
  type?: string
  parse_status?: string
  metadata?: unknown
}): string | null {
  if (knowledge.type !== 'manual' || knowledge.parse_status !== 'draft') return null
  try {
    const metadata = typeof knowledge.metadata === 'string'
      ? JSON.parse(knowledge.metadata)
      : knowledge.metadata
    if (!metadata || typeof metadata !== 'object') return null
    const value = metadata as { status?: unknown; content?: unknown }
    if (value.status && value.status !== 'draft') return null
    return typeof value.content === 'string' ? value.content : null
  } catch {
    return null
  }
}
