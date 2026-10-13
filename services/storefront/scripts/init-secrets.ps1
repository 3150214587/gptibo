$ErrorActionPreference = 'Stop'
$secretDirectory = Join-Path $PSScriptRoot '../.secrets'
New-Item -ItemType Directory -Path $secretDirectory -Force | Out-Null
foreach ($secretName in @('owner-password', 'app-password', 'auth-secret')) {
    $secretPath = Join-Path $secretDirectory $secretName
    if (Test-Path -LiteralPath $secretPath) {
        Write-Host "$secretName already exists; kept unchanged."
        continue
    }
    $secretBytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($secretBytes)
    $secretText = [Convert]::ToHexString($secretBytes).ToLowerInvariant()
    # CreateNew prevents overwriting a concurrently created secret.
    $secretStream = [IO.File]::Open($secretPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
    try {
        $encodedSecret = [Text.Encoding]::UTF8.GetBytes($secretText)
        $secretStream.Write($encodedSecret, 0, $encodedSecret.Length)
    } finally { $secretStream.Dispose() }
    Write-Host "$secretName created (value not printed)."
}
