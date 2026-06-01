@echo off
echo ==============================
echo Build synctool
echo ==============================

if not exist out mkdir out

echo.
echo [1/2] Build server (Linux amd64)...
cd /d "%~dp0server"
set GOOS=linux
set GOARCH=amd64
go build -o "%~dp0out\synctool-server" .
if %errorlevel% NEQ 0 (
    echo Server build FAILED
    pause
    exit /b 1
)
echo   -^> out\synctool-server

echo.
echo [2/2] Build client (Windows amd64)...
cd /d "%~dp0client"
set GOOS=windows
set GOARCH=amd64
go build -o "%~dp0out\synctool-client.exe" .
if %errorlevel% NEQ 0 (
    echo Client build FAILED
    pause
    exit /b 1
)
echo   -^> out\synctool-client.exe

echo.
echo ==============================
echo Build complete!
echo ==============================
echo.
echo out\synctool-server     -- copy to Linux
echo out\synctool-client.exe -- copy to Windows
echo.
pause
