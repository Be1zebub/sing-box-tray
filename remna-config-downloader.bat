@echo off
setlocal

rem Downloads a sing-box config from a Remnawave subscription URL.
rem
rem Sends User-Agent "singbox" so the panel's "Sing-box clients" subscription
rem request rule matches (user-agent REGEX ^sfa|sfi|sfm|sft|karing|singbox,
rem case-insensitive) and the response is the generated sing-box JSON instead
rem of the default base64 link list.
rem
rem NOTE: the UA must contain "singbox" literally. "sing-box/1.14.1" would NOT
rem match - that alternative has no hyphen in it.
rem
rem Usage:
rem   remna-config-downloader.bat
rem   remna-config-downloader.bat <subscription-url> [output-path]
rem
rem Default output is config.json next to this script.

set "UA=singbox"

set "URL=%~1"
if not defined URL set /p "URL=Subscription URL (e.g. https://foo.bar/api/sub/XXXXXXX): "
if not defined URL (
  echo.
  echo No URL given.
  goto :end
)
rem strip spaces that sneak in when pasting
set "URL=%URL: =%"

set "OUT=%~2"
if not defined OUT set "OUT=%~dp0config.json"

echo.
echo   GET  %URL%
echo   UA   %UA%
echo   -^>   %OUT%
echo.

curl.exe -fsSL --retry 2 --connect-timeout 15 -A "%UA%" -o "%OUT%" "%URL%"
if errorlevel 1 (
  echo.
  echo Download failed ^(curl exit %ERRORLEVEL%^).
  echo Check the URL, or that curl.exe exists in System32.
  goto :end
)

rem A sing-box config is JSON and contains an "inbounds" key. If the request
rem rule did not match, Remnawave answers with a base64 link list instead.
findstr /c:"inbounds" "%OUT%" >nul 2>&1
if errorlevel 1 (
  echo WARNING: response does not look like a sing-box JSON config.
  echo The panel probably did not match the Sing-box request rule and returned
  echo a base64 link list. Check the rule's user-agent condition, and that a
  echo SUBSCRIPTION template of type SINGBOX exists in the panel.
  goto :end
)

for %%A in ("%OUT%") do echo Saved %%~zA bytes - looks like a sing-box config.
echo.
echo Validate it with:
echo   sing-box.exe check -c "%OUT%"

:end
echo.
pause
endlocal
