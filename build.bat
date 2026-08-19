@echo off
setlocal

pushd "%~dp0"

echo [1/3] Building Vue admin UI...
pushd web
call npm install --no-audit --no-fund
if errorlevel 1 (
    echo npm install failed
    popd
    exit /b 1
)
call npm run build
if errorlevel 1 (
    echo npm run build failed
    popd
    exit /b 1
)
popd

echo [2/3] Formatting and testing Go backend...
go fmt ./...
if errorlevel 1 exit /b 1
go test ./...
if errorlevel 1 exit /b 1

echo [3/3] Building ocr-review-bot binary...
if not exist build mkdir build
go build -o build\ocr-review-bot.exe .\cmd\ocr-review-bot
if errorlevel 1 exit /b 1

echo.
echo Build complete.
echo Binary: build\ocr-review-bot.exe
echo Config: build\config.json
echo.
echo Run: build\ocr-review-bot.exe
echo Or:   set OCR_BOT_CONFIG=build\config.json ^&^& build\ocr-review-bot.exe

popd
endlocal
