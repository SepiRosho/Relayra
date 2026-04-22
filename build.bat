@echo off
setlocal

set "PROJECT_DIR=%~dp0"
cd /d "%PROJECT_DIR%"

set HTTP_PROXY=http://127.0.0.1:3066
set HTTPS_PROXY=http://127.0.0.1:3066
set GOOS=linux
set GOARCH=amd64

set "DIST=dist\relayra-dev-linux-amd64"
set "ARCHIVE=dist\relayra-dev-linux-amd64.tar.gz"

echo [1/4] Cleaning old build...
if exist "%DIST%" rmdir /s /q "%DIST%"
if exist "%ARCHIVE%" del /q "%ARCHIVE%"

echo [2/4] Compiling for linux/amd64...
mkdir "%DIST%\scripts" 2>nul
go build -o "%DIST%\relayra" ./cmd/relayra
if errorlevel 1 (
    echo BUILD FAILED
    exit /b 1
)

echo [3/4] Copying assets...
copy /y README.md "%DIST%\" >nul
copy /y GUIDE.md "%DIST%\" >nul
copy /y scripts\install.sh "%DIST%\" >nul
copy /y scripts\test-relay.sh "%DIST%\scripts\" >nul
copy /y scripts\test-webhook.sh "%DIST%\scripts\" >nul

echo [4/4] Creating tar.gz...
cd dist
tar czf relayra-dev-linux-amd64.tar.gz relayra-dev-linux-amd64

echo.
echo === Build complete ===
for %%F in (relayra-dev-linux-amd64.tar.gz) do echo Output: dist\%%F (%%~zF bytes)
echo.
