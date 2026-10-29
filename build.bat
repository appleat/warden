@echo off
rem ===========================================================================
rem  Warden build entry (Windows)
rem  Real logic lives in build.ps1 - cmd.exe cannot parse UTF-8 Chinese text in
rem  batch files, so this file stays ASCII-only and just forwards the arguments.
rem
rem  Examples:
rem    build.bat                        build every platform (incl. frontend)
rem    build.bat linux                  linux/amd64 + linux/arm64
rem    build.bat linux/arm64            only linux/arm64
rem    build.bat windows -Pack          windows build + zip
rem    build.bat -SkipWeb -Version 1.2.0
rem ===========================================================================
setlocal
for /f "tokens=2 delims=:" %%c in ('chcp') do set "OLDCP=%%c"
set "OLDCP=%OLDCP: =%"
chcp 65001 >nul

set "PS1=%~dp0build.ps1"
if not exist "%PS1%" (
  echo [error] build.ps1 not found next to build.bat
  exit /b 1
)

where pwsh >nul 2>nul
if not errorlevel 1 goto use_pwsh
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" %*
goto done

:use_pwsh
pwsh -NoProfile -ExecutionPolicy Bypass -File "%PS1%" %*

:done
set "RC=%errorlevel%"
if defined OLDCP chcp %OLDCP% >nul
exit /b %RC%
