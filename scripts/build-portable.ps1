param(
  [string]$Version = "0.1.0",
  [switch]$SkipWeb,
  [switch]$IncludeLinux
)

$ErrorActionPreference = "Stop"

$RootDir = Split-Path -Parent $PSScriptRoot
Push-Location $RootDir
try {
  if (-not $SkipWeb) {
    Write-Host "[1/4] Building Web UI..."
    npm --prefix web run build
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  }

  $DistDir = Join-Path $RootDir "dist"
  $WinStage = Join-Path $DistDir "tidegate-windows-amd64"
  New-Item -ItemType Directory -Force -Path $DistDir | Out-Null
  if (Test-Path $WinStage) {
    Remove-Item -Recurse -Force $WinStage
  }
  New-Item -ItemType Directory -Force -Path $WinStage | Out-Null

  $LdFlags = "-s -w -X main.version=$Version"
  $env:CGO_ENABLED = "0"

  Write-Host "[2/4] Building Windows amd64 portable binary ($Version)..."
  $env:GOOS = "windows"
  $env:GOARCH = "amd64"
  $WinExe = Join-Path $WinStage "tidegate.exe"
  go build -trimpath -ldflags="$LdFlags" -o $WinExe ./cmd/tidegate
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

  "" | Set-Content -Path (Join-Path $WinStage ".portable") -NoNewline -Encoding ASCII

  $StartBat = @(
    '@echo off',
    'setlocal',
    'cd /d "%~dp0"',
    '"%~dp0tidegate.exe" serve --tls-self-signed --data "%~dp0data" %*'
  ) -join "`r`n"
  Set-Content -Path (Join-Path $WinStage "start-portable.bat") -Value $StartBat -Encoding ASCII

  $WinZip = Join-Path $DistDir "tidegate-windows-amd64.zip"
  if (Test-Path $WinZip) {
    Remove-Item -Force $WinZip
  }
  Compress-Archive -Path (Join-Path $WinStage "*") -DestinationPath $WinZip

  $Artifacts = @($WinExe, $WinZip)

  if ($IncludeLinux) {
    Write-Host "[3/4] Building Linux amd64 / arm64 static binaries ($Version)..."
    $env:GOOS = "linux"
    foreach ($arch in @("amd64", "arm64")) {
      $env:GOARCH = $arch
      $OutBin = Join-Path $DistDir "tidegate-linux-$arch"
      go build -trimpath -ldflags="$LdFlags" -o $OutBin ./cmd/tidegate
      if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
      $Artifacts += $OutBin
    }
  } else {
    Write-Host "[3/4] Skipping Linux binaries (pass -IncludeLinux to include)."
  }

  Write-Host "[4/4] Writing SHA256SUMS.txt..."
  $SumLines = foreach ($item in $Artifacts) {
    $hash = (Get-FileHash -Path $item -Algorithm SHA256).Hash.ToLowerInvariant()
    $name = Split-Path -Leaf $item
    "$hash  $name"
  }
  $SumFile = Join-Path $DistDir "SHA256SUMS.txt"
  Set-Content -Path $SumFile -Value $SumLines -Encoding ASCII

  Write-Host "Portable artifacts ready in ${DistDir}:"
  $SumLines | ForEach-Object { Write-Host "  $_" }
} finally {
  Remove-Item Env:GOOS -ErrorAction SilentlyContinue
  Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
  Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
  Pop-Location
}
