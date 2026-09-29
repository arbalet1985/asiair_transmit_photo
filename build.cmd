@echo off
rem Сборка .exe для Windows в папку dist\
setlocal
cd /d "%~dp0"
if not exist dist mkdir dist
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w" -o dist\asiair-agent.exe .\cmd\asiair-agent || exit /b 1
go build -trimpath -ldflags "-s -w" -o dist\asiair-receiver.exe .\cmd\asiair-receiver || exit /b 1
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist\asiair-sorter.exe .\cmd\asiair-sorter || exit /b 1
copy /y configs\*.example.yaml dist\ >nul
echo Готово: dist\
