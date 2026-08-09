@echo off
REM Memora build script (Windows / cmd)
REM Usage: run build.bat in the project root.
REM Outputs to bin/:
REM   bin\memora.exe   backend executable (frontend dist embedded, self-contained)
REM
REM Flow: build frontend dist -> copy into backend\internal\web\dist (for go:embed)
REM       -> go build produces memora.exe.
setlocal
set ROOT=%~dp0
set BIN_DIR=%ROOT%bin
set EMBED_DIR=%ROOT%backend\internal\web\dist

echo === Preparing output dir ===
if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

REM ---- 1. build frontend (Vue 3) ----
echo === Building frontend ===
pushd "%ROOT%frontend"
if not exist "node_modules" (
    echo    npm install first ...
    call npm install
    if errorlevel 1 (
        echo [ERROR] npm install failed
        popd
        exit /b 1
    )
)
call npm run build
if errorlevel 1 (
    echo [ERROR] frontend build failed
    popd
    exit /b 1
)
popd

REM ---- 2. copy frontend dist into backend embed dir ----
echo === Copying dist to backend\internal\web\dist ===
if exist "%EMBED_DIR%" rmdir /S /Q "%EMBED_DIR%"
mkdir "%EMBED_DIR%"
xcopy /E /I /H /Y "%ROOT%frontend\dist\*" "%EMBED_DIR%" >nul
if errorlevel 1 (
    echo [ERROR] copy dist to embed dir failed
    exit /b 1
)
echo     OK: %EMBED_DIR%

REM ---- 3. build backend (Go) ----
echo === Building backend memora.exe ===
pushd "%ROOT%backend"
if exist "%BIN_DIR%\memora.exe" del /Q "%BIN_DIR%\memora.exe"
go build -trimpath -o "%BIN_DIR%\memora.exe" .\cmd\memora
if errorlevel 1 (
    echo [ERROR] backend build failed
    popd
    exit /b 1
)
echo     OK: %BIN_DIR%\memora.exe
popd

echo.
echo === Build complete ===
echo   %BIN_DIR%\memora.exe  (self-contained, frontend embedded)
endlocal
exit /b 0
