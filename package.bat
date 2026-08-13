@echo off
REM Memora package script (Windows / cmd) — Wails v3 NSIS installer
REM Usage:
REM   package.bat            Build + create NSIS installer (default: machine-wide)
REM   package.bat USER       Build + create per-user installer (no UAC)
REM   package.bat INSTALLER  Build + launch installer immediately
REM   package.bat NOBUILD    Skip build, just package existing bin/memora.exe
REM
REM Outputs to bin/:
REM   bin\memora-installer.exe    NSIS installer
REM   bin\memora.exe              (prerequisite)

setlocal
set ROOT=%~dp0
set BIN_DIR=%ROOT%bin
set MODE=%1

echo ============================================
echo   Memora Package - NSIS Installer Builder
echo ============================================
echo   MODE = %MODE%

REM ---- 0. Ensure bin dir exists ----
if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

REM ---- 1. Build the app (unless NOBUILD) ----
if /i "%MODE%" == "NOBUILD" goto :skip_build
if not exist "%BIN_DIR%\memora.exe" (
    echo === Building memora.exe first ===
    set RUN=false
    call "%ROOT%build.bat"
    if errorlevel 1 (
        echo [ERROR] build failed
        exit /b 1
    )
) else (
    echo === memora.exe exists, using existing build ===
)
:skip_build

REM ---- 2. Install NSIS if not present ----
echo === Checking NSIS (makensis) ===
where makensis >nul 2>&1
if errorlevel 1 (
    echo    makensis not found, downloading NSIS...
    if not exist "%ROOT%\nsis-setup.exe" (
        echo    Downloading from github.com/wailsapp/nsis ...
        powershell -Command "& { try { Invoke-WebRequest -Uri 'https://github.com/wailsapp/nsis/releases/latest/download/NSIS-Setup.exe' -OutFile '%ROOT%\nsis-setup.exe' -UseBasicParsing } catch { echo 'Download failed, trying alternative...'; Invoke-WebRequest -Uri 'https://github.com/wailsapp/nsis/releases/latest/download/NSIS-Setup.exe' -OutFile '%ROOT%\nsis-setup.exe' -UseBasicParsing } }" 2>nul
        if not exist "%ROOT%\nsis-setup.exe" (
            echo    Download failed. Please install NSIS manually:
            echo      https://nsis.sourceforge.io/Download
            echo      Then run: package.bat NOBUILD
            exit /b 1
        )
    )
    echo    Installing NSIS...
    start /wait "%ROOT%\nsis-setup.exe" /S
    if errorlevel 1 (
        echo [WARN] NSIS installer exited with code 1
    )
    del /Q "%ROOT%\nsis-setup.exe" 2>nul
    where makensis >nul 2>&1
    if errorlevel 1 (
        echo [ERROR] NSIS still not found after install
        exit /b 1
    )
)
echo     OK: makensis found

REM ---- 3. Determine install scope ----
if /i "%MODE%" == "USER" (
    set INSTALL_SCOPE=user
) else (
    set INSTALL_SCOPE=machine
)
echo    Install scope: %INSTALL_SCOPE%

REM ---- 4. Run wails3 package ----
echo === Creating NSIS installer ===
wails3 package FORMAT=nsis INSTALL_SCOPE=%INSTALL_SCOPE%
if errorlevel 1 (
    echo [ERROR] wails3 package failed
    exit /b 1
)
echo     OK: %BIN_DIR%\memora-installer.exe

REM ---- 5. Verify output ----
if not exist "%BIN_DIR%\memora-installer.exe" (
    echo [WARN] Installer not found in bin, checking nsis dir...
    if exist "%ROOT%\build\windows\nsis\memora-installer.exe" (
        copy /Y "%ROOT%\build\windows\nsis\memora-installer.exe" "%BIN_DIR%\memora-installer.exe"
    )
)

echo.
echo ============================================
echo   Package complete!
echo     %BIN_DIR%\memora-installer.exe
echo   Install scope: %INSTALL_SCOPE%
echo ============================================

REM ---- 6. Launch installer if MODE=INSTALLER ----
if /i "%MODE%" == "INSTALLER" (
    echo.
    echo === Launching installer ===
    start "" "%BIN_DIR%\memora-installer.exe"
)

endlocal
exit /b 0
