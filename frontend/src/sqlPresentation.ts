// Presentation only: exports and execution continue to use the original SQL.
export function formatSQL(sql: string): string {
  const tokens = sql.match(/--[^\n]*|\/\*[\s\S]*?\*\/|'(?:''|\\.|[^'\\])*'|"(?:""|\\.|[^"\\])*"|`(?:``|[^`])*`|[(),]|\s+|[^\s(),`'"]+/g) || []
  const alter = /^\s*ALTER\s+TABLE\b/i.test(sql)
  let depth = 0
  let startedAlterClauses = false
  let output = ''
  for (const token of tokens) {
    if (alter && !startedAlterClauses && depth === 0 && /^(ADD|DROP|MODIFY|CHANGE|RENAME|ALTER|CONVERT)$/i.test(token) && output.trim().length > 5) {
      output = output.trimEnd() + '\n  '
      startedAlterClauses = true
    }
    if (/^\s+$/.test(token) && output.endsWith('\n  ')) continue
    output += token
    if (token === '(') depth++
    if (token === ')') depth--
    if (alter && token === ',' && depth === 0) output = output.trimEnd() + '\n  '
  }
  return output.replace(/;\s*$/, '').trim() + ';'
}
