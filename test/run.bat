@echo off
REM FeedShit - Build and run from test directory
REM All runtime data (data/, SQLite, uploads) stays in test/

echo [FeedShit Test] Building binary...
cd /d "%~dp0.."
go build -o test\feedshit.exe .\src\
if %ERRORLEVEL% neq 0 (
    echo [FeedShit Test] Build FAILED
    pause
    exit /b 1
)

echo [FeedShit Test] Starting server on :8080 ...
echo [FeedShit Test] User page:   http://localhost:8080/
echo [FeedShit Test] Admin panel: http://localhost:8080/admin
echo [FeedShit Test] Data dir:    %~dp0data\
echo [FeedShit Test] Press Ctrl+C to stop
echo.

cd /d "%~dp0"
feedshit.exe
