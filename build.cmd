@echo off
setlocal
chcp 65001 >nul
cd /d "%~dp0"
echo [INFO] 构建 OpenChamber fnOS x86_64 安装包
pushd frontend
call npm ci --ignore-scripts --no-audit --no-fund
if errorlevel 1 exit /b 1
call node_modules\.bin\vue-tsc.cmd --noEmit
if errorlevel 1 exit /b 1
call npm run build
if errorlevel 1 exit /b 1
popd
if not exist fnpack\app\bin mkdir fnpack\app\bin
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags="-s -w" -o fnpack\app\bin\openchamber .
if errorlevel 1 exit /b 1
pushd fnpack
set "FNPACK_EXE=%TEMP%\openchamber-fnpack-%RANDOM%.exe"
copy /y fnpack-1.2.3-windows-amd64 "%FNPACK_EXE%" >nul
if errorlevel 1 exit /b 1
"%FNPACK_EXE%" build
set "PACK_EXIT=%ERRORLEVEL%"
del /q "%FNPACK_EXE%"
if not "%PACK_EXIT%"=="0" exit /b %PACK_EXIT%
popd
echo [INFO] 已生成 fnpack\openchamber.fpk，运行行为需在 fnOS 手动验收。
