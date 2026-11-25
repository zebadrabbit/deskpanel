# Flash build/ (from build.sh) to the panel, from the PC it's plugged into, then show its log.
# Uses PlatformIO's bundled esptool if present, else `python -m esptool` (pip install esptool).
param([string]$Port = "COM11", [switch]$NoMonitor)
$ErrorActionPreference = 'Stop'
$pioPy = "$env:USERPROFILE\.platformio\penv\Scripts\python.exe"
$pioEsptool = "$env:USERPROFILE\.platformio\packages\tool-esptoolpy\esptool.py"
$py, $esptool = if (Test-Path $pioEsptool) { $pioPy, @($pioEsptool) } else { "python", @("-m", "esptool") }

Push-Location "$PSScriptRoot\build"
try { & $py @esptool --chip esp32s3 -p $Port -b 921600 write_flash "@flash_args" }
finally { Pop-Location }
if ($LASTEXITCODE -ne 0) { throw "esptool failed ($LASTEXITCODE)" }
if (-not $NoMonitor) { & $py -m serial.tools.miniterm --raw $Port 115200 }
