@echo off
REM Memora verify gate (Windows / cmd)
REM Usage: run verify.bat in the project root.
REM Checks:
REM   1. frontend typecheck (vue-tsc)
REM   2. frontend unit tests (vitest, if present)
REM   3. frontend build
REM   4. backend go vet ./...
REM   5. backend go test -count=1 ./...
REM   6. gofmt -l backend drift check (must be empty)
REM Any failure exits with a non-zero code; full pass prints "verify OK".
setlocal
set ROOT=%~dp0
set FAILED=0

echo === [1/6] Frontend typecheck (vue-tsc) ===
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
call npx vue-tsc --noEmit -p tsconfig.app.json
if errorlevel 1 (
    echo [ERROR] frontend typecheck failed
    set FAILED=1
)
popd

echo === [2/6] Frontend unit tests (vitest) ===
pushd "%ROOT%frontend"
findstr /R /C:"\"test\"" package.json >nul 2>&1
if not errorlevel 1 (
    call npm run test
    if errorlevel 1 (
        echo [ERROR] frontend tests failed
        set FAILED=1
    )
) else (
    echo    no test script found, skipping
)
popd

echo === [3/6] Frontend build ===
pushd "%ROOT%frontend"
call npm run build
if errorlevel 1 (
    echo [ERROR] frontend build failed
    set FAILED=1
)
popd

echo === [4/6] go vet ./... ===
pushd "%ROOT%backend"
go vet ./...
if errorlevel 1 (
    echo [ERROR] go vet failed
    set FAILED=1
)
popd

echo === [5/6] go test -count=1 ./... ===
pushd "%ROOT%backend"
go test -count=1 ./...
if errorlevel 1 (
    echo [ERROR] go test failed
    set FAILED=1
)
popd

echo === [6/6] gofmt drift check ===
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