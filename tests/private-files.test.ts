import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { ensurePrivateDirectory, writePrivateFile } from '../scripts/private-files.mjs'

test('private provisioning replaces broad ACLs and protects both existing and newly written credentials', async t => {
  const root = await mkdtemp(join(tmpdir(), 'ymx-private-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const directory = join(root, 'private files with spaces')
  await ensurePrivateDirectory(directory)
  if (process.platform === 'win32') execFileSync('icacls.exe', [directory, '/grant', '*S-1-5-32-545:(OI)(CI)RX'], {windowsHide:true, stdio:'pipe'})
  await ensurePrivateDirectory(directory)
  const file = join(directory, 'mock secret.json')
  await writeFile(file, 'old')
  await writePrivateFile(file, 'mock-new-secret')
  const next = join(directory, 'next-secret.json')
  await writePrivateFile(next, 'mock-next-secret')
  assert.equal(await readFile(file, 'utf8'), 'mock-new-secret')
  assert.equal(await readFile(next, 'utf8'), 'mock-next-secret')
  if (process.platform === 'win32') {
    const sid = execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', '[System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value'], {encoding:'utf8', windowsHide:true}).trim()
    for (const target of [directory, file, next]) {
      const trustees = execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', '$acl = if ([System.IO.Directory]::Exists($env:YMX_ACL_TEST_PATH)) { [System.IO.Directory]::GetAccessControl($env:YMX_ACL_TEST_PATH) } else { [System.IO.File]::GetAccessControl($env:YMX_ACL_TEST_PATH) }; $acl.Access | ForEach-Object { $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value }'], {encoding:'utf8', windowsHide:true, env:{...process.env, YMX_ACL_TEST_PATH:target}}).trim().split(/\r?\n/).sort()
      assert.deepEqual(trustees, [sid, 'S-1-5-18', 'S-1-5-32-544'].sort())
    }
  } else {
    assert.equal((await stat(directory)).mode & 0o777, 0o700)
    assert.equal((await stat(file)).mode & 0o777, 0o600)
  }
})
