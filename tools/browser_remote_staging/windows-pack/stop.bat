@echo off
taskkill /IM ragflow-browser-gateway.exe /F >nul 2>&1
if %errorlevel%==0 (
  echo Gateway stopped.
) else (
  echo Gateway is not running.
)
pause
