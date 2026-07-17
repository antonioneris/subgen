$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$repo = if ($env:SUBGEN_REPO) { $env:SUBGEN_REPO } else { "antonioneris/subgen" }
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) {
    "x64"   { $arch = "amd64" }
    "arm64" { $arch = "arm64" }
    default  { throw "Arquitetura Windows não suportada: $architecture" }
}

$asset = "subgen_windows_${arch}.zip"
$baseUrl = "https://github.com/$repo/releases/latest/download"
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("subgen-install-" + [guid]::NewGuid())
$installDir = Join-Path $env:LOCALAPPDATA "Programs\subgen"

try {
    New-Item -ItemType Directory -Path $tempDir -Force | Out-Null
    Write-Host "Baixando subgen para Windows/$arch..."
    $archive = Join-Path $tempDir $asset
    $checksums = Join-Path $tempDir "checksums.txt"
    Invoke-WebRequest "$baseUrl/$asset" -OutFile $archive
    Invoke-WebRequest "$baseUrl/checksums.txt" -OutFile $checksums

    $line = Get-Content $checksums | Where-Object { $_ -match "\s+$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) { throw "Checksum de $asset não encontrado" }
    $expected = ($line -split "\s+")[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "Checksum inválido; download recusado" }

    Expand-Archive -Path $archive -DestinationPath $tempDir -Force
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item (Join-Path $tempDir "subgen.exe") (Join-Path $installDir "subgen.exe") -Force

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $entries = @($userPath -split ";" | Where-Object { $_ })
    if ($entries -notcontains $installDir) {
        $newPath = (@($entries) + $installDir) -join ";"
        [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
        Write-Host "PATH permanente do usuário atualizado."
    }
    if (($env:Path -split ";") -notcontains $installDir) {
        $env:Path = "$installDir;$env:Path"
    }

    if ((-not (Get-Command ffmpeg -ErrorAction SilentlyContinue) -or -not (Get-Command ffprobe -ErrorAction SilentlyContinue)) -and $env:SUBGEN_SKIP_FFMPEG -ne "1") {
        if (Get-Command winget -ErrorAction SilentlyContinue) {
            Write-Host "FFmpeg não encontrado; instalando via WinGet..."
            winget install --id Gyan.FFmpeg -e --accept-source-agreements --accept-package-agreements
            if ($LASTEXITCODE -ne 0) { throw "WinGet não conseguiu instalar o FFmpeg" }
            $env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" + [Environment]::GetEnvironmentVariable("Path", "User")
        }
        else {
            Write-Warning "FFmpeg não encontrado. Instale-o antes de processar vídeos."
        }
    }

    $installedVersion = & (Join-Path $installDir "subgen.exe") version
    Write-Host ""
    Write-Host "✓ $installedVersion instalado em $installDir"
    Write-Host "Execute: subgen config"
}
finally {
    if (Test-Path $tempDir) { Remove-Item $tempDir -Recurse -Force }
}
