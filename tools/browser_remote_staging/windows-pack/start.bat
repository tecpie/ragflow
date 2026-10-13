@echo off
setlocal EnableExtensions EnableDelayedExpansion

cd /d "%~dp0"

if exist config.env (
    for /f "usebackq tokens=1,* delims==" %%A in ("config.env") do (
        set "_line=%%A"
        if not "!_line!"=="" if not "!_line:~0,1!"=="#" (
            set "%%A"
        )
    )
)

if "%BROWSER_GATEWAY_PORT%"=="" set BROWSER_GATEWAY_PORT=19080
if "%BROWSER_CDP_UPSTREAM%"=="" set BROWSER_CDP_UPSTREAM=http://127.0.0.1:9222
if "%BROWSER_STAGING_DIR%"=="" set BROWSER_STAGING_DIR=%ProgramData%\ragflow\browser-uploads
if "%BROWSER_STAGING_TOKEN%"=="" set BROWSER_STAGING_TOKEN=change-me

if not exist "%BROWSER_STAGING_DIR%" mkdir "%BROWSER_STAGING_DIR%"

echo ============================================================
echo  RAGFlow Browser Gateway
echo  Port         : %BROWSER_GATEWAY_PORT%
echo  CDP upstream : %BROWSER_CDP_UPSTREAM%
echo  Staging dir  : %BROWSER_STAGING_DIR%
echo  Token        : configured
echo.
echo  RAGFlow Browser node:
echo    CDP URL            = http://^<this-pc-ip^>:%BROWSER_GATEWAY_PORT%
echo    Remote staging URL = http://^<this-pc-ip^>:%BROWSER_GATEWAY_PORT%
echo ============================================================
echo.

start "RAGFlow Browser Gateway" /MIN "%~dp0ragflow-browser-gateway.exe"
timeout /t 2 /nobreak >nul

echo Health check:
curl -s http://127.0.0.1:%BROWSER_GATEWAY_PORT%/health
echo.
echo.
echo Gateway started in background window "RAGFlow Browser Gateway".
echo Edit config.env and run start.bat again to apply changes.
echo.
pause
