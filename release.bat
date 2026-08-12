@echo off
REM Memora release script (Windows / cmd) - tag driven
REM Usage:  release.bat <tag>        e.g.  release.bat v0.1.0
REM
REM Flow:
REM   1. validate the tag exists (git rev-parse)
REM   2. check working tree; force `git checkout <tag>` (uncommitted changes are discarded - back up first!)
REM   3. npm ci (strict lockfile) -> frontend build -> copy dist into backend embed dir
REM   4. go build with ldflags injecting version=<tag>, commit, build-time
REM   5. generate metadata: SHA-256 / SBOM / CHANGELOG / VERSION
REM
REM Artifacts are written to:
REM   dist-release\memora-v<tag>\
REM     memora.exe            self-contained binary (frontend embedded)
REM     memora.exe.sha256     SHA-256 checksum (certutil)
REM     sbom.json             SBOM: Go modules (`go list -m all`) + frontend deps (package-lock.json)
REM     sbom-go.json          Go module list only
REM     CHANGELOG.txt         commits since the previous tag (fallback: all commits of this tag)
REM     VERSION               the release version string
REM
REM Code signing strategy:
REM   Release binaries are currently UNSIGNED by default.
REM   Optional enhancement (future): sign with Authenticode on Windows, e.g.
REM     signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /a "dist-release\memora-v<tag>\memora.exe"
REM   Signing requires a code-signing certificate (EV/OV) and the Windows SDK signtool.exe;
REM   it is deliberately kept out of this script so local/dev builds stay unsigned.
setlocal
set ROOT=%~dp0

REM ---- 0. args ----
if "%~1"=="" (
    echo [ERROR] usage: release.bat ^<tag^>    e.g. release.bat v0.1.0
    exit /b 1
)
set TAG=%~1

REM normalize: dist dir is always "memora-v<ver>" (drop a leading "v" from the tag if present)
set BASE_TAG=%TAG%
if not "%BASE_TAG:~0,1%"=="v" set BASE_TAG=v%TAG%
set OUT_DIR=%ROOT%dist-release\memora-%BASE_TAG%
set EMBED_DIR=%ROOT%backend\internal\web\dist

REM ---- 1. validate tag ----
echo === Validating tag "%TAG%" ===
git rev-parse "%TAG%" >nul 2>nul
if errorlevel 1 (
    echo [ERROR] tag "%TAG%" does not exist
    exit /b 1
)
echo     OK: tag "%TAG%" exists

REM ---- 2. check working tree, then force checkout ----
echo === Checking working tree ===
set DIRTY=
for /f "delims=" %%i in ('git status --porcelain 2^>nul') do set DIRTY=%%i
if defined DIRTY (
    echo [WARN] working tree has uncommitted changes - they WILL BE DISCARDED by the forced checkout.
    echo        back up or commit them first if needed. Proceeding anyway...
    git status --porcelain
)
echo === Forced checkout of "%TAG%" ===
git checkout --force "%TAG%"
if errorlevel 1 (
    echo [ERROR] git checkout "%TAG%" failed
    exit /b 1
)
echo     OK: checked out %TAG%

REM ---- 3. prepare output dir ----
if exist "%OUT_DIR%" rmdir /S /Q "%OUT_DIR%"
mkdir "%OUT_DIR%"
if errorlevel 1 (
    echo [ERROR] cannot create %OUT_DIR%
    exit /b 1
)

REM ---- 4. build info from git (after checkout, HEAD == tag) ----
set COMMIT=
for /f "delims=" %%i in ('git rev-parse --short HEAD 2^>nul') do set COMMIT=%%i
if not defined COMMIT set COMMIT=unknown
set BT=
git show -s --format=%%cI HEAD > "%TEMP%\memora-bt.tmp" 2>nul
set /p BT=<"%TEMP%\memora-bt.tmp"
del /Q "%TEMP%\memora-bt.tmp" 2>nul
if defined BT goto :bt_ok
set BT=%DATE%_%TIME%
set BT=%BT:/=-%
set BT=%BT::=-%
set BT=%BT: =_%
:bt_ok
echo     VERSION=%BASE_TAG%  COMMIT=%COMMIT%  BT=%BT%

REM ---- 5. frontend: npm ci (strict lockfile) + build ----
echo === npm ci (strict lockfile) ===
pushd "%ROOT%frontend"
call npm ci
if errorlevel 1 (
    echo [ERROR] npm ci failed - package-lock.json out of sync?
    popd
    exit /b 1
)
echo === Building frontend ===
call npm run build
if errorlevel 1 (
    echo [ERROR] frontend build failed
    popd
    exit /b 1
)
popd

REM ---- 6. copy dist into backend embed dir ----
echo === Copying dist to backend\internal\web\dist ===
if exist "%EMBED_DIR%" rmdir /S /Q "%EMBED_DIR%"
mkdir "%EMBED_DIR%"
xcopy /E /I /H /Y "%ROOT%frontend\dist\*" "%EMBED_DIR%" >nul
if errorlevel 1 (
    echo [ERROR] copy dist to embed dir failed
    exit /b 1
)
echo     OK: %EMBED_DIR%

REM ---- 7. backend: go build with ldflags ----
echo === Building backend memora.exe ===
pushd "%ROOT%backend"
go build -trimpath -ldflags "-X memora/internal/assembler.BuildVersion=%BASE_TAG% -X memora/internal/assembler.BuildCommit=%COMMIT% -X memora/internal/assembler.BuildTime=%BT%" -o "%OUT_DIR%\memora.exe" .\cmd\memora
if errorlevel 1 (
    echo [ERROR] backend build failed
    popd
    exit /b 1
)
echo     OK: %OUT_DIR%\memora.exe
popd

REM ---- 8. metadata: SHA-256 ----
echo === Generating SHA-256 checksum ===
set HASH=
for /f "skip=1 delims=" %%i in ('certutil -hashfile "%OUT_DIR%\memora.exe" SHA256 2^>nul') do (
    set HASH=%%i
    goto :hash_done
)
:hash_done
if not defined HASH set HASH=unknown
> "%OUT_DIR%\memora.exe.sha256" echo %HASH%  memora.exe
echo     OK: %OUT_DIR%\memora.exe.sha256

REM ---- 9. metadata: SBOM ----
echo === Generating SBOM ===
pushd "%ROOT%backend"
go list -m all > "%OUT_DIR%\.gomods.tmp" 2>nul
popd
pushd "%ROOT%frontend"
node -e "var fs=require('fs');var t=fs.readFileSync(process.argv[1],'utf8').split(/\r?\n/);var mods=[];for(var i=0;i<t.length;i++){var l=t[i].trim();if(l.length===0){continue;}var p=l.split(/\s+/);var ver=(p[1]||'').replace(/\/\/ indirect.*/,'').trim();if(ver.length===0){continue;}mods.push({name:p[0],version:ver});}var pl=require('./'+process.argv[2]);var pk=pl.packages||{};var front=[];for(var k in pk){var q=pk[k];if(!q||!q.version){continue;}var name=q.name;if(!name){var idx=k.lastIndexOf('node_modules/');name=(idx>=0)?k.slice(idx+13):k;}if(name.length===0){continue;}front.push({name:name,version:q.version});}var sbom={format:'memora-sbom',version:'1.0',generatedAt:process.argv[3],application:{name:'memora',version:process.argv[4],commit:process.argv[5]},components:{go:{source:'go list -m all',count:mods.length,modules:mods},frontend:{source:'package-lock.json',count:front.length,dependencies:front}}};fs.writeFileSync(process.argv[6],JSON.stringify(sbom,null,2));fs.writeFileSync(process.argv[7],JSON.stringify({generatedAt:process.argv[3],modules:mods},null,2));" "%OUT_DIR%\.gomods.tmp" "package-lock.json" "%BT%" "%BASE_TAG%" "%COMMIT%" "%OUT_DIR%\sbom.json" "%OUT_DIR%\sbom-go.json"
if errorlevel 1 (
    echo [WARN] SBOM generation failed
) else (
    echo     OK: %OUT_DIR%\sbom.json
    echo     OK: %OUT_DIR%\sbom-go.json
)
popd
if exist "%OUT_DIR%\.gomods.tmp" del /Q "%OUT_DIR%\.gomods.tmp"

REM ---- 10. metadata: CHANGELOG (since previous tag) ----
echo === Generating CHANGELOG ===
set PREV_TAG=
git describe --tags --abbrev=0 "%TAG%~1" > "%TEMP%\memora-prev.tmp" 2>nul
set /p PREV_TAG=<"%TEMP%\memora-prev.tmp"
del /Q "%TEMP%\memora-prev.tmp" 2>nul
if defined PREV_TAG (
    echo     commits since %PREV_TAG%
    git log --oneline "%PREV_TAG%..%TAG%" > "%OUT_DIR%\CHANGELOG.txt"
) else (
    echo     no previous tag found - listing all commits of %TAG%
    git log --oneline "%TAG%" > "%OUT_DIR%\CHANGELOG.txt"
)
echo     OK: %OUT_DIR%\CHANGELOG.txt

REM ---- 11. VERSION file ----
> "%OUT_DIR%\VERSION" echo %BASE_TAG%
echo     OK: %OUT_DIR%\VERSION

REM ---- 12. summary ----
echo.
echo === Release complete: %BASE_TAG% ===
echo   %OUT_DIR%\memora.exe
echo   %OUT_DIR%\memora.exe.sha256
echo   %OUT_DIR%\sbom.json
echo   %OUT_DIR%\sbom-go.json
echo   %OUT_DIR%\CHANGELOG.txt
echo   %OUT_DIR%\VERSION
echo.
echo NOTE: binary is unsigned. For Authenticode signing see the header comment of this script.
endlocal
exit /b 0
