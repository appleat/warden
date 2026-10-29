# Warden 跨平台构建脚本（PowerShell 实现，由 build.bat 或 pwsh 直接调用）
#
# 用法：
#   build.bat                          构建全部平台（含前端）
#   build.bat linux                    只构建 linux/amd64 与 linux/arm64
#   build.bat linux/arm64              只构建 linux/arm64
#   build.bat windows -Pack            构建 Windows 版并打 zip
#   build.bat -Version 1.2.0 -Clean    指定版本号并先清空 dist
#   build.bat -SkipWeb                 跳过前端构建（复用 web\dist）

[CmdletBinding()]
param(
    [Parameter(Position = 0, ValueFromRemainingArguments = $true)]
    [string[]]$Target,

    [Alias('v')]
    [string]$Version = '1.0.0',

    [switch]$SkipWeb,
    [switch]$Pack,
    [switch]$Clean,

    [Alias('h')]
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }

$App = 'warden'
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$Out = Join-Path $Root 'dist'
$Failed = $false
$BuiltCount = 0

function Write-Head([string]$Text) {
    Write-Host ''
    Write-Host "== $Text" -ForegroundColor Cyan
}

function Write-Ok([string]$Text) {
    Write-Host "   $Text" -ForegroundColor Green
}

function Write-Warn([string]$Text) {
    Write-Host "   $Text" -ForegroundColor Yellow
}

function Write-Err([string]$Text) {
    Write-Host "   $Text" -ForegroundColor Red
}

function Show-Usage {
    Write-Host ''
    Write-Host 'Warden 构建脚本' -ForegroundColor Cyan
    Write-Host ''
    Write-Host '  build.bat                          构建全部平台（含前端）'
    Write-Host '  build.bat linux                    只构建 linux/amd64 与 linux/arm64'
    Write-Host '  build.bat linux/arm64              只构建 linux/arm64'
    Write-Host '  build.bat windows -Pack            构建 Windows 版并打 zip'
    Write-Host '  build.bat -SkipWeb                 跳过前端，只用现有 web\dist'
    Write-Host '  build.bat -Version 1.2.0 -Clean    指定版本号并先清空 dist'
    Write-Host ''
    Write-Host '  支持平台：windows/amd64 windows/arm64 linux/amd64 linux/arm64'
    Write-Host '            darwin/amd64 darwin/arm64'
    Write-Host ''
    Write-Host '  产物在 dist\ 目录，同一份二进制既能当控制端也能当被管端：'
    Write-Host '    warden-linux-amd64 -mode server'
    Write-Host '    warden-linux-amd64 -mode agent -agent-listen :8090 -agent-token <令牌>'
    Write-Host ''
}

if ($Help) {
    Show-Usage
    exit 0
}

Write-Host '============================================================' -ForegroundColor DarkGray
Write-Host " Warden 构建   版本 $Version" -ForegroundColor White
Write-Host " 输出目录 $Out" -ForegroundColor DarkGray
Write-Host '============================================================' -ForegroundColor DarkGray

# ---------------------------------------------------------------- 环境检查
Write-Head '检查构建环境'

$goCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCmd) {
    Write-Err '未找到 go 命令，请安装 Go 1.21+ 并加入 PATH'
    exit 1
}
Write-Ok (& go version)

$Built = (Get-Date).ToString('yyyy-MM-ddTHH:mm:ss')
Write-Ok "构建时间 $Built"

if ($Clean -and (Test-Path $Out)) {
    Write-Ok "清空 $Out"
    Remove-Item -Recurse -Force $Out
}
if (-not (Test-Path $Out)) {
    New-Item -ItemType Directory -Path $Out | Out-Null
}

# ---------------------------------------------------------------- 前端构建
Write-Head '构建前端'

if ($SkipWeb) {
    if (-not (Test-Path (Join-Path $Root 'web\dist\index.html'))) {
        Write-Err '找不到 web\dist\index.html，请不要加 -SkipWeb 直接构建'
        exit 1
    }
    Write-Warn '已跳过（复用现有 web\dist）'
} else {
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Write-Err '未找到 npm 命令，请安装 Node.js 18+ 并加入 PATH'
        exit 1
    }
    $webDir = Join-Path $Root 'web'
    Push-Location $webDir
    try {
        if (-not (Test-Path (Join-Path $webDir 'node_modules'))) {
            Write-Ok '安装依赖 npm install ...'
            & npm install --no-audit --no-fund
            if ($LASTEXITCODE -ne 0) { throw 'npm install 失败' }
        }
        Write-Ok 'vite build ...'
        & npm run build
        if ($LASTEXITCODE -ne 0) { throw '前端构建失败（vite build）' }
        Write-Ok '前端产物已更新 web\dist'
    } catch {
        Write-Err $_.Exception.Message
        exit 1
    } finally {
        Pop-Location
    }
}

# ---------------------------------------------------------------- 交叉编译
Write-Head '交叉编译'

# SQLite 用纯 Go 实现，必须关闭 CGO，否则无法交叉编译
$env:CGO_ENABLED = '0'

$matrix = @(
    @('windows', 'amd64'),
    @('windows', 'arm64'),
    @('linux', 'amd64'),
    @('linux', 'arm64'),
    @('darwin', 'amd64'),
    @('darwin', 'arm64')
)

function Test-Selected([string]$os, [string]$arch) {
    if (-not $Target -or $Target.Count -eq 0) { return $true }
    foreach ($t in $Target) {
        if ($t -eq 'all') { return $true }
        if ($t -ieq $os) { return $true }
        if ($t -ieq "$os/$arch") { return $true }
    }
    return $false
}

Push-Location $Root
try {
    foreach ($m in $matrix) {
        $os = $m[0]
        $arch = $m[1]
        if (-not (Test-Selected $os $arch)) { continue }

        $ext = ''
        if ($os -eq 'windows') { $ext = '.exe' }
        $name = "$App-$os-$arch$ext"
        $outFile = Join-Path $Out $name

        Write-Host ''
        Write-Host " [构建] $os/$arch -> dist\$name" -ForegroundColor White
        $env:GOOS = $os
        $env:GOARCH = $arch

        & go build -trimpath -ldflags "-s -w -X main.version=$Version -X main.buildTime=$Built" -o $outFile ./cmd/server
        if ($LASTEXITCODE -ne 0) {
            Write-Err "$os/$arch 编译失败"
            $Failed = $true
            continue
        }

        $BuiltCount++
        $mb = [math]::Round((Get-Item $outFile).Length / 1MB, 1)
        Write-Ok "$name ($mb MB)"

        if ($Pack) {
            $stage = Join-Path $Out ".stage-$os-$arch"
            $zip = Join-Path $Out "$App-$Version-$os-$arch.zip"
            if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
            New-Item -ItemType Directory -Path $stage | Out-Null
            Copy-Item $outFile $stage
            Copy-Item (Join-Path $Root 'config.yaml') $stage
            # 部署脚本是给 Linux 用的，Windows 包只带二进制和配置
            if ($os -ne 'windows') {
                Copy-Item (Join-Path $Root 'deploy') $stage -Recurse
            }
            try {
                Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zip -Force
                Write-Ok "打包 $App-$Version-$os-$arch.zip"
            } catch {
                Write-Warn "打包失败：$($_.Exception.Message)"
            }
            Remove-Item -Recurse -Force $stage
        }
    }
} finally {
    Pop-Location
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
}

# ---------------------------------------------------------------- 结果汇总
Write-Host ''
if ($Failed) {
    Write-Host '============================================================' -ForegroundColor Red
    Write-Host ' 构建失败，请查看上面的错误信息' -ForegroundColor Red
    Write-Host '============================================================' -ForegroundColor Red
    exit 1
}

if ($BuiltCount -eq 0) {
    Write-Warn "没有匹配到任何平台：$($Target -join ' ')"
    Write-Warn '可用平台名：windows linux darwin，或精确到 windows/amd64、linux/arm64 ...'
    exit 1
}

$files = Get-ChildItem $Out -File | Sort-Object Name

Write-Host '============================================================' -ForegroundColor Green
Write-Host ' 构建完成，产物在 dist\' -ForegroundColor Green
foreach ($f in $files) {
    $mb = [math]::Round($f.Length / 1MB, 1)
    Write-Host ("   {0,-34} {1,6} MB" -f $f.Name, $mb)
}
Write-Host ''
Write-Host ' 控制端： warden-linux-amd64 -mode server              （默认 :8080）'
Write-Host ' 被管端： warden-linux-amd64 -mode agent -agent-listen :8090 -agent-token <令牌>'
Write-Host '============================================================' -ForegroundColor Green
exit 0
