@echo off
REM Memora build script (Windows / cmd)
REM Usage: run build.bat in the project root.
REM Outputs to bin/:
REM   bin\memora.exe         backend executable (frontend dist embedded, self-contained)
REM   bin\memora.exe.sha256  SHA-256 checksum of memora.exe (certutil)
REM   bin\sbom.json          SBOM (Go modules from `go list -m all` + frontend deps from package-lock.json)
REM   bin\sbom-go.json       Go module list only (JSON)
REM   bin\CHANGELOG.txt      commit list since last tag (fallback: last 20 commits)
REM   bin\VERSION            version string (git describe; fallback "dev")
REM
REM Version / commit / build-time are injected into the binary via
REM   go build -ldflags "-X memora/internal/assembler.BuildVersion=... -X ...BuildCommit=... -X ...BuildTime=..."
REM
REM Flow: build frontend dist -> copy into backend\internal\web\dist (for go:embed)
REM       -> go build produces memora.exe -> generate checksum/SBOM/CHANGELOG/VERSION.
setlocal
set ROOT=%~dp0
set BIN_DIR=%ROOT%bin
set EMBED_DIR=%ROOT%backend\internal\web\dist

echo === Preparing output dir ===
if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

REM ---- 0. collect build info from git (ldflags injection source) ----
echo === Collecting build info (git) ===
for /f "delims=" %%i in ('git describe --tags --always --dirty 2^>nul') do set VERSION=%%i
if not defined VERSION set VERSION=dev
for /f "delims=" %%i in ('git rev-parse --short HEAD 2^>nul') do set COMMIT=%%i
if not defined COMMIT set COMMIT=unknown
REM Build time: prefer git commit ISO time (no spaces -> safe for -ldflags); fallback to local time with
REM colons/slashes/spaces sanitized (a raw %TIME% value may contain spaces, which would break -X parsing).
set BT=
git show -s --format=%%cI HEAD > "%BIN_DIR%\.bt.tmp" 2>nul
set /p BT=<"%BIN_DIR%\.bt.tmp"
del /Q "%BIN_DIR%\.bt.tmp" 2>nul
if defined BT goto :bt_ok
set BT=%DATE%_%TIME%
set BT=%BT:/=-%
set BT=%BT::=-%
set BT=%BT: =_%
:bt_ok
echo    VERSION=%VERSION%
echo    COMMIT =%COMMIT%
echo    BT     =%BT%

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

REM ---- 3. build backend (Go) with ldflags injection ----
echo === Building backend memora.exe ===
pushd "%ROOT%backend"
if exist "%BIN_DIR%\memora.exe" del /Q "%BIN_DIR%\memora.exe"
go build -trimpath -ldflags "-X memora/internal/assembler.BuildVersion=%VERSION% -X memora/internal/assembler.BuildCommit=%COMMIT% -X memora/internal/assembler.BuildTime=%BT%" -o "%BIN_DIR%\memora.exe" .\cmd\memora
if errorlevel 1 (
    echo [ERROR] backend build failed
    popd
    exit /b 1
)
echo     OK: %BIN_DIR%\memora.exe
popd

REM ---- 4. metadata: SHA-256 ----
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

REM ---- 5. metadata: SBOM (Go modules + frontend deps) ----
echo === Generating SBOM ===
pushd "%ROOT%backend"
go list -m all > "%BIN_DIR%\.gomods.tmp" 2>nul
popd
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

REM ---- 6. metadata: CHANGELOG ----
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

REM ---- 7. VERSION file ----
> "%BIN_DIR%\VERSION" echo %VERSION%
echo     OK: %BIN_DIR%\VERSION

echo.
echo === Build complete ===
echo   %BIN_DIR%\memora.exe              (self-contained, frontend embedded, version=%VERSION%)
echo   %BIN_DIR%\memora.exe.sha256
echo   %BIN_DIR%\sbom.json
echo   %BIN_DIR%\sbom-go.json
echo   %BIN_DIR%\CHANGELOG.txt
echo   %BIN_DIR%\VERSION
endlocal
exit /b 0
