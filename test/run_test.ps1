<#
.SYNOPSIS
    FeedShit automated E2E test — builds, starts server, runs checks, reports results.
.NOTES
    Run from the test/ directory. Server starts on port 18088 to avoid conflicts.
#>
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Net.Http
Add-Type -AssemblyName System.Security
$port = 18088
$base = "http://localhost:$port"
$counters = @{ passed = 0; failed = 0 }

function Assert($name, $condition, $detail) {
    if ($condition) {
        Write-Host "  PASS  $name" -ForegroundColor Green
        $counters.passed++
    } else {
        Write-Host "  FAIL  $name — $detail" -ForegroundColor Red
        $counters.failed++
    }
}

# ---- Pre-test cleanup ----
Get-Process -Name "feedshit*" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
if (Test-Path "$PSScriptRoot\data") { Remove-Item -Recurse -Force "$PSScriptRoot\data" }

# ---- Build ----
Write-Host "`n[1/11] Building binary..." -ForegroundColor Cyan
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
go build -o "test\feedshit.exe" ".\cmd\feedshit\" 2>&1
Assert "Build succeeds" ($LASTEXITCODE -eq 0) "go build failed"

# ---- Start server ----
Write-Host "`n[2/11] Starting server on port $port..." -ForegroundColor Cyan
$env:PORT = "$port"
$env:DATA_DIR = "$PSScriptRoot\data"
$env:ADMIN_USERNAME = "admin"
$env:ADMIN_PASSWORD = "TestPass123"

$exePath = "$PSScriptRoot\feedshit.exe"
$proc = Start-Process -FilePath $exePath -WorkingDirectory $PSScriptRoot -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 3

if ($proc.HasExited) {
    Write-Host "  FAIL  Server exited immediately" -ForegroundColor Red
    exit 1
}

$http = $null
try {
    $handler = New-Object System.Net.Http.HttpClientHandler
    $handler.AllowAutoRedirect = $false
    $http = New-Object System.Net.Http.HttpClient($handler)

    # ==== [3/11] Pre-setup protection (whitelist-based) ====
    Write-Host "`n[3/11] Testing pre-setup protection (whitelist)..." -ForegroundColor Cyan

    # Whitelisted routes — should work before setup
    $resp = $http.GetAsync("$base/health").Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "GET /health returns 200 (whitelisted)" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = $http.GetAsync("$base/setup").Result
    Assert "GET /setup returns 200 (whitelisted)" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = $http.GetAsync("$base/api/v1/setup/status").Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Setup not complete initially" $body.Contains('"setup_complete":false') "should be false"
    Assert "Setup status includes pow_difficulty" $body.Contains("pow_difficulty") "field missing"

    # /fb/:slug is whitelisted — but no projects exist yet, so 404 (not redirect to /setup)
    $resp = $http.GetAsync("$base/fb/nonexistent").Result
    Assert "GET /fb/nonexistent returns 404 (not setup redirect)" ([int]$resp.StatusCode -eq 404) "got $([int]$resp.StatusCode)"

    # Non-whitelisted routes — should be blocked
    $resp = $http.GetAsync("$base/admin").Result
    Assert "GET /admin redirects to /setup" ([int]$resp.StatusCode -eq 302) "got $([int]$resp.StatusCode)"

    $resp = $http.GetAsync("$base/feedback").Result
    Assert "GET /feedback redirects to /setup (not whitelisted)" ([int]$resp.StatusCode -eq 302) "got $([int]$resp.StatusCode)"

    $loginContent = New-Object System.Net.Http.StringContent(
        '{"username":"admin","password":"TestPass123"}',
        [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http.PostAsync("$base/api/v1/admin/login", $loginContent).Result
    Assert "Login blocked before setup (503)" ([int]$resp.StatusCode -eq 503) "got $([int]$resp.StatusCode)"

    $resp = $http.GetAsync("$base/api/v1/projects").Result
    Assert "Projects API blocked before setup (503)" ([int]$resp.StatusCode -eq 503) "got $([int]$resp.StatusCode)"

    # ==== [4/11] Setup flow ====
    Write-Host "`n[4/11] Testing setup flow..." -ForegroundColor Cyan

    # Weak password should be rejected
    $weakSetup = New-Object System.Net.Http.StringContent(
        '{"admin_username":"testadmin","admin_password":"weak"}',
        [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http.PostAsync("$base/api/v1/setup", $weakSetup).Result
    Assert "Weak password rejected (400)" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    $setupContent = New-Object System.Net.Http.StringContent(
        '{"admin_username":"testadmin","admin_password":"Setup123"}',
        [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http.PostAsync("$base/api/v1/setup", $setupContent).Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "POST /api/v1/setup returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"

    $setupContent2 = New-Object System.Net.Http.StringContent(
        '{"admin_username":"hacker","password":"Evil1234"}',
        [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http.PostAsync("$base/api/v1/setup", $setupContent2).Result
    Assert "Setup re-run blocked (403)" ([int]$resp.StatusCode -eq 403) "got $([int]$resp.StatusCode)"

    # ==== [5/11] Auth + Admin API ====
    Write-Host "`n[5/11] Testing auth + admin API..." -ForegroundColor Cyan

    $loginContent = New-Object System.Net.Http.StringContent(
        '{"username":"testadmin","password":"Setup123"}',
        [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http.PostAsync("$base/api/v1/admin/login", $loginContent).Result
    Assert "Login returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $cookie = ""
    $csrfToken = ""
    foreach ($h in $resp.Headers) {
        if ($h.Key -eq "Set-Cookie") {
            foreach ($v in $h.Value) {
                if ($v -match "admin_session=([^;]+)") { $cookie = $Matches[1] }
                if ($v -match "csrf_token=([^;]+)") { $csrfToken = $Matches[1] }
            }
        }
    }
    Assert "Session cookie set" ($cookie.Length -gt 0) "no cookie"
    Assert "CSRF token set" ($csrfToken.Length -gt 0) "no csrf token"

    function AuthGet($path) {
        $req = New-Object System.Net.Http.HttpRequestMessage([System.Net.Http.HttpMethod]::Get, "$base$path")
        $req.Headers.Add("Cookie", "admin_session=$cookie")
        return $http.SendAsync($req).Result
    }
    function AuthReq($method, $path, $body) {
        $req = New-Object System.Net.Http.HttpRequestMessage($method, "$base$path")
        $req.Headers.Add("Cookie", "admin_session=$cookie; csrf_token=$csrfToken")
        $req.Headers.Add("X-CSRF-Token", $csrfToken)
        if ($body) {
            $req.Content = New-Object System.Net.Http.StringContent($body, [System.Text.Encoding]::UTF8, "application/json")
        }
        return $http.SendAsync($req).Result
    }

    $resp = AuthGet "/api/v1/admin/stats"
    Assert "GET /api/v1/admin/stats returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/config/email"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "GET /config/email returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Email config has smtp_host" $body.Contains("smtp_host") "field missing"

    $resp = AuthGet "/api/v1/admin/config/account"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "GET /config/account returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Account config has username" $body.Contains("testadmin") "username missing"

    $resp = AuthGet "/api/v1/admin/config/system"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "GET /config/system returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "System config has base_url" $body.Contains("base_url") "field missing"

    $noAuthHandler = New-Object System.Net.Http.HttpClientHandler
    $noAuthHandler.AllowAutoRedirect = $false
    $noAuthHandler.UseCookies = $false
    $noAuthHttp = New-Object System.Net.Http.HttpClient($noAuthHandler)
    $resp = $noAuthHttp.GetAsync("$base/api/v1/admin/stats").Result
    Assert "Unauthed stats returns 401" ([int]$resp.StatusCode -eq 401) "got $([int]$resp.StatusCode)"
    $noAuthHttp.Dispose()

    # ==== [6/11] Project management + /fb/:slug pages ====
    Write-Host "`n[6/11] Testing project management + feedback pages..." -ForegroundColor Cyan

    $resp = AuthReq "POST" "/api/v1/admin/projects" '{"name":"Test App","slug":"test-app","description":"A test project"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create project returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"

    $resp = AuthReq "POST" "/api/v1/admin/projects" '{"name":"Website","slug":"website","description":""}'
    Assert "Create second project" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "POST" "/api/v1/admin/projects" '{"name":"Duplicate","slug":"test-app","description":""}'
    Assert "Duplicate slug returns 409" ([int]$resp.StatusCode -eq 409) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/projects"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "List projects returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Projects list contains test-app" $body.Contains("test-app") "slug missing"
    Assert "Project has feedback_count field" $body.Contains("feedback_count") "field missing"

    $resp = AuthReq "PUT" "/api/v1/admin/projects/1" '{"name":"Test App","slug":"test-app","description":"Updated","is_active":false}'
    Assert "Disable project returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = $http.GetAsync("$base/api/v1/projects").Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Public projects returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Disabled project hidden from public" (-not $body.Contains('"slug":"test-app"')) "should be hidden"

    $resp = AuthReq "PUT" "/api/v1/admin/projects/1" '{"name":"Test App","slug":"test-app","description":"Updated","is_active":true}'
    Assert "Re-enable project" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Test /fb/:slug per-project feedback pages
    $resp = $http.GetAsync("$base/fb/test-app").Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "GET /fb/test-app returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Feedback page has project name" $body.Contains("Test App") "project name missing"
    Assert "Feedback page has injected PROJECT data" $body.Contains('"slug":"test-app"') "project data not injected"
    Assert "Feedback page has contact fields" $body.Contains("contact_name") "contact field missing"

    # Disabled project feedback page should return 404
    $resp = AuthReq "PUT" "/api/v1/admin/projects/1" '{"name":"Test App","slug":"test-app","description":"Updated","is_active":false}'
    $resp = $http.GetAsync("$base/fb/test-app").Result
    Assert "Disabled project /fb/ returns 404" ([int]$resp.StatusCode -eq 404) "got $([int]$resp.StatusCode)"

    # Re-enable for feedback submission tests
    $resp = AuthReq "PUT" "/api/v1/admin/projects/1" '{"name":"Test App","slug":"test-app","description":"Updated","is_active":true}'

    # Legacy /feedback should redirect to /fb/default
    $resp = $http.GetAsync("$base/feedback").Result
    Assert "GET /feedback redirects (302)" ([int]$resp.StatusCode -eq 302) "got $([int]$resp.StatusCode)"

    # ==== [7/11] Feedback submission + export ====
    Write-Host "`n[7/11] Testing feedback submission + export..." -ForegroundColor Cyan

    $resp = $http.GetAsync("$base/api/v1/setup/status").Result
    $statusBody = $resp.Content.ReadAsStringAsync().Result
    $powDifficulty = 4
    if ($statusBody -match '"pow_difficulty":(\d+)') { $powDifficulty = [int]$Matches[1] }

    $projectId = "test-app"
    $ts = [string]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())
    $sha = [System.Security.Cryptography.SHA256]::Create()
    $nonce = 0
    $prefix = "0" * $powDifficulty
    $baseStr = "$projectId$ts"
    while ($true) {
        $payload = "$baseStr$nonce"
        $hash = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($payload))
        $hex = [BitConverter]::ToString($hash).Replace("-","").ToLower()
        if ($hex.StartsWith($prefix)) { break }
        $nonce++
        if ($nonce -gt 500000) { break }
    }
    Assert "PoW computed (nonce=$nonce)" ($nonce -lt 500000) "nonce exceeded"

    $multipart = New-Object System.Net.Http.MultipartFormDataContent
    $multipart.Add((New-Object System.Net.Http.StringContent($projectId)), "project_id")
    $multipart.Add((New-Object System.Net.Http.StringContent("E2E Test Issue")), "title")
    $multipart.Add((New-Object System.Net.Http.StringContent("Test description")), "description")
    $multipart.Add((New-Object System.Net.Http.StringContent("TestUser")), "contact_name")
    $multipart.Add((New-Object System.Net.Http.StringContent("test@example.com")), "contact_email")

    $req = New-Object System.Net.Http.HttpRequestMessage(
        [System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $req.Headers.Add("X-PoW-Timestamp", "$ts")
    $req.Headers.Add("X-PoW-Nonce", "$nonce")
    $req.Content = $multipart

    $resp = $http.SendAsync($req).Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Submit with PoW returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"

    # Extract tracking token for later tests
    $submitData = $body | ConvertFrom-Json
    $trackingToken = $submitData.tracking_token
    Assert "Submit returns tracking_token" ($trackingToken.Length -gt 0) "token missing"

    # Verify contact info stored
    $resp = AuthGet "/api/v1/admin/feedbacks/1"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback has contact_name" $body.Contains("TestUser") "contact_name missing"
    Assert "Feedback has contact_email" $body.Contains("test@example.com") "contact_email missing"

    $resp = AuthGet "/api/v1/admin/feedbacks/export"
    Assert "CSV export returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/feedbacks/export?project=test-app"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "CSV export contains feedback" $body.Contains("E2E Test Issue") "content missing"
    Assert "CSV export has contact columns" $body.Contains("联系人") "contact column missing"

    $resp = AuthGet "/api/v1/admin/project-stats"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Project stats returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Project stats has test-app" $body.Contains("test-app") "project missing"
    Assert "Project stats has feedback count" ($body -match '"count":\s*1') "count should be 1"

    # File type validation
    $ts2 = [string]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())
    $nonce2 = 0; $baseStr2 = "test-app$ts2"
    while ($true) {
        $payload2 = "$baseStr2$nonce2"
        $hash2 = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($payload2))
        $hex2 = [BitConverter]::ToString($hash2).Replace("-","").ToLower()
        if ($hex2.StartsWith($prefix)) { break }
        $nonce2++
        if ($nonce2 -gt 500000) { break }
    }
    $multipart2 = New-Object System.Net.Http.MultipartFormDataContent
    $multipart2.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")
    $multipart2.Add((New-Object System.Net.Http.StringContent("Bad Upload")), "title")
    $fakeExe = New-Object System.Net.Http.StringContent("MZ fake")
    $multipart2.Add($fakeExe, "images", "malware.exe")
    $req2 = New-Object System.Net.Http.HttpRequestMessage(
        [System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $req2.Headers.Add("X-PoW-Timestamp", "$ts2")
    $req2.Headers.Add("X-PoW-Nonce", "$nonce2")
    $req2.Content = $multipart2
    $resp = $http.SendAsync($req2).Result
    Assert "File type validation rejects .exe" ([int]$resp.StatusCode -ne 200) "got $([int]$resp.StatusCode)"

    # ==== [8/11] Notes + Assignee ====
    Write-Host "`n[8/11] Testing notes + assignee..." -ForegroundColor Cyan

    # Add internal note
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/1/notes" '{"content":"Internal note for testing","is_public":false}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Add note returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"

    # Add public reply
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/1/notes" '{"content":"Public reply","is_public":true}'
    Assert "Add public note returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode)"

    # Empty note rejected
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/1/notes" '{"content":"","is_public":false}'
    Assert "Empty note rejected (400)" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # List notes
    $resp = AuthGet "/api/v1/admin/feedbacks/1/notes"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "List notes returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Notes contain internal note" $body.Contains("Internal note") "note missing"
    Assert "Notes contain public reply" $body.Contains("Public reply") "reply missing"

    # Delete note
    $resp = AuthReq "DELETE" "/api/v1/admin/feedbacks/1/notes/1" $null
    Assert "Delete note returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Assignee
    $resp = AuthReq "PUT" "/api/v1/admin/feedbacks/1/assignee" '{"assignee":"Alice"}'
    Assert "Update assignee returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/feedbacks/1"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback has assignee" $body.Contains('"assignee":"Alice"') "assignee missing"

    # ==== [9/11] Bulk operations + Chart + Backup ====
    Write-Host "`n[9/11] Testing bulk ops + chart + backup..." -ForegroundColor Cyan

    # Submit a second feedback for bulk tests (sleep to ensure different timestamp)
    Start-Sleep -Seconds 1
    $ts3 = [string]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())
    $nonce3 = 0; $baseStr3 = "test-app$ts3"
    while ($true) {
        $payload3 = "$baseStr3$nonce3"
        $hash3 = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($payload3))
        $hex3 = [BitConverter]::ToString($hash3).Replace("-","").ToLower()
        if ($hex3.StartsWith($prefix)) { break }
        $nonce3++
        if ($nonce3 -gt 500000) { break }
    }
    $multipart3 = New-Object System.Net.Http.MultipartFormDataContent
    $multipart3.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")
    $multipart3.Add((New-Object System.Net.Http.StringContent("Second Issue")), "title")
    $multipart3.Add((New-Object System.Net.Http.StringContent("Desc")), "description")
    $req3 = New-Object System.Net.Http.HttpRequestMessage(
        [System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $req3.Headers.Add("X-PoW-Timestamp", "$ts3")
    $req3.Headers.Add("X-PoW-Nonce", "$nonce3")
    $req3.Content = $multipart3
    $resp = $http.SendAsync($req3).Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Second feedback submitted" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"

    # Bulk status update
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/bulk-status" '{"ids":[1,2],"status":"processing"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Bulk status returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"
    Assert "Bulk status affected=2" ($body -match '"affected":\s*2') "expected 2"

    # Verify status changed
    $resp = AuthGet "/api/v1/admin/feedbacks/2"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback 2 status changed" $body.Contains('"status":"processing"') "status not updated"

    # Bulk delete (delete feedback #2)
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/bulk-delete" '{"ids":[2]}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Bulk delete returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"

    # Verify deleted
    $resp = AuthGet "/api/v1/admin/feedbacks/2"
    Assert "Deleted feedback returns 404" ([int]$resp.StatusCode -eq 404) "got $([int]$resp.StatusCode)"

    # Chart data
    $resp = AuthGet "/api/v1/admin/chart-data?days=30"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Chart data returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Chart has daily_trend" $body.Contains("daily_trend") "trend missing"
    Assert "Chart has status_distribution" $body.Contains("status_distribution") "distribution missing"

    # Backup
    $resp = AuthReq "POST" "/api/v1/admin/backup" $null
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Backup returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"
    Assert "Backup has path" $body.Contains("feedbacks_") "backup path missing"

    # ==== [10/11] Tracking (submitter self-service) ====
    Write-Host "`n[10/11] Testing tracking (submitter self-service)..." -ForegroundColor Cyan

    # Track page accessible
    $resp = $http.GetAsync("$base/track").Result
    Assert "Track page returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    $trackBody = $resp.Content.ReadAsStringAsync().Result
    Assert "Track page has token input" $trackBody.Contains("tokenInput") "input missing"

    # Query feedback by tracking token
    $resp = $http.GetAsync("$base/api/v1/track/feedback?token=$trackingToken").Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Track query returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"
    Assert "Track has feedback title" $body.Contains("E2E Test Issue") "title missing"
    Assert "Track has status" ($body.Contains("processing") -or $body.Contains("pending")) "status missing"
    Assert "Track has notes array" $body.Contains('"notes"') "notes missing"
    Assert "Track hides internal notes" (-not $body.Contains("Internal note")) "internal note leaked"
    Assert "Track shows public replies" $body.Contains("Public reply") "public reply missing"

    # Query with empty token
    $resp = $http.GetAsync("$base/api/v1/track/feedback?token=").Result
    Assert "Empty token returns 400" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # Query with invalid token
    $resp = $http.GetAsync("$base/api/v1/track/feedback?token=invalidtoken123").Result
    Assert "Invalid token returns 404" ([int]$resp.StatusCode -eq 404) "got $([int]$resp.StatusCode)"

    # Submitter reply
    $replyForm = New-Object System.Net.Http.MultipartFormDataContent
    $replyForm.Add((New-Object System.Net.Http.StringContent($trackingToken)), "token")
    $replyForm.Add((New-Object System.Net.Http.StringContent("Additional info from submitter")), "content")
    $resp = $http.PostAsync("$base/api/v1/track/reply", $replyForm).Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Submitter reply returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"
    Assert "Reply has note_id" $body.Contains("note_id") "note_id missing"

    # Empty reply rejected
    $emptyReply = New-Object System.Net.Http.MultipartFormDataContent
    $emptyReply.Add((New-Object System.Net.Http.StringContent($trackingToken)), "token")
    $emptyReply.Add((New-Object System.Net.Http.StringContent("")), "content")
    $resp = $http.PostAsync("$base/api/v1/track/reply", $emptyReply).Result
    Assert "Empty reply rejected" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # Verify the reply appears in tracking query
    $resp = $http.GetAsync("$base/api/v1/track/feedback?token=$trackingToken").Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Track shows submitter reply" $body.Contains("Additional info from submitter") "submitter reply missing"

    # ==== [11/12] Team collaboration: Admin CRUD + Priority + Duplicate ====
    Write-Host "`n[11/12] Testing team collaboration features..." -ForegroundColor Cyan

    # Current user endpoint
    $resp = AuthGet "/api/v1/admin/me"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "GET /me returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Current user has role" $body.Contains('"role":"admin"') "role missing"

    # Create admin
    $resp = AuthReq "POST" "/api/v1/admin/admins" '{"username":"editor1","password":"Editor1234","role":"editor"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create admin returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"

    # List admins
    $resp = AuthGet "/api/v1/admin/admins"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "List admins returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Admins list contains editor1" $body.Contains("editor1") "username missing"

    # Duplicate username rejected
    $resp = AuthReq "POST" "/api/v1/admin/admins" '{"username":"editor1","password":"Editor1234","role":"viewer"}'
    Assert "Duplicate admin username returns 409" ([int]$resp.StatusCode -eq 409) "got $([int]$resp.StatusCode)"

    # Find editor1's ID dynamically (super admin may be ID 1)
    $resp = AuthGet "/api/v1/admin/admins"
    $adminsBody = $resp.Content.ReadAsStringAsync().Result
    $adminsJson = $adminsBody | ConvertFrom-Json
    $editor1Id = ($adminsJson.admins | Where-Object { $_.username -eq "editor1" }).id

    # Update admin
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$editor1Id" '{"role":"viewer"}'
    Assert "Update admin returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Delete admin
    $resp = AuthReq "DELETE" "/api/v1/admin/admins/$editor1Id" $null
    Assert "Delete admin returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Priority update
    $resp = AuthReq "PUT" "/api/v1/admin/feedbacks/1/priority" '{"priority":"high"}'
    Assert "Update priority returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/feedbacks/1"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback has priority=high" $body.Contains('"priority":"high"') "priority missing"

    # Invalid priority rejected
    $resp = AuthReq "PUT" "/api/v1/admin/feedbacks/1/priority" '{"priority":"critical"}'
    Assert "Invalid priority returns 400" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # Mark as duplicate (submit a second feedback first)
    Start-Sleep -Seconds 1
    $ts4 = [string]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())
    $nonce4 = 0; $baseStr4 = "test-app$ts4"
    while ($true) {
        $payload4 = "$baseStr4$nonce4"
        $hash4 = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($payload4))
        $hex4 = [BitConverter]::ToString($hash4).Replace("-","").ToLower()
        if ($hex4.StartsWith($prefix)) { break }
        $nonce4++
        if ($nonce4 -gt 500000) { break }
    }
    $multipart4 = New-Object System.Net.Http.MultipartFormDataContent
    $multipart4.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")
    $multipart4.Add((New-Object System.Net.Http.StringContent("Duplicate Issue")), "title")
    $multipart4.Add((New-Object System.Net.Http.StringContent("Same as #1")), "description")
    $req4 = New-Object System.Net.Http.HttpRequestMessage(
        [System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $req4.Headers.Add("X-PoW-Timestamp", "$ts4")
    $req4.Headers.Add("X-PoW-Nonce", "$nonce4")
    $req4.Content = $multipart4
    $resp = $http.SendAsync($req4).Result
    Assert "Duplicate candidate submitted" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Mark feedback #3 as duplicate of #1
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/3/duplicate" '{"duplicate_of":1}'
    Assert "Mark duplicate returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/feedbacks/3"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback is_duplicate=true" $body.Contains('"is_duplicate":true') "is_duplicate missing"
    Assert "Feedback duplicate_of=1" $body.Contains('"duplicate_of":1') "duplicate_of missing"

    # Unmark duplicate
    $resp = AuthReq "DELETE" "/api/v1/admin/feedbacks/3/duplicate" $null
    Assert "Unmark duplicate returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/feedbacks/3"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback is_duplicate=false after unmark" $body.Contains('"is_duplicate":false') "still marked"

    # Enhanced search: search by contact name
    $resp = AuthGet "/api/v1/admin/feedbacks?keyword=TestUser"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Search by contact_name returns results" $body.Contains("E2E Test Issue") "contact search failed"

    # Filter by priority
    $resp = AuthGet "/api/v1/admin/feedbacks?priority=high"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Filter by priority returns results" $body.Contains("E2E Test Issue") "priority filter failed"

    # Filter by assignee
    $resp = AuthGet "/api/v1/admin/feedbacks?assignee=Alice"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Filter by assignee returns results" $body.Contains("E2E Test Issue") "assignee filter failed"

    # Feedbacks list returns assignees
    $resp = AuthGet "/api/v1/admin/feedbacks"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedbacks list has assignees array" $body.Contains('"assignees"') "assignees missing"

    # ==== [12/13] Round 6: API Tokens + Bulk Ops + CSV Import + Data Ops + Email Template ====
    Write-Host "`n[12/13] Testing Round 6 features..." -ForegroundColor Cyan

    # --- API Token CRUD ---
    $resp = AuthGet "/api/v1/admin/api-tokens"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "List API tokens returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "POST" "/api/v1/admin/api-tokens" '{"name":"CI Pipeline","project_id":"test-app"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create API token returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"
    Assert "Token starts with fs_" $body.Contains('"token":"fs_') "token prefix missing"

    # Parse token value for later use
    $tokenJson = $body | ConvertFrom-Json
    $apiToken = $tokenJson.token
    $tokenId = $tokenJson.id

    # List tokens — should have 1
    $resp = AuthGet "/api/v1/admin/api-tokens"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Token list has 1 entry" $body.Contains("CI Pipeline") "token name missing"

    # Update token — disable it
    $resp = AuthReq "PUT" "/api/v1/admin/api-tokens/$tokenId" '{"name":"CI Pipeline","project_id":"test-app","is_active":false}'
    Assert "Update token returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Re-enable it
    $resp = AuthReq "PUT" "/api/v1/admin/api-tokens/$tokenId" '{"is_active":true}'
    Assert "Re-enable token returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # --- API Token feedback submission ---
    $http2 = New-Object System.Net.Http.HttpClient
    $http2.DefaultRequestHeaders.Add("Authorization", "Bearer $apiToken")
    $content = New-Object System.Net.Http.StringContent('{"title":"API Token Feedback","description":"Submitted via API token","priority":"medium"}', [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http2.PostAsync("$base/api/v1/external/feedback", $content).Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "API token submit returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"
    Assert "API submit returns tracking_token" $body.Contains("tracking_token") "tracking_token missing"
    $http2.Dispose()

    # Invalid token rejected
    $http3 = New-Object System.Net.Http.HttpClient
    $http3.DefaultRequestHeaders.Add("Authorization", "Bearer fs_invalid_token_xxx")
    $content3 = New-Object System.Net.Http.StringContent('{"title":"Should Fail"}', [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http3.PostAsync("$base/api/v1/external/feedback", $content3).Result
    Assert "Invalid API token rejected (401)" ([int]$resp.StatusCode -eq 401) "got $([int]$resp.StatusCode)"
    $http3.Dispose()

    # --- Bulk operations (extended) ---
    # Submit a couple more feedbacks for bulk ops
    Start-Sleep -Seconds 1
    $ts5 = [string]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())
    $nonce5 = 0; $baseStr5 = "test-app$ts5"
    while ($true) {
        $payload5 = "$baseStr5$nonce5"
        $hash5 = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($payload5))
        $hex5 = [BitConverter]::ToString($hash5).Replace("-","").ToLower()
        if ($hex5.StartsWith($prefix)) { break }
        $nonce5++
        if ($nonce5 -gt 500000) { break }
    }
    $multipart5 = New-Object System.Net.Http.MultipartFormDataContent
    $multipart5.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")
    $multipart5.Add((New-Object System.Net.Http.StringContent("Bulk Test A")), "title")
    $multipart5.Add((New-Object System.Net.Http.StringContent("For bulk ops")), "description")
    $req5 = New-Object System.Net.Http.HttpRequestMessage(
        [System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $req5.Headers.Add("X-PoW-Timestamp", "$ts5")
    $req5.Headers.Add("X-PoW-Nonce", "$nonce5")
    $req5.Content = $multipart5
    $http.SendAsync($req5).Result | Out-Null

    Start-Sleep -Seconds 1
    $ts6 = [string]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())
    $nonce6 = 0; $baseStr6 = "test-app$ts6"
    while ($true) {
        $payload6 = "$baseStr6$nonce6"
        $hash6 = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($payload6))
        $hex6 = [BitConverter]::ToString($hash6).Replace("-","").ToLower()
        if ($hex6.StartsWith($prefix)) { break }
        $nonce6++
        if ($nonce6 -gt 500000) { break }
    }
    $multipart6 = New-Object System.Net.Http.MultipartFormDataContent
    $multipart6.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")
    $multipart6.Add((New-Object System.Net.Http.StringContent("Bulk Test B")), "title")
    $multipart6.Add((New-Object System.Net.Http.StringContent("For bulk ops 2")), "description")
    $req6 = New-Object System.Net.Http.HttpRequestMessage(
        [System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $req6.Headers.Add("X-PoW-Timestamp", "$ts6")
    $req6.Headers.Add("X-PoW-Nonce", "$nonce6")
    $req6.Content = $multipart6
    $http.SendAsync($req6).Result | Out-Null

    # Get feedback IDs for bulk ops (IDs 5 and 6)
    $resp = AuthGet "/api/v1/admin/feedbacks?keyword=Bulk+Test"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Bulk test feedbacks exist" $body.Contains("Bulk Test A") "bulk test feedbacks missing"

    # Bulk update tags
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/bulk-tags" '{"ids":[5,6],"tags":"bulk-test"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Bulk tags returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"
    Assert "Bulk tags affected count" $body.Contains("affected") "affected missing"

    # Verify tags updated
    $resp = AuthGet "/api/v1/admin/feedbacks/5"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback 5 has bulk tags" $body.Contains('"tags":"bulk-test"') "tags not updated"

    # Bulk update priority
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/bulk-priority" '{"ids":[5,6],"priority":"urgent"}'
    Assert "Bulk priority returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/feedbacks/5"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback 5 has urgent priority" $body.Contains('"priority":"urgent"') "priority not updated"

    # Bulk update assignee
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/bulk-assignee" '{"ids":[5,6],"assignee":"Bob"}'
    Assert "Bulk assignee returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/feedbacks/5"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback 5 has assignee Bob" $body.Contains('"assignee":"Bob"') "assignee not updated"

    # --- CSV Import ---
    $csvContent = "title,description,status,tags,priority`nImported A,Desc A,pending,imported,low`nImported B,Desc B,resolved,imported,high"
    $csvBytes = [System.Text.Encoding]::UTF8.GetBytes($csvContent)
    $csvStream = New-Object System.IO.MemoryStream(,$csvBytes)

    $csvForm = New-Object System.Net.Http.MultipartFormDataContent
    $csvForm.Add((New-Object System.Net.Http.StreamContent($csvStream)), "file", "import.csv")
    $csvForm.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")

    $csvReq = New-Object System.Net.Http.HttpRequestMessage(
        [System.Net.Http.HttpMethod]::Post, "$base/api/v1/admin/import/csv")
    $csvReq.Headers.Add("Cookie", $cookie)
    $csvReq.Headers.Add("X-CSRF-Token", $csrfToken)
    $csvReq.Content = $csvForm
    $resp = $http.SendAsync($csvReq).Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "CSV import returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"
    Assert "CSV import count=2" $body.Contains('"imported":2') "imported count wrong"

    # Verify imported feedbacks
    $resp = AuthGet "/api/v1/admin/feedbacks?keyword=Imported+A"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Imported feedback A exists" $body.Contains("Imported A") "imported A missing"

    # --- Data Archive ---
    $resp = AuthReq "POST" "/api/v1/admin/archive" '{"days_old":365}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Archive returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"
    Assert "Archive has archived count" $body.Contains('"archived"') "archived count missing"

    # Invalid archive days
    $resp = AuthReq "POST" "/api/v1/admin/archive" '{"days_old":0}'
    Assert "Archive with 0 days rejected" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # --- Prune Backups ---
    $resp = AuthReq "POST" "/api/v1/admin/prune-backups" '{"days_old":30}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Prune backups returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"

    # --- Email Template ---
    $resp = AuthGet "/api/v1/admin/config/email-template"
    Assert "Get email template returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "PUT" "/api/v1/admin/config/email-template" '{"subject_template":"[FeedShit] {{title}}","body_template":"Project: {{project}}\nTitle: {{title}}\nURL: {{admin_url}}"}'
    Assert "Save email template returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/config/email-template"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Email template subject saved" $body.Contains("[FeedShit]") "subject template not saved"

    # --- System config new fields ---
    $resp = AuthGet "/api/v1/admin/config/system"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "System config has webhook_type" $body.Contains('"webhook_type"') "webhook_type missing"

    $resp = AuthReq "PUT" "/api/v1/admin/config/system" '{"webhook_url":"https://example.com/hook","webhook_type":"feishu","archive_days":"90","backup_retention_days":"30"}'
    Assert "Save system config with new fields returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/config/system"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Webhook type saved as feishu" $body.Contains('"webhook_type":"feishu"') "webhook_type not saved"

    # --- Delete API Token ---
    $resp = AuthReq "DELETE" "/api/v1/admin/api-tokens/$tokenId" $null
    Assert "Delete API token returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Verify deleted token can no longer submit
    $http4 = New-Object System.Net.Http.HttpClient
    $http4.DefaultRequestHeaders.Add("Authorization", "Bearer $apiToken")
    $content4 = New-Object System.Net.Http.StringContent('{"title":"Should Fail"}', [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http4.PostAsync("$base/api/v1/external/feedback", $content4).Result
    Assert "Deleted token rejected (401)" ([int]$resp.StatusCode -eq 401) "got $([int]$resp.StatusCode)"
    $http4.Dispose()

    # ==== [13/14] Round 7: Project Archive + Slug Redirect + Member Isolation ====
    Write-Host "`n[13/14] Testing Round 7 features..." -ForegroundColor Cyan

    # --- Project Archive ---
    $resp = AuthReq "POST" "/api/v1/admin/projects" '{"name":"Archive Proj","slug":"archive-slug","description":""}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create archive-test project returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"
    $archiveProj = $body | ConvertFrom-Json
    $archiveProjId = $archiveProj.id

    # Archive the project
    $resp = AuthReq "POST" "/api/v1/admin/projects/$archiveProjId/archive" '{"archived":true}'
    Assert "Archive project returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Archived project returns 410 on /fb/:slug
    $resp = AuthGet "/fb/archive-slug"
    Assert "Archived project /fb/ returns 410" ([int]$resp.StatusCode -eq 410) "got $([int]$resp.StatusCode)"

    # Archived project hidden from public list
    $resp = $http.GetAsync("$base/api/v1/projects").Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Archived project hidden from public" (-not $body.Contains('"slug":"archive-slug"')) "still visible"

    # Admin can see archived projects with ?archived=true
    $resp = AuthGet "/api/v1/admin/projects?archived=true"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Admin sees archived projects" $body.Contains('"slug":"archive-slug"') "archived not in admin list"

    # Unarchive the project
    $resp = AuthReq "POST" "/api/v1/admin/projects/$archiveProjId/archive" '{"archived":false}'
    Assert "Unarchive project returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Now /fb/archive-slug should work again
    $resp = AuthGet "/fb/archive-slug"
    Assert "Unarchived project /fb/ returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # --- Slug Redirect ---
    $resp = AuthReq "POST" "/api/v1/admin/projects" '{"name":"Slug Test","slug":"old-slug-name","description":""}'
    Assert "Create slug-test project returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode)"
    $body = $resp.Content.ReadAsStringAsync().Result
    $slugProj = $body | ConvertFrom-Json
    $slugProjId = $slugProj.id

    # Verify old slug works
    $resp = AuthGet "/fb/old-slug-name"
    Assert "Old slug returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Change slug via PUT
    $resp = AuthReq "PUT" "/api/v1/admin/projects/$slugProjId" '{"name":"Slug Test","slug":"new-slug-name","description":"","is_active":true}'
    Assert "Update slug returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # New slug works
    $resp = AuthGet "/fb/new-slug-name"
    Assert "New slug returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Old slug returns 301 redirect (use HttpClient with no auto-redirect)
    $noRedirectHandler = New-Object System.Net.Http.HttpClientHandler
    $noRedirectHandler.AllowAutoRedirect = $false
    $noRedirectHttp = New-Object System.Net.Http.HttpClient($noRedirectHandler)
    $noRedirectHttp.BaseAddress = [System.Uri]$base
    $resp = $noRedirectHttp.GetAsync("/fb/old-slug-name").Result
    Assert "Old slug returns 301" ([int]$resp.StatusCode -eq 301) "got $([int]$resp.StatusCode)"
    # Check Location header points to new slug
    $location = ""
    if ($resp.Headers.Location) { $location = $resp.Headers.Location.ToString() }
    Assert "301 Location points to new slug" $location.Contains("new-slug-name") "location=$location"
    $noRedirectHttp.Dispose()
    $noRedirectHandler.Dispose()

    # --- Project Member Isolation ---
    # Create an editor with restricted access
    $resp = AuthReq "POST" "/api/v1/admin/admins" '{"username":"restricted-ed","password":"Restr1234","role":"editor"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create restricted editor returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"
    $restrictJson = $body | ConvertFrom-Json
    $restrictId = $restrictJson.id

    # Create two isolated projects
    $resp = AuthReq "POST" "/api/v1/admin/projects" '{"name":"Isolated A","slug":"isolated-a","description":""}'
    Assert "Create isolated-a returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode)"
    $body = $resp.Content.ReadAsStringAsync().Result
    $isoA = $body | ConvertFrom-Json

    $resp = AuthReq "POST" "/api/v1/admin/projects" '{"name":"Isolated B","slug":"isolated-b","description":""}'
    Assert "Create isolated-b returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode)"
    $body = $resp.Content.ReadAsStringAsync().Result
    $isoB = $body | ConvertFrom-Json

    # Set restricted editor's project scope to only isolated-a
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$restrictId/grants" '{"grants":[{"project_slug":"isolated-a","category_key":"*","role":"editor"}]}'
    Assert "Set project grants returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Verify grants API returns the restriction
    $resp = AuthGet "/api/v1/admin/admins/$restrictId/grants"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Get project grants returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Project grants contains isolated-a" $body.Contains('"isolated-a"') "isolated-a missing"
    Assert "Project grants excludes isolated-b" (-not $body.Contains('"isolated-b"')) "isolated-b should not be there"

    # Login as restricted editor and verify project list is filtered
    $restrictLogin = New-Object System.Net.Http.StringContent(
        '{"username":"restricted-ed","password":"Restr1234"}',
        [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http.PostAsync("$base/api/v1/admin/login", $restrictLogin).Result
    Assert "Restricted editor login returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $restrictCookie = ""
    foreach ($h in $resp.Headers) {
        if ($h.Key -eq "Set-Cookie") {
            foreach ($v in $h.Value) {
                if ($v -match "admin_session=([^;]+)") { $restrictCookie = $Matches[1] }
            }
        }
    }
    Assert "Restricted editor got session cookie" ($restrictCookie.Length -gt 0) "no cookie"

    # Use restricted editor's cookie to list projects
    $restrictReq = New-Object System.Net.Http.HttpRequestMessage([System.Net.Http.HttpMethod]::Get, "$base/api/v1/admin/projects")
    $restrictReq.Headers.Add("Cookie", "admin_session=$restrictCookie")
    $resp = $http.SendAsync($restrictReq).Result
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Restricted editor list projects returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    Assert "Restricted editor sees isolated-a" $body.Contains('"slug":"isolated-a"') "isolated-a missing"
    Assert "Restricted editor cannot see isolated-b" (-not $body.Contains('"slug":"isolated-b"')) "isolated-b should be hidden"

    # Clear restriction (empty list = unrestricted)
    # Re-login as admin first to ensure we have a valid admin session
    $relogin = New-Object System.Net.Http.StringContent(
        '{"username":"testadmin","password":"Setup123"}',
        [System.Text.Encoding]::UTF8, "application/json")
    $resp = $http.PostAsync("$base/api/v1/admin/login", $relogin).Result
    $newCookie = ""
    $newCsrf = ""
    foreach ($h in $resp.Headers) {
        if ($h.Key -eq "Set-Cookie") {
            foreach ($v in $h.Value) {
                if ($v -match "admin_session=([^;]+)") { $newCookie = $Matches[1] }
                if ($v -match "csrf_token=([^;]+)") { $newCsrf = $Matches[1] }
            }
        }
    }
    # Update cookie and csrfToken for subsequent admin requests
    $cookie = $newCookie
    $csrfToken = $newCsrf

    $resp = AuthReq "PUT" "/api/v1/admin/admins/$restrictId/grants" '{"grants":[]}'
    Assert "Clear project restriction returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Cleanup: delete restricted editor and isolated projects
    $resp = AuthReq "DELETE" "/api/v1/admin/admins/$restrictId" $null
    Assert "Delete restricted editor returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "DELETE" "/api/v1/admin/projects/$($isoA.id)" $null
    Assert "Delete isolated-a returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "DELETE" "/api/v1/admin/projects/$($isoB.id)" $null
    Assert "Delete isolated-b returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "DELETE" "/api/v1/admin/projects/$archiveProjId" $null
    Assert "Delete archive-proj returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "DELETE" "/api/v1/admin/projects/$slugProjId" $null
    Assert "Delete slug-proj returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # --- Categories (Feature A) ---
    Write-Host "`n[14/16] Testing categories..." -ForegroundColor Cyan

    # Create categories for test-app project (project ID=1)
    $resp = AuthReq "POST" "/api/v1/admin/projects/1/categories" '{"key":"performance","name":"性能问题","color":"#e53e3e","sort_order":1}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create category performance returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"
    $perfCatJson = $body | ConvertFrom-Json
    $perfCatId = $perfCatJson.id

    $resp = AuthReq "POST" "/api/v1/admin/projects/1/categories" '{"key":"ui","name":"界面问题","color":"#3182ce","sort_order":2}'
    Assert "Create category ui returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode)"

    $resp = AuthReq "POST" "/api/v1/admin/projects/1/categories" '{"key":"network","name":"网络问题","color":"#38a169","sort_order":3}'
    Assert "Create category network returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode)"

    # Duplicate key should fail
    $resp = AuthReq "POST" "/api/v1/admin/projects/1/categories" '{"key":"performance","name":"Dup","color":"#000","sort_order":4}'
    Assert "Duplicate category key returns 409" ([int]$resp.StatusCode -eq 409) "got $([int]$resp.StatusCode)"

    # List categories
    $resp = AuthGet "/api/v1/admin/projects/1/categories"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "List categories returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    $catsJson = $body | ConvertFrom-Json
    Assert "Categories list has 3 items" ($catsJson.categories.Count -eq 3) "got $($catsJson.categories.Count)"

    # Update category
    $resp = AuthReq "PUT" "/api/v1/admin/categories/$perfCatId" '{"name":"性能与稳定性","color":"#c53030","sort_order":1}'
    Assert "Update category returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Set feedback category
    $resp = AuthReq "PATCH" "/api/v1/admin/feedbacks/1/category" '{"category":"performance"}'
    Assert "Set feedback category returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # Verify feedback has category
    $resp = AuthGet "/api/v1/admin/feedbacks/1"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Feedback has category field" $body.Contains('"category":"performance"') "category not set"

    # Filter feedbacks by category
    $resp = AuthGet "/api/v1/admin/feedbacks?category=performance"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Filter by category returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    $fbJson = $body | ConvertFrom-Json
    Assert "Category filter returns matching feedbacks" ($fbJson.total -ge 1) "got total=$($fbJson.total)"

    # Public submit with valid category — use clean client (no admin cookies) to avoid CSRF
    $pubHandler = New-Object System.Net.Http.HttpClientHandler
    $pubHandler.AllowAutoRedirect = $false
    $pubHandler.UseCookies = $false
    $pubHttp = New-Object System.Net.Http.HttpClient($pubHandler)

    $ts2 = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds().ToString()
    $prefix = "0" * 4  # difficulty 4
    $nonce2 = 0
    $baseStr2 = "test-app" + $ts2
    do {
        $hash = [System.Security.Cryptography.SHA256]::Create().ComputeHash([System.Text.Encoding]::UTF8.GetBytes($baseStr2 + $nonce2))
        $hex2 = [BitConverter]::ToString($hash).Replace("-","").ToLower()
        if ($hex2.StartsWith($prefix)) { break }
        $nonce2++
    } while ($nonce2 -lt 500000)
    $mp2 = New-Object System.Net.Http.MultipartFormDataContent
    $mp2.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")
    $mp2.Add((New-Object System.Net.Http.StringContent("Category test from public form")), "title")
    $mp2.Add((New-Object System.Net.Http.StringContent("ui")), "category")
    $catReq = New-Object System.Net.Http.HttpRequestMessage([System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $catReq.Headers.Add("X-PoW-Timestamp", "$ts2")
    $catReq.Headers.Add("X-PoW-Nonce", "$nonce2")
    $catReq.Content = $mp2
    $catSubmitResp = $pubHttp.SendAsync($catReq).Result
    Assert "Public submit with valid category returns 200" ([int]$catSubmitResp.StatusCode -eq 200) "got $([int]$catSubmitResp.StatusCode)"
    # Find the new feedback and check its category
    $resp = AuthGet "/api/v1/admin/feedbacks?search=Category+test+from+public+form"
    $searchBody = $resp.Content.ReadAsStringAsync().Result
    Assert "Public submitted feedback has category=ui" $searchBody.Contains('"category":"ui"') "category not set"

    # Public submit with invalid category should be rejected
    Start-Sleep -Seconds 1  # ensure timestamp differs from previous submit
    $ts3 = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds().ToString()
    $nonce3 = 0
    $baseStr3 = "test-app" + $ts3
    do {
        $hash3 = [System.Security.Cryptography.SHA256]::Create().ComputeHash([System.Text.Encoding]::UTF8.GetBytes($baseStr3 + $nonce3))
        $hex3 = [BitConverter]::ToString($hash3).Replace("-","").ToLower()
        if ($hex3.StartsWith($prefix)) { break }
        $nonce3++
    } while ($nonce3 -lt 500000)
    $mp3 = New-Object System.Net.Http.MultipartFormDataContent
    $mp3.Add((New-Object System.Net.Http.StringContent("test-app")), "project_id")
    $mp3.Add((New-Object System.Net.Http.StringContent("Bad category public test")), "title")
    $mp3.Add((New-Object System.Net.Http.StringContent("fake-category")), "category")
    $badCatReq = New-Object System.Net.Http.HttpRequestMessage([System.Net.Http.HttpMethod]::Post, "$base/api/v1/feedback/submit")
    $badCatReq.Headers.Add("X-PoW-Timestamp", "$ts3")
    $badCatReq.Headers.Add("X-PoW-Nonce", "$nonce3")
    $badCatReq.Content = $mp3
    $badCatResp = $pubHttp.SendAsync($badCatReq).Result
    $badCatBody = $badCatResp.Content.ReadAsStringAsync().Result
    Assert "Public submit with invalid category returns 400" ([int]$badCatResp.StatusCode -eq 400) "got $([int]$badCatResp.StatusCode) body=$badCatBody"
    $pubHttp.Dispose()

    # Bulk update category
    $resp = AuthReq "POST" "/api/v1/admin/feedbacks/bulk-category" '{"ids":[5,6],"category":"ui"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Bulk update category returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"

    # Chart data includes category_distribution
    $resp = AuthGet "/api/v1/admin/chart-data?days=30"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Chart data includes category_distribution" $body.Contains("category_distribution") "missing category_distribution"

    # Delete category with feedback references should clear them
    # First assign feedback 1 to "network"
    $resp = AuthReq "PATCH" "/api/v1/admin/feedbacks/1/category" '{"category":"network"}'
    Assert "Set feedback 1 to network" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    # Get network category ID
    $resp = AuthGet "/api/v1/admin/projects/1/categories"
    $catsBody = $resp.Content.ReadAsStringAsync().Result
    $catsJson = $catsBody | ConvertFrom-Json
    $networkId = $null
    foreach ($c in $catsJson.categories) { if ($c.key -eq "network") { $networkId = $c.id } }
    Assert "Network category exists" ($networkId -ne $null) "not found"
    # Delete network category
    $resp = AuthReq "DELETE" "/api/v1/admin/categories/$networkId" $null
    Assert "Delete network category returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    # Feedback 1's category should be cleared
    $resp = AuthGet "/api/v1/admin/feedbacks/1"
    $fb1Body = $resp.Content.ReadAsStringAsync().Result
    $fb1Json = $fb1Body | ConvertFrom-Json
    Assert "Deleted category clears feedback category" ($fb1Json.category -eq "") "got category=$($fb1Json.category)"

    # --- Member Grants (Feature B) ---
    Write-Host "`n[15/16] Testing member grants..." -ForegroundColor Cyan

    # Create a manager user
    $resp = AuthReq "POST" "/api/v1/admin/admins" '{"username":"mgr1","password":"Manager1234","role":"manager"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create manager returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"
    $mgrJson = $body | ConvertFrom-Json
    $mgrId = $mgrJson.id

    # Set grants: manager has editor access on test-app with wildcard categories
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$mgrId/grants" '{"grants":[{"project_slug":"test-app","category_key":"*","role":"editor"}]}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Set grants returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$body"

    # Get grants
    $resp = AuthGet "/api/v1/admin/admins/$mgrId/grants"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Get grants returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"
    $grantsJson = $body | ConvertFrom-Json
    Assert "Grants list has 1 item" ($grantsJson.grants.Count -eq 1) "got $($grantsJson.grants.Count)"
    Assert "Grant has correct project" ($grantsJson.grants[0].project_slug -eq "test-app") "got $($grantsJson.grants[0].project_slug)"
    Assert "Grant has wildcard category" ($grantsJson.grants[0].category_key -eq "*") "got $($grantsJson.grants[0].category_key)"

    # Update grants: add category-specific access
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$mgrId/grants" '{"grants":[{"project_slug":"test-app","category_key":"performance","role":"editor"},{"project_slug":"test-app","category_key":"ui","role":"viewer"}]}'
    Assert "Update grants with categories returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/admins/$mgrId/grants"
    $body = $resp.Content.ReadAsStringAsync().Result
    $grantsJson2 = $body | ConvertFrom-Json
    Assert "Updated grants has 2 items" ($grantsJson2.grants.Count -eq 2) "got $($grantsJson2.grants.Count)"

    # Invalid project slug should fail
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$mgrId/grants" '{"grants":[{"project_slug":"nonexistent","category_key":"*","role":"editor"}]}'
    Assert "Grant with invalid project returns 400" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # Invalid role should fail
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$mgrId/grants" '{"grants":[{"project_slug":"test-app","category_key":"*","role":"superadmin"}]}'
    Assert "Grant with invalid role returns 400" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # Invalid category_key should fail
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$mgrId/grants" '{"grants":[{"project_slug":"test-app","category_key":"nonexistent-cat","role":"editor"}]}'
    Assert "Grant with invalid category returns 400" ([int]$resp.StatusCode -eq 400) "got $([int]$resp.StatusCode)"

    # Delete single grant
    $grantId = $grantsJson2.grants[0].id
    $resp = AuthReq "DELETE" "/api/v1/admin/admins/$mgrId/grants/$grantId"
    Assert "Delete single grant returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/admins/$mgrId/grants"
    $body = $resp.Content.ReadAsStringAsync().Result
    $grantsJson3 = $body | ConvertFrom-Json
    Assert "After delete, grants has 1 item" ($grantsJson3.grants.Count -eq 1) "got $($grantsJson3.grants.Count)"

    # Cleanup: delete manager and categories
    $resp = AuthReq "DELETE" "/api/v1/admin/admins/$mgrId" $null
    Assert "Delete manager returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # --- Security: H1/H2/H3 ---
    Write-Host "`n[16/17] Testing security fixes (H1/H2/H3)..." -ForegroundColor Cyan

    # H2: New user with no grants sees empty feedback list
    $resp = AuthReq "POST" "/api/v1/admin/admins" '{"username":"norole-ed","password":"Norole1234","role":"editor"}'
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Create norole-ed returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$body"
    $noroleJson = $body | ConvertFrom-Json
    $noroleId = $noroleJson.id

    # Login as norole-ed and check feedback list — use separate handler to avoid cookie pollution
    $noroleHandler = New-Object System.Net.Http.HttpClientHandler
    $noroleHandler.AllowAutoRedirect = $false
    $noroleHttp = New-Object System.Net.Http.HttpClient($noroleHandler)
    $loginResp = $noroleHttp.PostAsync("$base/api/v1/admin/login", $(New-Object System.Net.Http.StringContent('{"username":"norole-ed","password":"Norole1234"}', [System.Text.Encoding]::UTF8, 'application/json'))).Result
    Assert "norole-ed login returns 200" ([int]$loginResp.StatusCode -eq 200) "got $([int]$loginResp.StatusCode)"
    $noroleCookies = $loginResp.Headers.GetValues("Set-Cookie")
    $noroleSession = ""
    foreach ($ck in $noroleCookies) { if ($ck -match "admin_session=([^;]+)") { $noroleSession = $Matches[1] } }
    $noroleHttp.DefaultRequestHeaders.Add("Cookie", "admin_session=$noroleSession")

    # H2: Feedback list should be empty (no grants = no access)
    $fbResp = $noroleHttp.GetAsync("$base/api/v1/admin/feedbacks").Result
    $fbBody = $fbResp.Content.ReadAsStringAsync().Result
    $fbData = $fbBody | ConvertFrom-Json
    Assert "H2: No-grant editor sees 0 feedbacks" ($fbData.total -eq 0) "got total=$($fbData.total)"

    # H2: Project list should also be empty
    $projResp = $noroleHttp.GetAsync("$base/api/v1/admin/projects").Result
    $projBody = $projResp.Content.ReadAsStringAsync().Result
    $projData = $projBody | ConvertFrom-Json
    Assert "H2: No-grant editor sees 0 projects" ($projData.projects.Count -eq 0) "got $($projData.projects.Count)"

    # H1: norole-ed tries to read feedback #1 by ID — should be forbidden
    $getResp = $noroleHttp.GetAsync("$base/api/v1/admin/feedbacks/1").Result
    Assert "H1: No-grant editor gets 403 on feedback detail" ([int]$getResp.StatusCode -eq 403) "got $([int]$getResp.StatusCode)"

    # Now grant norole-ed access to test-app and verify they can read
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$noroleId/grants" '{"grants":[{"project_slug":"test-app","category_key":"*","role":"viewer"}]}'
    $grantBody = $resp.Content.ReadAsStringAsync().Result
    Assert "Grant norole-ed viewer on test-app" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode) body=$grantBody noroleId=$noroleId"

    $getResp2 = $noroleHttp.GetAsync("$base/api/v1/admin/feedbacks/1").Result
    Assert "H1: Granted viewer can read feedback" ([int]$getResp2.StatusCode -eq 200) "got $([int]$getResp2.StatusCode)"

    # H1: Viewer cannot write (status update should be forbidden)
    $statusReq = New-Object System.Net.Http.StringContent('{"status":"processing"}', [System.Text.Encoding]::UTF8, 'application/json')
    # Need CSRF for PUT — get token first (use separate variable to avoid overwriting admin's $csrfToken)
    $csrfResp = $noroleHttp.GetAsync("$base/api/v1/admin/csrf-token").Result
    $csrfBody = $csrfResp.Content.ReadAsStringAsync().Result
    $csrfData = $csrfBody | ConvertFrom-Json
    $noroleCsrfToken = $csrfData.csrf_token
    $putMsg = New-Object System.Net.Http.HttpRequestMessage("PUT", "$base/api/v1/admin/feedbacks/1/status")
    $putMsg.Content = $statusReq
    $putMsg.Headers.Add("X-CSRF-Token", $noroleCsrfToken)
    $putResp = $noroleHttp.SendAsync($putMsg).Result
    Assert "H1: Viewer cannot update status (403)" ([int]$putResp.StatusCode -eq 403) "got $([int]$putResp.StatusCode)"

    $noroleHttp.Dispose()

    # Target category permission: editor on "performance" cannot move feedback to "ui"
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$noroleId/grants" '{"grants":[{"project_slug":"test-app","category_key":"performance","role":"editor"}]}'
    Assert "Grant norole-ed editor on performance" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $catHandler = New-Object System.Net.Http.HttpClientHandler
    $catHandler.AllowAutoRedirect = $false
    $catHttp = New-Object System.Net.Http.HttpClient($catHandler)
    $catLoginResp = $catHttp.PostAsync("$base/api/v1/admin/login", $(New-Object System.Net.Http.StringContent('{"username":"norole-ed","password":"Norole1234"}', [System.Text.Encoding]::UTF8, 'application/json'))).Result
    $catCookies = $catLoginResp.Headers.GetValues("Set-Cookie")
    $catSession = ""
    $catCsrf = ""
    foreach ($ck in $catCookies) {
        if ($ck -match "admin_session=([^;]+)") { $catSession = $Matches[1] }
        if ($ck -match "csrf_token=([^;]+)") { $catCsrf = $Matches[1] }
    }
    $catMsg = New-Object System.Net.Http.HttpRequestMessage("PATCH", "$base/api/v1/admin/feedbacks/1/category")
    $catMsg.Content = New-Object System.Net.Http.StringContent('{"category":"ui"}', [System.Text.Encoding]::UTF8, 'application/json')
    $catMsg.Headers.Add("Cookie", "admin_session=$catSession; csrf_token=$catCsrf")
    $catMsg.Headers.Add("X-CSRF-Token", $catCsrf)
    $catResp = $catHttp.SendAsync($catMsg).Result
    Assert "Target category perm: editor on performance cannot move to ui" ([int]$catResp.StatusCode -eq 403) "got $([int]$catResp.StatusCode)"
    $catHttp.Dispose()

    # Restore wildcard viewer for cleanup
    $resp = AuthReq "PUT" "/api/v1/admin/admins/$noroleId/grants" '{"grants":[{"project_slug":"test-app","category_key":"*","role":"viewer"}]}'

    # H3: Submit feedback with invalid category via API token should be rejected
    # Create an API token for test-app
    $resp = AuthReq "POST" "/api/v1/admin/api-tokens" '{"name":"H3-Test","project_id":"test-app"}'
    $tokenBody = $resp.Content.ReadAsStringAsync().Result
    Assert "H3: Create API token returns 201" ([int]$resp.StatusCode -eq 201) "got $([int]$resp.StatusCode) body=$tokenBody"
    $tokenJson = $tokenBody | ConvertFrom-Json
    $testToken = $tokenJson.token

    # Submit with invalid category — use separate handler
    if ($testToken) {
    $h3Handler = New-Object System.Net.Http.HttpClientHandler
    $h3Handler.AllowAutoRedirect = $false
    $h3Http = New-Object System.Net.Http.HttpClient($h3Handler)
    $h3Http.DefaultRequestHeaders.Add("Authorization", "Bearer $testToken")
    $h3Body = New-Object System.Net.Http.StringContent('{"title":"H3 dirty category test","description":"Testing","category":"nonexistent-category"}', [System.Text.Encoding]::UTF8, 'application/json')
    $h3Resp = $h3Http.PostAsync("$base/api/v1/external/feedback", $h3Body).Result
    Assert "H3: API submit with invalid category returns 400" ([int]$h3Resp.StatusCode -eq 400) "got $([int]$h3Resp.StatusCode)"
    $h3RespBody = $h3Resp.Content.ReadAsStringAsync().Result
    Assert "H3: Error message mentions category" $h3RespBody.Contains("分类") "body=$h3RespBody"
    $h3Http.Dispose()
    } else {
        Assert "H3: API submit with invalid category returns 400" $false "skipped — token creation failed"
        Assert "H3: Error message mentions category" $false "skipped"
    }

    # Cleanup: delete the test token
    $tokenId = $tokenJson.id
    $resp = AuthReq "DELETE" "/api/v1/admin/api-tokens/$tokenId" $null

    # Cleanup: delete norole-ed
    $resp = AuthReq "DELETE" "/api/v1/admin/admins/$noroleId" $null
    Assert "Delete norole-ed returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    # ==== [16/16] Cleanup ====
    Write-Host "`n[16/16] Testing project deletion..." -ForegroundColor Cyan

    $resp = AuthReq "DELETE" "/api/v1/admin/projects/2" $null
    Assert "Delete project returns 200" ([int]$resp.StatusCode -eq 200) "got $([int]$resp.StatusCode)"

    $resp = AuthGet "/api/v1/admin/projects"
    $body = $resp.Content.ReadAsStringAsync().Result
    Assert "Deleted project not in list" (-not $body.Contains('"slug":"website"')) "still present"

} finally {
    if ($proc -and !$proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    }
    if ($http) { $http.Dispose() }
}

# ---- Summary ----
Write-Host "`n================================" -ForegroundColor White
$total = $counters.passed + $counters.failed
if ($counters.failed -eq 0) {
    Write-Host "All $total tests PASSED" -ForegroundColor Green
} else {
    Write-Host "$($counters.failed)/$total tests FAILED" -ForegroundColor Red
}
Write-Host "================================`n" -ForegroundColor White

exit $counters.failed
