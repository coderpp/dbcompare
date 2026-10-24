<script setup lang="ts">
import { computed } from 'vue'
const props = withDefaults(defineProps<{ sql: string; startLine?: number }>(), { startLine: 1 })
// Tokenize for presentation only. Text is rendered by Vue, never injected as HTML.
const lines = computed(() => {
  const tokens = props.sql.match(/--[^\n]*|\/\*[\s\S]*?\*\/|'(?:''|\\.|[^'\\])*'|"(?:""|\\.|[^"\\])*"|`(?:``|[^`])*`|\b(?:ALTER|TABLE|CREATE|DROP|ADD|MODIFY|CHANGE|COLUMN|INDEX|KEY|PRIMARY|UNIQUE|CONSTRAINT|FOREIGN|REFERENCES|ON|DELETE|UPDATE|SET|NULL|NOT|DEFAULT|COMMENT|CHARACTER|COLLATE|CHARSET|ENGINE|AUTO_INCREMENT|RENAME|TO|USING|CASCADE|RESTRICT|IF|EXISTS|AFTER|FIRST)\b|\b\d+(?:\.\d+)?\b|\b[A-Za-z_][A-Za-z_0-9]*\b|\s+|./gi) || []
  const result: { text: string; kind: string }[][] = [[]]
  for (const text of tokens) {
    const kind = text.startsWith('--') || text.startsWith('/*') ? 'comment' : /^["']/.test(text) ? 'string' : text.startsWith('`') ? 'identifier' : /^\d/.test(text) ? 'number' : /^(ALTER|TABLE|CREATE|DROP|ADD|MODIFY|CHANGE|COLUMN|INDEX|KEY|PRIMARY|UNIQUE|CONSTRAINT|FOREIGN|REFERENCES|ON|DELETE|UPDATE|SET|NULL|NOT|DEFAULT|COMMENT|CHARACTER|COLLATE|CHARSET|ENGINE|AUTO_INCREMENT|RENAME|TO|USING|CASCADE|RESTRICT|IF|EXISTS|AFTER|FIRST)$/i.test(text) ? 'keyword' : 'plain'
    text.split('\n').forEach((part, index) => {
      if (index) result.push([])
      if (part) result[result.length - 1].push({ text: part, kind })
    })
  }
  return result
})
</script>

<template>
  <div class="code-block" aria-label="SQL 语句">
    <div v-for="(line, index) in lines" :key="index" class="code-line"><span class="line-number" aria-hidden="true">{{ startLine + index }}</span><code><span v-for="(token, j) in line" :key="j" :class="`token-${token.kind}`">{{ token.text }}</span></code></div>
  </div>
</template>

<style scoped>
.code-block { padding: 3px 0 14px; font: 13px/1.85 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace; color: #334155; tab-size: 2; }
.code-line { display: flex; align-items: stretch; min-height: 23px; }
.line-number { flex: 0 0 42px; padding: 0 11px 0 4px; text-align: right; color: #94a3b8; background: #f7f9fb; user-select: none; font-variant-numeric: tabular-nums; }
code { padding: 0 12px; white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; flex: 1; font: inherit; }
.token-keyword { color: #2456db; }
.token-identifier { color: #087e75; }
.token-string { color: #697c45; }
.token-number { color: #8b5fc1; }
.token-comment { color: #8491a3; }
</style>
