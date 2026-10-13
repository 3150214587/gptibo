import {readFileSync} from 'node:fs'
const extensions = JSON.parse(readFileSync(new URL('../internal/importer/legacy-extensions.json', import.meta.url), 'utf8'))
// Only explicitly supported additive columns are accepted. Never drop unknown data.
export function supportedColumns(table, actual) {
  const extra = extensions[table.name] ?? []
  const expected = [...table.columns, ...extra.filter(c => actual.some(a => a.name === c.name))]
  const canonical = columns => JSON.stringify([...columns].sort((a,b) => a.name.localeCompare(b.name)))
  return canonical(actual) === canonical(expected)
}
