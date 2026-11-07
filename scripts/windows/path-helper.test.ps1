# Round-trip test for path-helper.ps1 with install paths that PowerShell or cmd
# would interpret if they were embedded in source. Runs under pwsh (macOS, Linux)
# or Windows PowerShell; it never touches the real user PATH.
#   pwsh -NoProfile -File scripts/windows/path-helper.test.ps1
$ErrorActionPreference = 'Stop'
$helper = Join-Path $PSScriptRoot 'path-helper.ps1'
$exe = (Get-Process -Id $PID).Path
$failures = 0

function Invoke-Helper([string]$action, [string]$dir, [string]$file) {
  $env:DISPATCHQ_INSTDIR = $dir
  $out = & $exe -NoProfile -File $helper -Action $action -PathFile $file 2>&1
  return @{ Code = $LASTEXITCODE; Out = ($out -join "`n") }
}
function Check([bool]$ok, [string]$what) {
  if ($ok) { Write-Host "ok   $what" } else { Write-Host "FAIL $what"; $script:failures++ }
}

$dirs = @(
  'C:\Apps\$cash\DispatchQ',
  'C:\Apps\$(whoami)\DispatchQ',
  'C:\Apps\`backtick`\DispatchQ',
  'C:\Apps\with space\DispatchQ',
  'C:\Apps\quo''te\DispatchQ',
  'C:\Apps\Ünïcödé 日本\DispatchQ',
  'C:\Apps\%PATH%&calc\DispatchQ'
)
foreach ($dir in $dirs) {
  $file = [IO.Path]::GetTempFileName()
  try {
    $other = 'C:\Other\bin;D:\tools'
    [IO.File]::WriteAllText($file, $other)

    $r = Invoke-Helper 'Add' $dir $file
    $v = [IO.File]::ReadAllText($file, [Text.Encoding]::UTF8)
    Check ($r.Code -eq 0 -and $v -ceq "$other;$dir") "add keeps '$dir' literal ($($r.Out))"

    $r = Invoke-Helper 'Add' ($dir + '\') $file
    $v2 = [IO.File]::ReadAllText($file, [Text.Encoding]::UTF8)
    Check ($r.Code -eq 0 -and $v2 -ceq $v) "second add (trailing backslash) does not duplicate '$dir'"

    $r = Invoke-Helper 'Remove' $dir $file
    $v = [IO.File]::ReadAllText($file, [Text.Encoding]::UTF8)
    Check ($r.Code -eq 0 -and $v -ceq $other) "remove restores the original PATH for '$dir'"

    $r = Invoke-Helper 'Remove' $dir $file
    Check ($r.Code -eq 0) "remove of an absent entry succeeds for '$dir'"
  } finally { Remove-Item -LiteralPath $file -ErrorAction SilentlyContinue }
}

# Errors are reported, not swallowed.
$file = [IO.Path]::GetTempFileName()
$r = Invoke-Helper 'Add' '' $file
Check ($r.Code -ne 0 -and $r.Out -match 'DISPATCHQ_INSTDIR') 'a missing directory is an error'
$r = Invoke-Helper 'Add' 'C:\x' '/nonexistent-dir/for/path-file'
Check ($r.Code -ne 0) 'an unwritable target is an error'
Remove-Item -LiteralPath $file -ErrorAction SilentlyContinue

if ($failures -gt 0) { Write-Host "$failures check(s) failed"; exit 1 }
Write-Host 'all path-helper checks passed'
