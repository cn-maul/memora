@echo off
REM Memora build script (Windows / cmd) - Wails v3
REM Usage: run build.bat in the project root.
REM
REM Outputs to bin/:
REM   bin\memora.exe          Wails native executable (Vue 3 embedded, self-contained)
REM   bin\memora.exe.sha256   SHA-256 checksum (certutil)
REM   bin\sbom.json           SBOM (Go modules + frontend deps)
REM   bin\sbom-go.json        Go module list only
REM   bin\CHANGELOG.txt       commit list since last tag
REM   bin\VERSION             version string (git describe; fallback "dev")
REM
REM Build flow:
REM   1. Collect build info from git (version / commit / build time)
REM   2. wails3 build   - go mod tidy, npm install, npm run build,
REM                        wails3 generate bindings, wails3 generate syso,
REM                        go build with ldflags version injection
REM   3. Generate SHA-256 / SBOM / CHANGELOG / VERSION

setlocal
set ROOT=%~dp0
set BIN_DIR=%ROOT%bin

echo === Preparing output dir ===
if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

REM ---- 0. collect build info from git (ldflags injection source) ----
echo === Collecting build info (git) ===
for /f "delims=" %%i in ('git describe --tags --always --dirty 2^>nul') do set VERSION=%%i
if not defined VERSION set VERSION=dev
for /f "delims=" %%i in ('git rev-parse --short HEAD 2^>nul') do set COMMIT=%%i
if not defined COMMIT set COMMIT=unknown
set BT=
git show -s --format=%%cI HEAD > "%BIN_DIR%\.bt.tmp" 2>nul
set /p BT=<"%BIN_DIR%\.bt.tmp"
del /Q "%BIN_DIR%\.bt.tmp" 2>nul
if not defined BT (
    set BT=%DATE%_%TIME%
    set BT=%BT:/=-%
    set BT=%BT::=-%
    set BT=%BT: =_%
)
echo    VERSION=%VERSION%
echo    COMMIT =%COMMIT%
echo    BT     =%BT%

REM ---- 1. Build ldflags injection string ----
set VERSION_LDFLAGS=-X memora/internal/assembler.BuildVersion=%VERSION% -X memora/internal/assembler.BuildCommit=%COMMIT% -X memora/internal/assembler.BuildTime=%BT%

REM ---- 2. wails3 build (handles deps, frontend, bindings, syso, go build) ----
echo === Building with wails3 ===
echo    ldflags: %VERSION_LDFLAGS%
wails3 build VERSION_LDFLAGS="%VERSION_LDFLAGS%"
if errorlevel 1 (
    echo [ERROR] wails3 build failed
    exit /b 1
)
echo     OK: %BIN_DIR%\memora.exe

REM ---- 3. metadata: SHA-256 ----
echo === Generating SHA-256 checksum ===
set HASH=
for /f "skip=1 delims=" %%i in ('certutil -hashfile "%BIN_DIR%\memora.exe" SHA256 2^>nul') do (
    set HASH=%%i
    goto :hash_done
)
:hash_done
if not defined HASH set HASH=unknown
> "%BIN_DIR%\memora.exe.sha256" echo %HASH%  memora.exe
echo     OK: %BIN_DIR%\memora.exe.sha256

REM ---- 4. metadata: SBOM (Go modules + frontend deps) ----
echo === Generating SBOM ===
go list -m all > "%BIN_DIR%\.gomods.tmp" 2>nul
pushd "%ROOT%frontend"
node -e "var fs=require('fs');var t=fs.readFileSync(process.argv[1],'utf8').split(/\r?\n/);var mods=[];for(var i=0;i<t.length;i++){var l=t[i].trim();if(l.length===0){continue;}var p=l.split(/\s+/);var ver=(p[1]||'').replace(/\/\/ indirect.*/,'').trim();if(ver.length===0){continue;}mods.push({name:p[0],version:ver});}var pl=require('./'+process.argv[2]);var pk=pl.packages||{};var front=[];for(var k in pk){var q=pk[k];if(!q||!q.version){continue;}var name=q.name;if(!name){var idx=k.lastIndexOf('node_modules/');name=(idx>=0)?k.slice(idx+13):k;}if(name.length===0){continue;}front.push({name:name,version:q.version});}var sbom={format:'memora-sbom',version:'1.0',generatedAt:process.argv[3],application:{name:'memora',version:process.argv[4],commit:process.argv[5]},components:{go:{source:'go list -m all',count:mods.length,modules:mods},frontend:{source:'package-lock.json',count:front.length,dependencies:front}}};fs.writeFileSync(process.argv[6],JSON.stringify(sbom,null,2));fs.writeFileSync(process.argv[7],JSON.stringify({generatedAt:process.argv[3],modules:mods},null,2));" "%BIN_DIR%\.gomods.tmp" "package-lock.json" "%BT%" "%VERSION%" "%COMMIT%" "%BIN_DIR%\sbom.json" "%BIN_DIR%\sbom-go.json"
if errorlevel 1 (
    echo [WARN] SBOM generation failed
) else (
    echo     OK: %BIN_DIR%\sbom.json  [Go modules + frontend deps]
    echo     OK: %BIN_DIR%\sbom-go.json
)
popd
if exist "%BIN_DIR%\.gomods.tmp" del /Q "%BIN_DIR%\.gomods.tmp"

REM ---- 5. metadata: CHANGELOG ----
echo === Generating CHANGELOG ===
set PREV_TAG=
git describe --tags --abbrev=0 > "%BIN_DIR%\.prev.tmp" 2>nul
set /p PREV_TAG=<"%BIN_DIR%\.prev.tmp"
del /Q "%BIN_DIR%\.prev.tmp" 2>nul
if defined PREV_TAG (
    git log --oneline "%PREV_TAG%..HEAD" > "%BIN_DIR%\CHANGELOG.txt"
) else (
    git log --oneline -20 HEAD > "%BIN_DIR%\CHANGELOG.txt"
)
echo     OK: %BIN_DIR%\CHANGELOG.txt

REM ---- 6. VERSION file ----
> "%BIN_DIR%\VERSION" echo %VERSION%
echo     OK: %BIN_DIR%\VERSION

echo.
echo === Build complete ===
echo   %BIN_DIR%\memora.exe              (Wails native, frontend embedded, version=%VERSION%)
echo   %BIN_DIR%\memora.exe.sha256
echo   %BIN_DIR%\sbom.json
echo   %BIN_DIR%\sbom-go.json
echo   %BIN_DIR%\CHANGELOG.txt
echo   %BIN_DIR%\VERSION
echo.
echo === Launching memora.exe ===
if /i "%RUN%" == "false" (
    echo [SKIP] Set RUN=true to launch automatically
) else (
    start "" "%BIN_DIR%\memora.exe"
)
endlocal
exit /b 0
