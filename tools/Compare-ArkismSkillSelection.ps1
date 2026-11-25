[CmdletBinding()]
param([Parameter(Mandatory)][string]$SelectedRef,[string[]]$FilePath=@())
$ErrorActionPreference='Stop'
$profile=Get-Content -Raw -LiteralPath (Join-Path $env:LOCALAPPDATA 'dev.arkism.desktop/managed-local.json') | ConvertFrom-Json
$endpoint=[Uri]$profile.server
if(-not $endpoint.IsLoopback -or $endpoint.Scheme -notin 'http','https'){throw 'Expected the managed loopback endpoint'}
$settings=@{
    ARKISM_URL=$endpoint.AbsoluteUri.TrimEnd('/')
    ARKISM_TOKEN_FILE=(Get-Item -LiteralPath (Join-Path $profile.credentials 'owner.token')).FullName
    ARKISM_WORKSPACE=$profile.scope.workspace_id
    ARKISM_PROJECT=$profile.scope.project_id
    ARKISM_USER=$profile.scope.user_id
    ARKISM_AGENT=[string]$profile.scope.agent_id
}
$saved=@{}
try {
    foreach($key in $settings.Keys){
        $saved[$key]=[Environment]::GetEnvironmentVariable($key,'Process')
        [Environment]::SetEnvironmentVariable($key,[string]$settings[$key],'Process')
    }
    $argsList=@($SelectedRef)
    foreach($path in $FilePath){$argsList+=@("--file",$path)}
    & (Join-Path $env:LOCALAPPDATA 'Programs/Arkism/integration/arkism-skill-selection.exe') @argsList
    if($LASTEXITCODE -ne 0){throw "Selection comparison failed (exit $LASTEXITCODE)"}
} finally {
    foreach($key in $saved.Keys){[Environment]::SetEnvironmentVariable($key,$saved[$key],'Process')}
}
