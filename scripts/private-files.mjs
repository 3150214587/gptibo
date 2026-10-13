import { chmod, mkdir, open, writeFile } from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const protectAcl = `
$ErrorActionPreference = 'Stop'
$item = Get-Item -LiteralPath $env:YMX_PRIVATE_PATH -Force
$currentSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$inheritance = [System.Security.AccessControl.InheritanceFlags]::None
if ($item.PSIsContainer) {
  $security = New-Object System.Security.AccessControl.DirectorySecurity
  $inheritance = [System.Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
} else {
  $security = New-Object System.Security.AccessControl.FileSecurity
}
$security.SetAccessRuleProtection($true, $false)
foreach ($sid in @($currentSid, [System.Security.Principal.SecurityIdentifier]'S-1-5-18', [System.Security.Principal.SecurityIdentifier]'S-1-5-32-544')) {
  $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', $inheritance, 'None', 'Allow')
  $security.AddAccessRule($rule)
}
if ($item.PSIsContainer) { [System.IO.Directory]::SetAccessControl($item.FullName, $security) }
else { [System.IO.File]::SetAccessControl($item.FullName, $security) }
`

export async function protectPrivatePath(path, directory = false) {
  if (process.platform === 'win32') {
    // Paths are data in an environment variable, never interpolated shell code.
    execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', protectAcl], { windowsHide: true, stdio: 'pipe', env: { ...process.env, YMX_PRIVATE_PATH: path instanceof URL ? fileURLToPath(path) : path } })
  } else await chmod(path, directory ? 0o700 : 0o600)
}
export async function ensurePrivateDirectory(path) {
  await mkdir(path, { recursive: true, mode: 0o700 })
  await protectPrivatePath(path, true)
}
export async function writePrivateFile(path, content) {
  // Protect new/existing files before writing a credential, including .dev.vars.
  const file = await open(path, 'a', 0o600)
  await file.close()
  await protectPrivatePath(path)
  await writeFile(path, content, { mode: 0o600 })
}
