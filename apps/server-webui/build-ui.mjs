import { spawnSync } from 'node:child_process'
import { cp, readdir, rm } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const root = new URL('../../', import.meta.url)
const result = spawnSync('pnpm', ['--filter', '@metacubexd/ui', 'generate'], {
  cwd: root,
  env: { ...process.env, MCXD_GO_SERVER: 'true', NUXT_APP_BASE_URL: '/' },
  stdio: 'inherit',
  shell: process.platform === 'win32',
})
if (result.error) throw result.error
if (result.status !== 0) process.exit(result.status ?? 1)

const source = new URL('packages/ui/.output/public/', root)
const target = new URL('./web/', import.meta.url)
for (const name of await readdir(target)) {
  if (name !== 'placeholder.txt')
    await rm(new URL(name, target), { recursive: true })
}
await cp(fileURLToPath(source), fileURLToPath(target), { recursive: true })
