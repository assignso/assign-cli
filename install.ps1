$ErrorActionPreference = 'Stop'
$repo = 'assignso/assign-cli'
$installDir = if ($env:ASSIGN_INSTALL_DIR) { $env:ASSIGN_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Assign\bin' }
$version = $env:ASSIGN_VERSION
if (-not $version) {
  $version = (Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ 'User-Agent' = 'assign-cli-installer' }).tag_name
}
if (-not $version -or $version -notmatch '^[0-9A-Za-z.-]+$') { throw 'Could not determine a valid Assign release version.' }

$arch = if ([Environment]::Is64BitOperatingSystem -and [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() -eq 'Arm64') { 'arm64' } else { 'amd64' }
$archive = "assign_${version}_windows_${arch}.zip"
$checksums = "assign_${version}_SHA256SUMS"
$base = "https://github.com/$repo/releases/download/$version"
$temp = Join-Path ([System.IO.Path]::GetTempPath()) ("assign-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temp | Out-Null
try {
  Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $temp $archive)
  Invoke-WebRequest -Uri "$base/$checksums" -OutFile (Join-Path $temp $checksums)
  $expected = ((Get-Content (Join-Path $temp $checksums)) | Where-Object { $_ -match ("  " + [regex]::Escape($archive) + '$') } | Select-Object -First 1).Split(' ')[0]
  if (-not $expected -or (Get-FileHash (Join-Path $temp $archive) -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected.ToLowerInvariant()) { throw 'Release checksum verification failed.' }
  Expand-Archive -Path (Join-Path $temp $archive) -DestinationPath $temp -Force
  $binary = Join-Path $temp 'assign.exe'
  if (-not (Test-Path $binary)) { throw 'Release archive did not contain assign.exe.' }
  if ((Get-AuthenticodeSignature $binary).Status -ne 'Valid') { throw 'The Assign release is not Authenticode signed; installation stopped.' }
  New-Item -ItemType Directory -Force -Path $installDir | Out-Null
  Copy-Item $binary (Join-Path $installDir 'assign.exe') -Force
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $installDir) { [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ';' + $installDir).TrimStart(';')), 'User') }
  Write-Output "Installed assign $version to $installDir\assign.exe. Open a new terminal, then run: assign login"
} finally { Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue }
