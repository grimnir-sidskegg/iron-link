# iron-link daemon installer (Windows). Run as Administrator.
# Copies the self-contained daemon into %ProgramFiles%\iron-link and registers
# it as an auto-start Windows service (runs as LocalSystem in the background —
# no console window, no per-launch UAC). The daemon embeds its cores (and
# wintun): there is no cores\ dir and no driver to install. For the full app +
# GUI use iron-link-*-setup.exe, which bundles this daemon with the Flutter
# client and does the same service registration.
$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$install = Join-Path $env:ProgramFiles "iron-link"
$daemon = Join-Path $install "iron-link-daemon.exe"

# Stop a previous service so its exe can be overwritten (upgrade); no-op on a
# first install.
if (Test-Path $daemon) { & $daemon stop 2>$null }

New-Item -ItemType Directory -Force -Path $install | Out-Null
Write-Host "-> daemon -> $install"
Copy-Item (Join-Path $here "iron-link-daemon.exe") $daemon -Force

Write-Host "-> registering + starting the iron-link service"
& $daemon install
& $daemon start

Write-Host ""
Write-Host "done. The iron-link service is running (services.msc) and auto-starts on boot."
Write-Host "Stop/remove it with:  `"$daemon`" stop  /  `"$daemon`" uninstall"
