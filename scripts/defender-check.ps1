# Tests a built frost.exe against Microsoft Defender for scripts/release.sh.
# It turns on frost's scheduled backup, which Defender's behaviour monitoring
# has flagged before, watches Defender for detections, then puts everything
# back, including a "frost backup" task that was already there.
#
#   defender-check.ps1 -Probe
#   defender-check.ps1 -Exe <frost.exe> -Log <file> [-Seconds 120] [-Version v1.2.3]
#
# -Probe prints whether Defender can test anything on this machine. A test
# writes everything it saw to the log. Exit codes: 0 not flagged, 1 flagged,
# 2 the test couldn't run. Keep this file ASCII: Windows PowerShell 5.1 reads
# scripts without a byte order mark as ANSI.
[CmdletBinding()]
param(
  [switch]$Probe,
  [string]$Exe,
  [string]$Log,
  [int]$Seconds = 120,
  [string]$Version = ''
)

$ErrorActionPreference = 'Stop'
$taskName = 'frost backup'
$defenderLog = 'Microsoft-Windows-Windows Defender/Operational'

# Get-Blocker says why Defender can't test frost here, or returns '' if it can.
# The detection frost once hit needed behaviour monitoring and the cloud.
function Get-Blocker {
  try {
    $s = Get-MpComputerStatus
    $p = Get-MpPreference
  } catch {
    return "Microsoft Defender isn't available ($($_.Exception.Message))"
  }
  if (-not $s.AMServiceEnabled -or -not $s.AntivirusEnabled) { return 'Microsoft Defender Antivirus is off' }
  if (-not $s.RealTimeProtectionEnabled) { return "Defender's real-time protection is off" }
  if (-not $s.BehaviorMonitorEnabled) { return "Defender's behaviour monitoring is off" }
  if ($p.MAPSReporting -eq 0) { return "Defender's cloud protection is off" }
  return ''
}

if ($Probe) {
  $why = Get-Blocker
  if ($why) {
    Write-Output "$why, so frost.exe can't be tested against it"
    exit 2
  }
  Write-Output "frost.exe will be tested before publishing (Defender $((Get-MpComputerStatus).AMProductVersion))"
  exit 0
}

if (-not $Exe -or -not $Log) {
  Write-Output 'usage: defender-check.ps1 -Probe | -Exe <frost.exe> -Log <file> [-Seconds 120] [-Version v1.2.3]'
  exit 2
}

$lines = New-Object System.Collections.Generic.List[string]
function Note([string]$s = '') { $lines.Add($s) }
function Section([string]$s) { $lines.Add(''); $lines.Add("== $s") }

$exePath = [IO.Path]::GetFullPath($Exe)
$work = Split-Path $exePath
$config = Join-Path $work 'config'
$cache = Join-Path $work 'cache'
$start = Get-Date
$saved = $null
$present = $false
$reason = "the test didn't finish"

# Invoke-Frost runs frost.exe with the test's own config and cache folders,
# notes what it printed, and returns its exit code.
function Invoke-Frost([string[]]$Arguments) {
  $ErrorActionPreference = 'Continue' # frost's stderr isn't a PowerShell error
  $text = & $exePath --config-dir $config --cache-dir $cache @Arguments 2>&1 | Out-String
  $rc = $LASTEXITCODE
  Note "frost $($Arguments -join ' ') (exit $rc)"
  foreach ($l in ($text -split "`r?`n")) { if ($l.Trim()) { Note "  | $l" } }
  return $rc
}

# Get-Hits returns Defender's detections since the test started that name the
# test's folder or frost's task.
function Get-Hits {
  $events = @(Get-WinEvent -FilterHashtable @{ LogName = $defenderLog; Id = 1116, 1117; StartTime = $start } -ErrorAction SilentlyContinue)
  $events | Where-Object { $_.Message.IndexOf($work, [StringComparison]::OrdinalIgnoreCase) -ge 0 -or $_.Message -match 'Tasks\\frost backup' }
}

function Invoke-Test {
  Section 'frost.exe'
  Note "version     $Version"
  Note "path        $exePath"
  if (-not (Test-Path $exePath)) {
    $script:reason = "there's no frost.exe at $exePath"
    return 2
  }
  $script:present = $true
  Note "sha256      $((Get-FileHash $exePath -Algorithm SHA256).Hash)"
  $os = Get-CimInstance Win32_OperatingSystem
  Note "windows     $($os.Caption) $($os.Version)"

  Section 'Defender'
  $why = Get-Blocker
  if ($why) {
    $script:reason = $why
    Note $why
    return 2
  }
  $s = Get-MpComputerStatus
  $p = Get-MpPreference
  Note "product     $($s.AMProductVersion), engine $($s.AMEngineVersion)"
  Note "signatures  $($s.AntivirusSignatureVersion), updated $($s.AntivirusSignatureLastUpdated)"
  Note "settings    real-time $($s.RealTimeProtectionEnabled), behaviour monitoring $($s.BehaviorMonitorEnabled), tamper protection $($s.IsTamperProtected)"
  Note "cloud       level $($p.MAPSReporting), block level $($p.CloudBlockLevel), sample submission $($p.SubmitSamplesConsent)"

  if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    $script:saved = Export-ScheduledTask -TaskName $taskName
    Note "A '$taskName' task was already here. It's saved and goes back afterwards."
  }
  New-Item -ItemType Directory -Force $config, $cache | Out-Null
  Set-Content -Path (Join-Path $config 'config.toml') -Value "[schedule]`nenabled = false`nevery = `"daily`"" -Encoding Ascii

  Section 'Turning on the schedule'
  $script:start = Get-Date
  Note "started     $($start.ToString('u'))"
  $rc = Invoke-Frost @('config', 'set', 'schedule.enabled', 'true')
  $registered = [bool](Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)
  if ($registered) {
    Note 'The task frost registered:'
    foreach ($l in ((Export-ScheduledTask -TaskName $taskName) -split "`r?`n")) { Note "  $l" }
  } else {
    Note 'No task was registered.'
  }

  Section "Watching Defender for $Seconds seconds"
  $hits = @()
  $deadline = $start.AddSeconds($Seconds)
  while ((Get-Date) -lt $deadline) {
    Start-Sleep -Seconds 5
    $hits = @(Get-Hits)
    if ($hits | Where-Object Id -eq 1117) {
      Start-Sleep -Seconds 5 # let the rest of the remediation land in the log
      $hits = @(Get-Hits)
      break
    }
  }
  if ($hits.Count) {
    $script:reason = 'Defender flagged frost.exe'
    foreach ($e in ($hits | Sort-Object TimeCreated)) {
      Note "event $($e.Id) at $($e.TimeCreated.ToString('u')), $([int]($e.TimeCreated - $start).TotalSeconds)s after the schedule was turned on:"
      foreach ($l in ($e.Message -split "`r?`n")) { if ($l.Trim()) { Note "  $($l.Trim())" } }
    }
    foreach ($d in @(Get-MpThreatDetection -ErrorAction SilentlyContinue | Where-Object { $_.InitialDetectionTime -ge $start })) {
      Note "detection   threat $($d.ThreatID), process $($d.ProcessName), action success $($d.ActionSuccess)"
      foreach ($r in $d.Resources) { Note "  $r" }
    }
    return 1
  }
  if ($rc -ne 0 -or -not $registered) {
    $script:reason = "frost couldn't turn on its schedule (exit $rc), so there was nothing for Defender to judge"
    return 2
  }
  Note 'Nothing was flagged.'
  $script:reason = 'Defender didn''t flag frost.exe'
  return 0
}

function Invoke-Cleanup {
  Section 'Cleaning up'
  if (Test-Path $exePath) {
    [void](Invoke-Frost @('config', 'set', 'schedule.enabled', 'false'))
  } elseif ($present) {
    Note 'frost.exe is gone, so Defender removed it.'
  }
  if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
    Note "Removed the '$taskName' task."
  }
  if ($saved) {
    Register-ScheduledTask -TaskName $taskName -Xml $saved | Out-Null
    Note "Put the '$taskName' task that was already here back."
  }
}

$code = 2
try {
  $code = Invoke-Test | Select-Object -Last 1
} catch {
  $code = 2
  $reason = "the test broke: $($_.Exception.Message)"
  Section 'Error'
  Note ($_ | Out-String).Trim()
} finally {
  try {
    Invoke-Cleanup
  } catch {
    Note "Cleanup failed: $($_.Exception.Message)"
    if ($code -eq 0) {
      $code = 2
      $reason = "cleanup failed: $($_.Exception.Message)"
    }
  }
  $verdict = switch ($code) { 0 { 'PASSED' } 1 { 'FLAGGED' } default { "COULDN'T TEST" } }
  $header = @("frost Windows Defender check: $verdict", $reason, "finished $((Get-Date).ToString('u'))")
  Set-Content -Path $Log -Value ($header + $lines) -Encoding UTF8
  Write-Output "${verdict}: $reason"
}
exit $code
