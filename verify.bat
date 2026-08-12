@echo off
REM Memora verify gate (Windows / cmd)
REM Usage: run verify.bat in the project root.
REM Checks:
REM   1. frontend typecheck + build (npm run build)
REM   2. backend go vet ./...
REM   3. backend go test -count=1 ./...
REM   4. gofmt -l backend drift check (must be empty)
REM Any failure exits with a non-zero code; full pass prints "verify OK".
setlocal
set ROOT=%~dp0
set FAILED=0

echo === [1/4] Frontend typecheck + build ===
pushd "%ROOT%frontend"
if not exist "node_modules" (
    echo    node_modules missing, running npm install ...
    call npm install
    if errorlevel 1 (
        echo [ERROR] npm install failed
        popd
        exit /b 1
    )
)
call npm run build
if errorlevel 1 (
    echo [ERROR] frontend typecheck/build failed
    set FAILED=1
)
popd

echo === [2/4] go vet ./... ===
pushd "%ROOT%backend"
go vet ./...
if errorlevel 1 (
    echo [ERROR] go vet failed
    set FAILED=1
)
popd

echo === [3/4] go test -count=1 ./... ===
pushd "%ROOT%backend"
go test -count=1 ./...
if errorlevel 1 (
    echo [ERROR] go test failed
    set FAILED=1
)
popd

echo === [4/4] gofmt drift check ===
set DRIFT_LIST=%TEMP%\memora_gofmt_drift.txt
gofmt -l "%ROOT%backend" > "%DRIFT_LIST%" 2>&1
if errorlevel 1 (
    echo [ERROR] gofmt failed to run
    set FAILED=1
)
set DRIFT=0
for /f "usebackq delims=" %%L in ("%DRIFT_LIST%") do (
    set DRIFT=1
    echo [DRIFT] %%L
)
del /Q "%DRIFT_LIST%" 2>nul
if not "%DRIFT%"=="0" (
    echo [ERROR] gofmt drift found - run gofmt -w on the files above
    set FAILED=1
)
if "%FAILED%"=="0" echo     OK: no gofmt drift

echo.
if not "%FAILED%"=="0" (
    echo verify FAILED
    endlocal
    exit /b 1
)
echo verify OK
endlocal
exit /b 0