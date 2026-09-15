#Requires -Version 5.1
<#
.SYNOPSIS
  Trigger a GhostFleet incremental data-generation run — e.g. as a backup-job
  *post* script, so the next backup finds fresh incremental changes.

.DESCRIPTION
  Resolves a deployment by name or id, then POSTs an incremental fill. By
  default it returns as soon as the run is accepted (fire-and-forget). With
  -Wait it polls until the run finishes and exits non-zero if it fails — useful
  when the backup job should block on completion.

  Exit codes: 0 success, 1 run failed, 2 run cancelled, 3 timed out, and a
  terminating error (non-zero) if the API rejects the request (e.g. the
  deployment isn't ready, or has no prior initial fill).

.PARAMETER ControllerUrl
  Base URL of the controller, e.g. http://ghostfleet.lab (port 80) or
  http://ghostfleet.lab:8080.

.PARAMETER Deployment
  Deployment name (case-sensitive) or id.

.PARAMETER ApiKey
  Optional static API key (sent as 'Authorization: Bearer'). Only needed if the
  controller has GHOSTFLEET_API_KEYS configured.

.PARAMETER Wait
  Block until the run finishes; the exit code then reflects success/failure.

.PARAMETER TimeoutSec
  Max seconds to wait when -Wait is set (default 3600).

.PARAMETER SkipCertificateCheck
  Ignore TLS validation (PowerShell 7+), for a reverse proxy with a self-signed
  cert in front of the controller.

.EXAMPLE
  ./Invoke-GhostfleetIncremental.ps1 -ControllerUrl http://ghostfleet.lab -Deployment "CR-Ghostfleet"

.EXAMPLE
  ./Invoke-GhostfleetIncremental.ps1 -ControllerUrl http://ghostfleet.lab -Deployment "CR-Ghostfleet" -ApiKey $env:GF_KEY -Wait
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory)] [string] $ControllerUrl,
  [Parameter(Mandatory)] [string] $Deployment,
  [string] $ApiKey,
  [switch] $Wait,
  [int] $TimeoutSec = 3600,
  [switch] $SkipCertificateCheck
)

$ErrorActionPreference = 'Stop'
$base = $ControllerUrl.TrimEnd('/')

$common = @{ Headers = @{} }
if ($ApiKey) { $common.Headers['Authorization'] = "Bearer $ApiKey" }
if ($SkipCertificateCheck -and $PSVersionTable.PSVersion.Major -ge 6) {
  $common['SkipCertificateCheck'] = $true
}

function Invoke-GF {
  param([string] $Method, [string] $Path, $Body)
  $req = @{ Method = $Method; Uri = "$base$Path" } + $common
  if ($null -ne $Body) {
    $req['Body'] = ($Body | ConvertTo-Json -Compress)
    $req['ContentType'] = 'application/json'
  }
  try {
    return Invoke-RestMethod @req
  } catch {
    $msg = $_.ErrorDetails.Message
    if (-not $msg) { $msg = $_.Exception.Message }
    throw "GhostFleet API $Method $Path failed: $msg"
  }
}

# 1. Resolve the deployment: exact id first, then exact (case-sensitive) name.
$deployments = Invoke-GF -Method GET -Path '/api/v1/deployments'
$dep = $deployments | Where-Object { $_.id -eq $Deployment } | Select-Object -First 1
if (-not $dep) {
  $dep = $deployments | Where-Object { $_.name -ceq $Deployment } | Select-Object -First 1
}
if (-not $dep) { throw "No deployment with id or name '$Deployment'." }
Write-Host "Deployment '$($dep.name)' ($($dep.id)) status=$($dep.status) filled=$($dep.filled)"

# 2. Trigger the incremental run.
$run = Invoke-GF -Method POST -Path "/api/v1/deployments/$($dep.id)/fill" -Body @{ type = 'incremental' }
Write-Host "Started incremental run $($run.id)."

if (-not $Wait) { exit 0 }

# 3. Poll until the run reaches a terminal state.
$deadline = (Get-Date).AddSeconds($TimeoutSec)
while ((Get-Date) -lt $deadline) {
  Start-Sleep -Seconds 5
  $runs = Invoke-GF -Method GET -Path "/api/v1/deployments/$($dep.id)/runs"
  $r = $runs | Where-Object { $_.id -eq $run.id } | Select-Object -First 1
  if (-not $r) { continue }
  switch ($r.status) {
    'succeeded' { Write-Host "Incremental run succeeded ($($r.stats))."; exit 0 }
    'failed'    { Write-Error "Incremental run failed: $($r.stats)"; exit 1 }
    'cancelled' { Write-Error 'Incremental run was cancelled.'; exit 2 }
  }
}
Write-Error "Timed out after ${TimeoutSec}s waiting for the incremental run."
exit 3
