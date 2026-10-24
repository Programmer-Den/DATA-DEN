# Registers the runner containers with GitHub once. Guide: runner/README.md
$secure = Read-Host "Registration token" -AsSecureString
$env:RUNNER_TOKEN = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
    [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
$compose = @("compose", "-f", "$PSScriptRoot/runner.compose.yaml")
try {
    docker @compose up -d
} finally {
    Remove-Item Env:RUNNER_TOKEN
}
docker @compose logs --tail 20
