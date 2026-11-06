# Adds or removes the DispatchQ install directory in the user's PATH.
#
# The directory arrives in the DISPATCHQ_INSTDIR environment variable and is only
# ever handled as data. It is never part of any script source, so characters that
# PowerShell treats specially ($, `, quotes, parentheses) in a legal install path
# stay literal.
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File path-helper.ps1 -Action Add|Remove
# Exit code 0 on success; any failure writes to stderr and exits non-zero.
#
# -PathFile reads and writes the PATH value in a text file instead of the user
# environment. It exists for scripts/windows/path-helper.test.ps1 only.
param(
  [Parameter(Mandatory = $true)][ValidateSet('Add', 'Remove')][string]$Action,
  [string]$PathFile
)

$ErrorActionPreference = 'Stop'
try {
  $dir = $env:DISPATCHQ_INSTDIR
  if ([string]::IsNullOrWhiteSpace($dir)) { throw 'DISPATCHQ_INSTDIR is not set' }
  $dir = $dir.TrimEnd('\')

  if ($PathFile) {
    $current = if (Test-Path -LiteralPath $PathFile) { [IO.File]::ReadAllText($PathFile, [Text.Encoding]::UTF8) } else { '' }
  } else {
    $current = [Environment]::GetEnvironmentVariable('Path', 'User')
  }
  if (-not $current) { $current = '' }

  # Entries are compared without a trailing backslash and ignoring case, as
  # Windows does. Other entries are kept exactly as they are.
  $same = { param($e) $e.TrimEnd('\') -ieq $dir }
  $entries = @($current -split ';' | Where-Object { $_ })
  $present = @($entries | Where-Object { & $same $_ }).Count -gt 0

  if ($Action -eq 'Add') {
    if ($present) { exit 0 }
    $entries += $dir
  } else {
    if (-not $present) { exit 0 }
    $entries = @($entries | Where-Object { -not (& $same $_) })
  }
  $new = $entries -join ';'

  if ($PathFile) {
    [IO.File]::WriteAllText($PathFile, $new, (New-Object Text.UTF8Encoding($false)))
  } else {
    [Environment]::SetEnvironmentVariable('Path', $new, 'User')
  }
  exit 0
} catch {
  [Console]::Error.WriteLine("path-helper: $($_.Exception.Message)")
  exit 1
}
