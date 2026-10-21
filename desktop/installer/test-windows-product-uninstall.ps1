# Actual common core and product policies with owned inert files and fake effects only.
param([string]$Filter = '')

function Invoke-ProductUninstallOfflineTests([string]$Filter) {
  $ErrorActionPreference = 'Stop'
  . (Join-Path $PSScriptRoot 'smoke-windows-installed.ps1')
  . (Join-Path $PSScriptRoot 'test-nsis-uninstall-job.ps1')
  . (Join-Path $PSScriptRoot 'smoke-windows-product-uninstall.ps1')
  $productionProcessReader = ${function:Get-ProductProcessRows}
  $core = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'WindowsProcessJob.cs'))
  $product = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'WindowsProductUninstallCheck.cs'))
  $fake = @'
namespace Arkism.InstallerChecks {
  using System;
  using System.Collections.Generic;
  public sealed class ProductTestClock : IMonotonicClock {
    public long Now; public long Milliseconds { get { return Now; } }
    public void Sleep(int value) { Now += value; }
  }
  public sealed class ProductTestApi : IJobApi, IProductProcessIdentity {
    public int ResumeCalls, FactoryCalls, TerminateCalls, Closed;
    public bool Hang, Killed; public Action Mutation;
    public string Faults = ""; public ProductTestClock Clock;
    public long IdentityPid = 2048; public string IdentityImage;
    private void Fault(string code) { if (Faults.Contains(code)) throw new JobApiException("test_" + code); }
    public IntPtr CreateJob() { return new IntPtr(11); }
    public void Configure(IntPtr value) { }
    public ProcessHandles CreateSuspended(JobRequest value) { return new ProcessHandles { Process=new IntPtr(12), Thread=new IntPtr(13) }; }
    public void Assign(IntPtr job, IntPtr process) { }
    public bool IsMember(IntPtr job, IntPtr process) { return true; }
    public void VerifyImage(JobRequest value, IntPtr process) { if(Mutation!=null) Mutation(); Fault("image"); }
    ProductProcessIdentity IProductProcessIdentity.GetVerifiedIdentity(IntPtr process) {
      if(process!=new IntPtr(12)) throw new Exception("foreign process identity query");
      return new ProductProcessIdentity(IdentityPid,IdentityImage);
    }
    public uint Resume(IntPtr thread) { ResumeCalls++; Fault("resume"); return 1; }
    public JobAccounting Query(IntPtr job) { return new JobAccounting { Active=Killed || (!Hang && Clock.Now>=20) ? 0U:1U, Total=4 }; }
    public uint ExitCode(IntPtr process) { return 0; }
    public void TerminateRoot(IntPtr process) { TerminateCalls++; Killed=true; Fault("terminate_root"); }
    public void ReapRoot(IntPtr process,int budget) { Fault("reap"); }
    public void TerminateJob(IntPtr job) { TerminateCalls++; Fault("terminate_job"); Killed=true; }
    public void Close(IntPtr handle) { Closed++; Fault("close"); }
  }
  public static class ProductTestHarness {
    public static ProductTestApi Last;
    public static string Environment(ProductUninstallBinding value) { return ProductUninstallCheck.EnvironmentBlock(value); }
    public static void BindingGate(Action binding) {
      var clock=new ProductTestClock();Last=new ProductTestApi { Clock=clock };
      ProductUninstallCheck.RunCore(new JobRequest(),binding,delegate(long pid,string image) { },
        delegate { Last.FactoryCalls++;return Last; },clock);
    }
    public static JobResult Run(Action<long,string> policy, Action mutation, bool hang, string faults,string image,long pid,string identityImage) {
      var clock=new ProductTestClock(); Last=new ProductTestApi { Clock=clock, Mutation=mutation, Hang=hang, Faults=faults,IdentityPid=pid,IdentityImage=identityImage };
      return ProductUninstallCheck.RunCore(new JobRequest { TimeoutMilliseconds=60, TerminationMilliseconds=40,Executable=image },
        delegate { },policy,delegate { Last.FactoryCalls++; return Last; },clock);
    }
  }
}
'@
  $compiler = @{}
  if ($PSVersionTable.PSVersion.Major -ge 7) { $compiler.CompilerOptions = '/langversion:5' }
  Add-Type -TypeDefinition ($core + "`n" + $product + "`n" + $fake) @compiler
  $suite = Join-Path ([IO.Path]::GetTempPath()) ('arkism-product-offline-' + [guid]::NewGuid().ToString('N'))
  [IO.Directory]::CreateDirectory($suite) | Out-Null
  function Test-ProductCase([string]$Name, [scriptblock]$Body) {
    if ($Filter -and $Name -notmatch $Filter) { return }
    & $Body
    $script:ProductPassed++
    Write-Output "PASS $Name"
  }
  function Require-ProductTest([bool]$Value, [string]$Code) { if (-not $Value) { throw $Code } }
  function Reject-ProductTest([scriptblock]$Body, [string]$Code) {
    $failure = $null; try { & $Body } catch { $failure = $_ }
    Require-ProductTest ($null -ne $failure) ('accepted_' + $Code)
    $detail=$failure.Exception.ToString();$job=Find-JobFixtureFailure $failure
    if($null -ne $job){foreach($cause in $job.Causes){$detail+=$cause.ToString()}}
    Require-ProductTest ($detail.Contains($Code)) ('wrong_error_' + $Code)
  }
  function Get-InstalledSmokeGlobalState($Context) { return $state.Global }
  function Get-InstalledSmokeProcesses([string]$InstallPath) {
    if ($state.ProcessUnknown) { throw 'synthetic_process_unknown' }
    return @($state.ProcessRows)
  }
  function Get-ProductProcessRows([string]$InstallPath) { return @($state.ProcessRows) }
  function New-TestSetupCollision($Fixture, [string]$Kind) {
    $relative = switch ($Kind) {
      hook { '.config/arkism/hook-codex.cmd' }
      backup { '.config/arkism/backups/existing.fixture' }
      shortcut_name { 'Desktop/Ar'+[char]0x00ad+'kism.lnk' }
      default { throw 'test_collision_kind_invalid' }
    }
    $path=Join-Path $Fixture.Environment.HOME $relative
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path))|Out-Null
    if ($Kind -ceq 'shortcut_name') {
      [IO.File]::Copy((Join-Path $Fixture.Environment.HOME 'Desktop/Arkism.lnk'),$path)
    } else { [IO.File]::WriteAllText($path,'Inert setup-owned collision; must never be overwritten.') }
    $Fixture.SetupCollisionPath=$path
  }
  function New-ProductFixture([int]$SetupExtraFiles = 0, [string]$SetupCollision = '', [switch]$SkipSyntheticData) {
    $parent = Join-Path $suite ([guid]::NewGuid().ToString('N')); $root = Join-Path $parent 'owned'
    [IO.Directory]::CreateDirectory($root) | Out-Null
    $owner='a'*32; [IO.File]::WriteAllText((Join-Path $root '.installed-smoke-owner'),$owner)
    $context=@{OwnedRoots=@{};RunnerTemp=$parent;SourceSha=('b'*40)}; $context.OwnedRoots[$root]=$owner
    $fixture=@{Context=$context;Root=$root;Install=(Join-Path $root 'installed Arkism');Stage=(Join-Path $parent 'stage');
      Setup=(Join-Path $parent 'Arkism-0.1.0-setup.exe');Version='0.1.0';Environment=(New-InstalledSmokeEnvironment $root)}
    foreach($path in @($fixture.Stage,$fixture.Install,$fixture.Environment.HOME,$fixture.Environment.APPDATA,$fixture.Environment.LOCALAPPDATA,$fixture.Environment.TEMP)) {
      [IO.Directory]::CreateDirectory($path) | Out-Null
    }
    Assert-ProductProfileBeforeSetup $fixture
    $names=@('uninstall.exe','runtime/arkism-installer.exe','Arkism.exe','integration/arkism.exe','runtime/memoryd.exe','runtime/memory-principal.exe','runtime/roles.sql',
      'runtime/pgsql/bin/postgres.exe','runtime/pgsql/bin/pg_ctl.exe','runtime/pgsql/bin/initdb.exe','runtime/pgsql/bin/psql.exe',
      'runtime/pgsql/bin/pg_dump.exe','runtime/pgsql/bin/pg_restore.exe','runtime/pgsql/lib/vector.dll','runtime/pgsql/share/extension/vector.control')
    foreach($name in $names) {
      foreach($base in @($fixture.Stage,$fixture.Install)) {
        $path=Join-Path $base $name;[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path))|Out-Null
        [IO.File]::WriteAllText($path,'inert_'+$name)
      }
    }
    [IO.File]::WriteAllText($fixture.Setup,'inert setup; never executed')
    # Uninstaller bytes belong to the staged manifest and must match exactly.
    $fixture.SetupHash=Get-InstalledSmokeHash $fixture.Setup
    $fixture.UninstallerHash=Get-InstalledSmokeHash (Join-Path $fixture.Install 'uninstall.exe')
    $fixture.UninstallerBytes=[IO.File]::ReadAllBytes((Join-Path $fixture.Install 'uninstall.exe'))
    $fixture.Manifest=@(Get-InstalledSmokePayload $fixture.Stage)
    $values=@{DisplayName='Arkism';DisplayVersion='0.1.0';Publisher='Arkism contributors';DisplayIcon=(Join-Path $fixture.Install 'Arkism.exe');
      InstallLocation=$fixture.Install;UninstallString=('"'+(Join-Path $fixture.Install 'uninstall.exe')+'"');NoModify=1;NoRepair=1}
    $kinds=@{};foreach($key in $values.Keys){$kinds[$key]=if($key -in @('NoModify','NoRepair')){'DWord'}else{'String'}}
    $links = @(
      [pscustomobject]@{Path=(Join-Path $fixture.Environment.APPDATA 'Microsoft/Windows/Start Menu/Programs/Arkism.lnk');Role='Programs'},
      [pscustomobject]@{Path=(Join-Path $fixture.Environment.HOME 'Desktop/Arkism.lnk');Role='Desktop'})
    foreach ($link in $links) {
      [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($link.Path)) | Out-Null
      [IO.File]::WriteAllText($link.Path,'Inert shortcut text; never executed or opened as a shortcut.')
    }
    $fixture.Global=@{Registrations=@([pscustomobject]@{Hive='CurrentUser';View='Registry32';Values=$values;Kinds=$kinds;SubKeys=@()});
      Shortcuts=@($links | ForEach-Object {[pscustomobject]@{Target=$values.DisplayIcon;Arguments='';Role=$_.Role;Path=$_.Path;Scope='owned'}});
      Tasks=@();Autostart=@();Residual=@()}
    $fixture.ProcessRows=@()
    for ($index=0; $index -lt $SetupExtraFiles; $index++) {
      [IO.File]::WriteAllText((Join-Path $fixture.Environment.LOCALAPPDATA ("setup-owned-$index.fixture")),"Inert setup-owned file $index.")
    }
    if ($SetupCollision) { New-TestSetupCollision $fixture $SetupCollision }
    Initialize-ProductProfileBaseline $fixture
    if (-not $SkipSyntheticData) { New-ProductSyntheticData $fixture }
    return $fixture
  }
  function Invoke-TestProductCore($State,[scriptblock]$Mutation,[bool]$Hang=$false,[string]$Faults='') {
    $image=Join-Path $State.Install 'uninstall.exe'
    $State.PolicyPids=@()
    $checkedPolicy=New-ProductUninstallPolicy $State
    $policy=[Action[long,string]]{param($AllowedPid,$AllowedImage);$State.PolicyPids+=$AllowedPid;$checkedPolicy.Invoke($AllowedPid,$AllowedImage)}
    $change=[Action]{
      $State.ProcessRows=@([pscustomobject]@{ProcessId=2048;ExecutablePath=$image})
      if($Mutation){& $Mutation}
    }
    $identityPid=2048;$identityImage=$image
    if($State.ContainsKey('FakeIdentityPid')){$identityPid=$State.FakeIdentityPid}
    if($State.ContainsKey('FakeIdentityImage')){$identityImage=$State.FakeIdentityImage}
    try{return [Arkism.InstallerChecks.ProductTestHarness]::Run($policy,$change,$Hang,$Faults,$image,$identityPid,$identityImage)}
    finally{$State.ProcessRows=@()}
  }
  function Complete-TestProductRemoval($State) {
    foreach($row in $State.Manifest){[IO.File]::Delete((Join-Path $State.Install $row.Path))}
    [IO.File]::Delete((Join-Path $State.Install 'uninstall.exe'))
    $dir=Join-Path $State.Environment.HOME '.config/arkism/backups/synthetic-remove';[IO.Directory]::CreateDirectory($dir)|Out-Null
    $saved=Join-Path $dir '0';[IO.File]::Copy($State.Hook,$saved)
    $manifest=@{};$manifest[$State.Hook]=$saved
    [IO.File]::WriteAllText((Join-Path $dir 'manifest.json'),($manifest|ConvertTo-Json))
    [IO.File]::Delete($State.Hook)
    foreach ($path in $State.ProfileShortcuts) { [IO.File]::Delete($path) }
    $State.Global.Registrations=@();$State.Global.Shortcuts=@()
  }
  function Invoke-TestRemovalScenario([string]$Mode) {
    $scenarioState=New-ProductFixture
    $old=('old-opaque'+[char]0+[char]0)*4
    $d='new-opaque-directory'+[char]0+[char]0;$f='new-opaque-file'+[char]0+[char]0
    function New-ScenarioSnapshot([string]$Text) {
      return [Arkism.InstallerChecks.RebootObservation]::Parse($true,7,[Text.Encoding]::Unicode.GetBytes($Text))
    }
    $before=New-ScenarioSnapshot $old;$after=New-ScenarioSnapshot ($old+$d+$f)
    $directory=New-ScenarioSnapshot ($old+$d+$f+$d);$final=New-ScenarioSnapshot ($old+$d+$f+$d+$f)
    $productAfter=$after;$last=$final
    if($Mode -eq 'queue_extra'){$productAfter=$directory}
    if($Mode -eq 'queue_final_changed'){$last=New-ScenarioSnapshot ($old+$d+$f+$d+$f+$d)}
    $scenario=@{ProductReads=@($before,$productAfter,$last);ProductIndex=0;EchoReads=@($after,$directory,$directory,$final);EchoIndex=0;Calls=@()}
    function Get-ProductRebootSnapshot { $value=$scenario.ProductReads[$scenario.ProductIndex];$scenario.ProductIndex++;return $value }
    function Get-JobEchoSnapshot { $value=$scenario.EchoReads[$scenario.EchoIndex];$scenario.EchoIndex++;return $value }
    function Get-ProductCopyMachine { return 332 }
    function New-JobEchoExecutable { return [pscustomobject]@{Sha256='synthetic';PointerBytes=4} }
    function Invoke-JobEchoLeaf($Context,$Root,$Copy,$Leaf,$Environment,$Kind,$Report) { $scenario.Calls+=$Kind }
    function Invoke-ProductBoundRemoval($State) {
      $result=Invoke-TestProductCore $State $null
      Complete-TestProductRemoval $State
      if($Mode -eq 'missing_helper'){[IO.File]::WriteAllText($State.Hook,'undeleted')}
      $copy=Join-Path $State.Environment.TEMP 'new-copy/Un.exe';[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($copy))|Out-Null
      [IO.File]::WriteAllBytes($copy,$State.UninstallerBytes)
      return $result
    }
    $report=[ordered]@{};$failure=$null
    try{Invoke-ProductRemovalCheck $scenarioState @{} $report}catch{$failure=$_}
    if($Mode -eq 'normal') {
      Require-ProductTest ($null -eq $failure -and ($scenario.Calls -join ',') -ceq 'directory,file') 'scenario_normal_failed'
      Require-ProductTest ($report.helper_exit_code_observed -eq $false -and $report.new_product_rows -eq 2 -and $report.new_api_echo_rows -eq 2) 'scenario_claim_incorrect'
      Require-ProductTest ($report.profile_files_before_removal -eq 3 -and $report.owned_shortcut_files_before_removal -eq 2) 'scenario_profile_report_incorrect'
    }else{
      Require-ProductTest ($null -ne $failure) 'scenario_failure_accepted'
    }
    if($Mode -in @('queue_extra','missing_helper')){Require-ProductTest ($scenario.Calls.Count -eq 0) 'failed_scenario_reached_echo'}
    Require-ProductTest ([IO.File]::Exists((Join-Path $scenarioState.Root '.installed-smoke-owner'))) 'scenario_deleted_root'
  }
  function Invoke-ProductBindingCases {
    Test-ProductCase 'literal path rejects quote bracket wildcard and relative components' {
      [Arkism.InstallerChecks.ProductUninstallCheck]::RequireLiteralInstallPath('D:\owned\installed Arkism')
      foreach($bad in @("D:\owned'\app",'D:\owned[1]\app','D:\owned]\app','D:\owned*\app','D:\owned?\app','D:\owned\..\app','D:\owned.\app')) {
        Reject-ProductTest {[Arkism.InstallerChecks.ProductUninstallCheck]::RequireLiteralInstallPath($bad)} 'product_install_path_unsupported'
      }
    }
    $mutations=@{
      setup={ [IO.File]::AppendAllText($state.Setup,'changed') }
      stage={ [IO.File]::AppendAllText((Join-Path $state.Stage 'Arkism.exe'),'changed') }
      manifest={ [IO.File]::AppendAllText((Join-Path $state.Install 'Arkism.exe'),'changed') }
      image={ [IO.File]::AppendAllText((Join-Path $state.Install 'uninstall.exe'),'changed') }
      owner={ [IO.File]::WriteAllText((Join-Path $state.Root '.installed-smoke-owner'),'wrong') }
      profile={ [IO.File]::WriteAllText((Join-Path $state.Environment.HOME 'unexpected.txt'),'changed') }
      shortcut={ [IO.File]::AppendAllText($state.ProfileShortcuts[0],'changed') }
      environment={ $state.Environment.HOME=$suite }
      task={ $state.Global.Tasks=@('synthetic-preexisting-task') }
      registration={ $state.Global.Registrations[0].Values.InstallLocation=$suite }
      path={ $state.Install=Join-Path $suite 'other-install' }
    }
    foreach($kind in $mutations.Keys) {
      foreach($late in @($false,$true)) {
        Test-ProductCase ("actual product policy $kind late=$late keeps resume zero") {
          $state=New-ProductFixture
          $failure=$null
          try {
            if($late){[void](Invoke-TestProductCore $state $mutations[$kind])}
            else{& $mutations[$kind];[void](Invoke-TestProductCore $state $null)}
          }catch{$failure=$_}
          Require-ProductTest ($null -ne $failure) 'policy_violation_accepted'
          $api=[Arkism.InstallerChecks.ProductTestHarness]::Last
          Require-ProductTest ($api.ResumeCalls -eq 0) 'policy_failure_resumed'
          Require-ProductTest ($api.FactoryCalls -eq [int]$late) 'policy_created_adapter_too_early'
        }
      }
    }
  }

  function Invoke-ProductCoreCases {
    Test-ProductCase 'actual product core normal completion preserves owned files' {
      $state=New-ProductFixture;$result=Invoke-TestProductCore $state $null
      Require-ProductTest ($result.EmptyConfirmed -and [Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 1) 'normal_core_failed'
      Require-ProductTest (($state.PolicyPids -join ',') -ceq '0,2048') 'policy_pid_order_incorrect'
      Assert-ProductUninstallInputs $state
    }
    Test-ProductCase 'actual source identity guard fails before adapter creation' {
      $check=[Action]{[Arkism.InstallerChecks.ProductUninstallCheck]::RequireSourceIdentity(('a'*40),('b'*40))}
      Reject-ProductTest {[Arkism.InstallerChecks.ProductTestHarness]::BindingGate($check)} 'product_source_mismatch'
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.FactoryCalls -eq 0) 'source_mismatch_created_adapter'
    }
    Test-ProductCase 'actual literal path guard fails before adapter creation' {
      $check=[Action]{[Arkism.InstallerChecks.ProductUninstallCheck]::RequireLiteralInstallPath('D:\owned[1]\app')}
      Reject-ProductTest {[Arkism.InstallerChecks.ProductTestHarness]::BindingGate($check)} 'product_install_path_unsupported'
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.FactoryCalls -eq 0) 'unsafe_path_created_adapter'
    }
    Test-ProductCase 'actual PowerShell product entry constructs binding and rejects invalid source before native' {
      $state=New-ProductFixture;$state.Context.SourceSha='invalid'
      Reject-ProductTest {Invoke-ProductBoundRemoval $state} 'product_source_mismatch'
      Require-ProductTest ([IO.File]::Exists((Join-Path $state.Install 'uninstall.exe'))) 'entry_touched_program'
    }
    Test-ProductCase 'actual product core preserves timeout termination and close errors' {
      $state=New-ProductFixture
      $failure=$null;try{Invoke-TestProductCore $state $null $true 'terminate_job,close'}catch{$failure=$_}
      foreach($code in @('job_timeout','test_terminate_job','test_close')) {
        Require-ProductTest ($null -ne $failure -and $failure.Exception.ToString().Contains($code)) 'core_error_lost'
      }
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.Closed -eq 3) 'handles_not_closed'
    }
  }

  function Invoke-ProductRemovalCases {
    foreach($bad in @('program','key','backup','data','sentinel','process')) {
      Test-ProductCase ("actual removal postcondition rejects $bad") {
        $state=New-ProductFixture;Complete-TestProductRemoval $state
        switch($bad) {
          program {[IO.File]::WriteAllText((Join-Path $state.Install 'Arkism.exe'),'leftover')}
          key {$state.Global.Registrations=@('leftover')}
          backup {[IO.File]::WriteAllText((Join-Path $state.Environment.HOME '.config/arkism/backups/synthetic-remove/0'),'changed')}
          data {[IO.File]::AppendAllText((Join-Path $state.Root 'retained data/closed-memory.fixture'),'changed')}
          sentinel {[IO.File]::AppendAllText((Join-Path $state.Root 'unrelated.sentinel'),'changed')}
          process {$state.ProcessUnknown=$true}
        }
        $caught=$false;try{Assert-ProductRemoved $state ([pscustomobject]@{EmptyConfirmed=$true})}catch{$caught=$true}
        Require-ProductTest $caught 'bad_postcondition_accepted'
        Require-ProductTest ([IO.File]::Exists((Join-Path $state.Root '.installed-smoke-owner'))) 'root_deleted'
      }
    }
    Test-ProductCase 'actual removal postconditions accept required effects without asserting helper exit' {
      $state=New-ProductFixture;Complete-TestProductRemoval $state
      Assert-ProductRemoved $state ([pscustomobject]@{EmptyConfirmed=$true})
    }
    Test-ProductCase 'profile baseline rejects an unlisted file while preserving the owned files' {
      $state=New-ProductFixture
      [IO.File]::WriteAllText((Join-Path $state.Environment.HOME 'unlisted.txt'),'unlisted owned file')
      Reject-ProductTest {Set-ProductProfileBaseline $state} 'product_manifest_changed'
      Require-ProductTest ([IO.File]::Exists($state.Hook)) 'baseline_deleted_hook'
    }
    foreach ($bad in @('scope','path')) {
      Test-ProductCase ("profile baseline rejects non-owned shortcut $bad") {
        $state=New-ProductFixture
        if ($bad -eq 'scope') { $state.Global.Shortcuts[0].Scope='real' }
        else { $state.Global.Shortcuts[0].Path=$state.Setup }
        Reject-ProductTest {Set-ProductProfileBaseline $state} 'product_shortcut_profile_not_owned'
        Require-ProductTest ([IO.File]::Exists($state.Hook)) 'baseline_deleted_hook'
      }
    }
    Test-ProductCase 'copy observer requires exactly one new hash bound copy' {
      $state=New-ProductFixture;$temp=$state.Environment.TEMP;$before=@(Get-ProductTempInventory $temp)
      $dir=Join-Path $temp 'copy';[IO.Directory]::CreateDirectory($dir)|Out-Null
      [IO.File]::WriteAllBytes((Join-Path $dir 'Un.exe'),$state.UninstallerBytes)
      $after=@(Get-ProductTempInventory $temp)
      $copy=Get-ProductUninstallerCopy $before $after $temp $state.UninstallerHash
      Require-ProductTest ($copy.Sha256 -ceq $state.UninstallerHash) 'copy_hash_changed'
      Reject-ProductTest {Get-ProductUninstallerCopy $after $after $temp $state.UninstallerHash} 'product_copy_ambiguous'
      $extra=Join-Path $temp 'extra';[IO.Directory]::CreateDirectory($extra)|Out-Null
      [IO.File]::WriteAllText((Join-Path $extra 'Un.exe'),'wrong hash')
      Reject-ProductTest {Get-ProductUninstallerCopy $before @(Get-ProductTempInventory $temp) $temp $state.UninstallerHash} 'product_copy_ambiguous'
    }
    Test-ProductCase 'setup queue gate preserves opaque existing four and rejects extra row' {
      $row='*1\??\D:\synthetic\Un.exe'+[char]0+[char]0
      $bytes=[Text.Encoding]::Unicode.GetBytes($row*4)
      $before=[Arkism.InstallerChecks.RebootObservation]::Parse($true,7,$bytes)
      $same=[Arkism.InstallerChecks.RebootObservation]::Parse($true,7,$bytes)
      [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($before,$same)
      $changed=[Arkism.InstallerChecks.RebootObservation]::Parse($true,7,[Text.Encoding]::Unicode.GetBytes($row*5))
      Reject-ProductTest {[Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($before,$changed)} 'job_echo_final_changed'
    }
    Test-ProductCase 'product environment uses an explicit profile allowlist without inert modes' {
      $value=New-Object Arkism.InstallerChecks.ProductUninstallBinding('D:\owned','a','b','D:\owned\installed Arkism','D:\setup.exe','c','d')
      $block=[Arkism.InstallerChecks.ProductTestHarness]::Environment($value)
      $keys=@($block.Split([char]0)|Where-Object{$_}|ForEach-Object{$_.Split('=')[0]})
      $allowed=@('SystemRoot','WINDIR','SystemDrive','ComSpec','PATHEXT','PROCESSOR_ARCHITECTURE','NUMBER_OF_PROCESSORS','PATH','HOME','USERPROFILE','APPDATA','LOCALAPPDATA','XDG_CONFIG_HOME','TEMP','TMP')
      foreach($key in $keys){Require-ProductTest ($key -cin $allowed) 'unexpected_inherited_environment'}
      Require-ProductTest ('XDG_CONFIG_HOME' -cin $keys -and 'ARKISM_JOB_MODE' -cnotin $keys) 'wrong_product_environment'
    }
  }

  function Invoke-ProductCoordinatorCases {
    foreach($mode in @('normal','missing_helper','queue_extra','queue_final_changed')) {
      Test-ProductCase ("actual removal coordinator $mode keeps old opaque rows and roots") { Invoke-TestRemovalScenario $mode }
    }
    Test-ProductCase 'process policy initial zero permits no process and late exact identity permits one' {
      $state=New-ProductFixture;$image=Join-Path $state.Install 'uninstall.exe'
      Assert-ProductProcessState $state 0 ''
      $state.ProcessRows=@([pscustomobject]@{ProcessId=[uint32]2048;ExecutablePath=$image})
      Assert-ProductProcessState $state 2048 $image
    }
    Test-ProductCase 'old shared absence guard rejects suspended self while product late guard allows it' {
      $state=New-ProductFixture;$image=Join-Path $state.Install 'uninstall.exe'
      $state.ProcessRows=@([pscustomobject]@{ProcessId=2048;ExecutablePath=$image})
      Reject-ProductTest {Assert-InstalledSmokeNoProcesses $state.Install 'product_unexpected_process'} 'product_unexpected_process'
      Assert-ProductProcessState $state 2048 $image
    }
    Test-ProductCase 'actual process reader rejects unknown uninstall image using scoped CIM boundary' {
      $state=New-ProductFixture;$observedRows=@([pscustomobject]@{ProcessId=2048;Name='uninstall.exe';ExecutablePath=$null})
      function Invoke-ProductFakeCim { return $observedRows }
      Set-Alias -Name Get-CimInstance -Value Invoke-ProductFakeCim -Scope Local
      try { Reject-ProductTest {& $productionProcessReader $state.Install} 'product_process_image_unknown' }
      finally { Remove-Item Alias:Get-CimInstance }
    }
  }

  function Invoke-ProductProcessSetCases {
    foreach($bad in @('initial_process','wrong_pid','wrong_image','duplicate','extra','unknown_image','zero_row_pid','float_row_pid',
      'text_row_pid','late_missing','negative_allowance','zero_late_allowance','wrong_allowed_image')) {
      Test-ProductCase ("process policy rejects $bad") {
        $state=New-ProductFixture;$image=Join-Path $state.Install 'uninstall.exe';$allowedPid=2048;$allowedImage=$image
        $row=[pscustomobject]@{ProcessId=2048;ExecutablePath=$image};$state.ProcessRows=@($row)
        switch($bad) {
          initial_process {$allowedPid=0;$allowedImage=''}
          wrong_pid {$row.ProcessId=2049}
          wrong_image {$row.ExecutablePath=Join-Path $state.Install 'Arkism.exe'}
          duplicate {$state.ProcessRows=@($row,$row)}
          extra {$state.ProcessRows+=([pscustomobject]@{ProcessId=2049;ExecutablePath=(Join-Path $state.Install 'Arkism.exe')})}
          unknown_image {$row.ExecutablePath=$null}
          zero_row_pid {$row.ProcessId=0}
          float_row_pid {$row.ProcessId=2048.0}
          text_row_pid {$row.ProcessId='2048'}
          late_missing {$state.ProcessRows=@()}
          negative_allowance {$allowedPid=-1}
          zero_late_allowance {$allowedPid=0}
          wrong_allowed_image {$allowedImage=Join-Path $state.Install 'Arkism.exe'}
        }
        $failure=$null;try{Assert-ProductProcessState $state $allowedPid $allowedImage}catch{$failure=$_}
        Require-ProductTest ($null -ne $failure) 'invalid_process_set_accepted'
      }
    }
  }

  function Invoke-ProductLateProcessCases {
    foreach($bad in @('wrong_pid','wrong_image','duplicate','unknown_image')) {
      Test-ProductCase ("actual late process policy $bad keeps resume zero") {
        $state=New-ProductFixture
        $change={
          switch($bad) {
            wrong_pid {$state.ProcessRows[0].ProcessId=2049}
            wrong_image {$state.ProcessRows[0].ExecutablePath=Join-Path $state.Install 'Arkism.exe'}
            duplicate {$state.ProcessRows+= $state.ProcessRows[0]}
            unknown_image {$state.ProcessRows[0].ExecutablePath=$null}
          }
        }
        Reject-ProductTest {Invoke-TestProductCore $state $change} 'product_process_set_invalid'
        Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0) 'invalid_late_set_resumed'
      }
    }
  }

  function Invoke-ProductIdentityCases {
    foreach($bad in @('zero','negative','image')) {
      Test-ProductCase ("actual native identity boundary $bad rejects before late policy") {
        $state=New-ProductFixture
        switch($bad) {
          zero {$state.FakeIdentityPid=0}
          negative {$state.FakeIdentityPid=-1}
          image {$state.FakeIdentityImage=Join-Path $state.Install 'Arkism.exe'}
        }
        Reject-ProductTest {Invoke-TestProductCore $state $null} 'product_process_identity_invalid'
        Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0 -and
          ($state.PolicyPids -join ',') -ceq '0') 'invalid_identity_reached_late_policy'
      }
    }
  }

  function Invoke-ProductDiagnosticCases {
    Test-ProductCase 'job report preserves fixed phase state counts and excludes unknown strings' {
      $state=New-ProductFixture;$result=Invoke-TestProductCore $state $null;$report=[ordered]@{}
      Add-ProductJobResult $report $result
      Require-ProductTest ($report.job_phase -ceq 'complete' -and $report.job_resume_state -ceq 'resumed' -and
        $report.job_may_have_run -and $report.job_empty -and $report.job_total_processes -eq 4) 'job_report_incorrect'
      $result.Phase='unsupported-private-text';$result.ResumeState='unsupported-private-text'
      Add-ProductJobResult $report $result
      Require-ProductTest ($report.job_phase -ceq 'unknown' -and $report.job_resume_state -ceq 'unknown') 'unsafe_diagnostic_text'
      $failure=$null
      try { Invoke-TestProductCore $state $null $false 'resume' } catch { $failure=Find-JobFixtureFailure $_ }
      Require-ProductTest ($null -ne $failure) 'resume_failure_missing'
      Add-ProductJobResult $report $failure.Result
      Require-ProductTest ($report.job_phase -ceq 'resume' -and $report.job_resume_state -ceq 'attempted' -and
        $report.job_may_have_run) 'resume_attempt_diagnostic_lost'
    }
    Test-ProductCase 'actual retention refuses ownership change and preserves primary cleanup evidence errors' {
      $state=New-ProductFixture;$report=[ordered]@{ok=$true}
      [IO.File]::WriteAllText((Join-Path $state.Root '.installed-smoke-owner'),'wrong')
      $cleanup=$null;try{Set-ProductRetention $state $report}catch{$cleanup=$_}
      Require-ProductTest ($null -ne $cleanup -and -not $report.ok -and $report.cleanup -ceq 'unconfirmed_root_retained') 'retention_claimed_success'
      $primary=$null;try{throw 'synthetic_primary'}catch{$primary=$_}
      $evidence=$null;try{throw 'synthetic_evidence'}catch{$evidence=$_}
      $failure=$null;try{Assert-InstalledSmokeOutcome $primary $cleanup $evidence}catch{$failure=$_}
      foreach($code in @('synthetic_primary','smoke_root_not_owned','synthetic_evidence')) {
        Require-ProductTest ($null -ne $failure -and $failure.Exception.Message.Contains($code)) 'outcome_error_lost'
      }
      Require-ProductTest ([IO.Directory]::Exists($state.Root)) 'retention_deleted_root'
    }
  }

  function Invoke-ProductPolicyDiagnosticCases {
    Test-ProductCase 'policy trace initial failure preserves the failed and last completed guards' {
      $state=New-ProductFixture
      [IO.File]::WriteAllText((Join-Path $state.Root 'unrelated.sentinel'),'changed inert sentinel')
      Reject-ProductTest {Invoke-TestProductCore $state $null} 'product_sentinel_changed'
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($report.policy_context -ceq 'initial' -and $report.policy_callback_count -eq 1 -and
        $report.policy_current_stage -ceq 'sentinel' -and $report.policy_completed_stage -ceq 'retained_data' -and
        -not $report.policy_process_observed -and -not $report.policy_completed) 'initial_policy_trace_incorrect'
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.FactoryCalls -eq 0) 'initial_policy_reached_native_adapter'
    }
    Test-ProductCase 'policy trace late global failure proves callback entry without allowing resume' {
      $state=New-ProductFixture
      Reject-ProductTest {Invoke-TestProductCore $state {$state.Global.Tasks=@('synthetic task')}} 'smoke_global_state_changed'
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($report.policy_context -ceq 'late' -and $report.policy_callback_count -eq 2 -and
        $report.policy_current_stage -ceq 'registration' -and $report.policy_completed_stage -ceq 'global_read' -and
        -not $report.policy_process_observed -and -not $report.policy_completed) 'late_policy_trace_incorrect'
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0) 'late_policy_failure_resumed'
    }
    Test-ProductCase 'policy trace process rejection records completed observation without verified identity' {
      $state=New-ProductFixture
      Reject-ProductTest {Invoke-TestProductCore $state {$state.ProcessRows+=$state.ProcessRows[0]}} 'product_process_set_invalid'
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($report.policy_context -ceq 'late' -and $report.policy_callback_count -eq 2 -and
        $report.policy_current_stage -ceq 'process_validate' -and $report.policy_completed_stage -ceq 'process_scan' -and
        $report.policy_process_observed -and $report.policy_process_count -eq 2 -and -not $report.policy_process_verified) 'process_policy_trace_incorrect'
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0) 'rejected_process_trace_resumed'
    }
    Test-ProductCase 'policy trace failed process query cannot reuse the earlier initial observation' {
      $state=New-ProductFixture
      function Get-ProductProcessRows([string]$InstallPath) {
        if ($state.ProcessRows.Count) { throw 'synthetic_process_query_failed' }
        return @()
      }
      Reject-ProductTest {Invoke-TestProductCore $state $null} 'synthetic_process_query_failed'
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($report.policy_callback_count -eq 2 -and $report.policy_current_stage -ceq 'process_scan' -and
        $report.policy_completed_stage -ceq 'shortcuts' -and -not $report.policy_process_observed -and
        $report.policy_process_count -eq 0 -and -not $report.policy_process_verified) 'incomplete_process_scan_claimed_observed'
    }
    Test-ProductCase 'policy trace success exports only fixed scalars after both actual callbacks' {
      $state=New-ProductFixture;$null=Invoke-TestProductCore $state $null
      $state.PolicyTrace.Raw=$state.Root
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($report.Count -eq 8 -and $report.policy_context -ceq 'late' -and $report.policy_callback_count -eq 2 -and
        $report.policy_current_stage -ceq 'complete' -and $report.policy_completed_stage -ceq 'complete' -and
        $report.policy_process_observed -and $report.policy_process_count -eq 1 -and
        $report.policy_process_verified -and $report.policy_completed) 'complete_policy_trace_incorrect'
      Require-ProductTest (-not ($report|ConvertTo-Json -Compress).Contains($state.Root)) 'policy_trace_exposed_unknown_field'
    }
  }

  function Invoke-ProductPolicyMalformedCases {
    Test-ProductCase 'policy trace rejects non scalar labels out of range counters and malformed booleans' {
      $state=New-ProductFixture;$null=Invoke-TestProductCore $state $null
      $convertible=[pscustomobject]@{Label='late';Raw=$state.Root}
      $convertible|Add-Member -MemberType ScriptMethod -Name ToString -Value {$this.Label} -Force
      $invalid=@(@{Key='Context';Value=$convertible},@{Key='Context';Value=('la'+[char]0x00ad+'te')},
        @{Key='CurrentStage';Value='unknown-unreported-content'},@{Key='CompletedStage';Value=$convertible},
        @{Key='CallbackCount';Value=3},@{Key='CallbackCount';Value='2'},@{Key='CallbackCount';Value=[long]2},
        @{Key='ProcessCount';Value=-1},@{Key='ProcessCount';Value=1000001},@{Key='ProcessCount';Value=1.0},
        @{Key='ProcessObserved';Value=1},@{Key='ProcessVerified';Value='true'},@{Key='Completed';Value=$null})
      foreach($entry in $invalid) {
        $saved=$state.PolicyTrace[$entry.Key];$state.PolicyTrace[$entry.Key]=$entry.Value
        $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
        Require-ProductTest ($report.Count -eq 0) 'malformed_policy_trace_copied'
        $state.PolicyTrace[$entry.Key]=$saved
      }
      $state.PolicyTrace.CallbackCount='unknown-unreported-content'
      [IO.File]::WriteAllText((Join-Path $state.Root 'unrelated.sentinel'),'changed inert sentinel')
      Reject-ProductTest {Invoke-ProductUninstallPolicy $state 0 ''} 'product_sentinel_changed'
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($report.Count -eq 0) 'malformed_policy_trace_masked_guard'
    }
  }

  function Invoke-ProductPolicyBindingCases {
    Test-ProductCase 'policy factory keeps its original state and caller scoped effect readers' {
      $first=New-ProductFixture
      $policy=New-ProductUninstallPolicy $first
      $state=New-ProductFixture
      $observations=@{Global=0;Processes=0}
      function Get-InstalledSmokeGlobalState($Context) {
        Require-ProductTest ([object]::ReferenceEquals($Context,$first.Context)) 'policy_switched_global_context'
        $observations.Global++;return $first.Global
      }
      function Get-ProductProcessRows([string]$InstallPath) {
        Require-ProductTest ($InstallPath -ceq $first.Install) 'policy_switched_process_context'
        $observations.Processes++;return @($first.ProcessRows)
      }
      $image=Join-Path $first.Install 'uninstall.exe'
      $change=[Action]{$first.ProcessRows=@([pscustomobject]@{ProcessId=2048;ExecutablePath=$image})}
      $result=[Arkism.InstallerChecks.ProductTestHarness]::Run($policy,$change,$false,'',$image,2048,$image)
      Require-ProductTest ($result.EmptyConfirmed -and $first['PolicyTrace']['CallbackCount'] -eq 2 -and
        -not $state.ContainsKey('PolicyTrace') -and $observations.Global -eq 2 -and $observations.Processes -eq 2) 'policy_factory_binding_changed'
    }
    Test-ProductCase 'policy trace uses indexed entries despite extended property shadows on every field' {
      $state=New-ProductFixture;$null=Invoke-TestProductCore $state $null
      $trace=$state['PolicyTrace'];$expected=[ordered]@{};Add-ProductPolicyDiagnostic $expected $state
      $unreported=[pscustomobject]@{Raw='synthetic-unreported-content'}
      $unreported|Add-Member -MemberType ScriptMethod -Name ToString -Value {'synthetic-unreported-content'} -Force
      foreach($key in @('Context','CallbackCount','CurrentStage','CompletedStage','ProcessObserved','ProcessCount','ProcessVerified','Completed')) {
        $trace|Add-Member -MemberType NoteProperty -Name $key -Value $unreported -Force
      }
      $state|Add-Member -MemberType NoteProperty -Name PolicyTrace -Value $unreported -Force
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest (($report|ConvertTo-Json -Compress) -ceq ($expected|ConvertTo-Json -Compress)) 'extended_policy_property_serialized'
      Require-ProductTest (-not ($report|ConvertTo-Json -Compress).Contains('synthetic-unreported-content')) 'extended_policy_content_exposed'
      $null=New-ProductUninstallPolicy $state
      Require-ProductTest ($state['PolicyTrace'] -is [hashtable] -and $state['PolicyTrace']['CallbackCount'] -eq 0) 'extended_state_shadow_replaced_indexed_factory'
    }
  }

  function Get-ProductScanTestRows($State,[string]$Mode) {
    if (-not $State.ProcessRows.Count) { return @() }
    $row=$State.ProcessRows[0]
    switch($Mode) {
      query { throw 'synthetic_query_failure' }
      partial_query { $row;throw 'synthetic_partial_query_failure' }
      unknown_image { $row.ProcessId=2049;$row.ExecutablePath=$null;$row|Add-Member NoteProperty Name 'uninstall.exe' -Force }
      array_image { $row.ExecutablePath=@([string][char]0,'inert') }
      invalid_path { $row.ExecutablePath=[string][char]0 }
    }
    return $row
  }

  function Invoke-ProductScanDiagnosticCase([string]$Mode) {
    $state=New-ProductFixture
    function Get-ProductProcessRows([string]$InstallPath,$ScanState,$ExpectedPid,$NativeImage) {
      & $productionProcessReader $InstallPath $ScanState $ExpectedPid $NativeImage
    }
    function Invoke-ProductScanFakeCim { Get-ProductScanTestRows $state $Mode }
    Set-Alias Get-CimInstance Invoke-ProductScanFakeCim -Scope Local
    $failure=$null;$result=$null
    try { $result=Invoke-TestProductCore $state $null } catch { $failure=$_ }
    finally { Remove-Item Alias:Get-CimInstance }
    $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
    if ($Mode -ceq 'success') {
      Require-ProductTest ($null -eq $failure -and $result.EmptyConfirmed -and $report.policy_completed -and
        $report.policy_scan_stage -ceq 'complete' -and $report.policy_scan_query_returned -and
        $report.policy_scan_query_rows -eq 1 -and $report.policy_scan_rows_visited -eq 1 -and
        $report.policy_scan_image_kind -ceq 'string' -and $report.policy_scan_pid_relation -ceq 'expected_pid') 'scan_success_trace_incorrect'
      return
    }
    Require-ProductTest ($null -ne $failure -and $report.policy_callback_count -eq 2 -and
      $report.policy_current_stage -ceq 'process_scan' -and -not $report.policy_process_observed -and
      [Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0) 'scan_failure_guard_changed'
    $expected=@{query=@{Stage='query';Queried=$false;Kind='unobserved'}
      partial_query=@{Stage='query';Queried=$false;Kind='unobserved'}
      unknown_image=@{Stage='unknown_image';Queried=$true;Kind='null'}
      array_image=@{Stage='image_check';Queried=$true;Kind='array'}
      invalid_path=@{Stage='path_normalize';Queried=$true;Kind='string'}}[$Mode]
    Require-ProductTest ($report.policy_scan_stage -ceq $expected.Stage) 'scan_failure_substage_missing'
    $queried=$expected.Queried
    Require-ProductTest ($report.policy_scan_query_returned -eq $queried -and
      $report.policy_scan_query_rows -eq [int]$queried -and $report.policy_scan_rows_visited -eq [int]$queried) 'partial_scan_claimed_complete'
    Require-ProductTest ($report.policy_scan_image_kind -ceq $expected.Kind) 'scan_value_kind_incorrect'
    if ($Mode -ceq 'unknown_image') {
      Require-ProductTest ($report.policy_scan_name_kind -ceq 'string' -and
        $report.policy_scan_pid_relation -ceq 'other_pid') 'unknown_foreign_scan_relation_missing'
    }
  }

  function Invoke-ProductScanDiagnosticCases {
    foreach($mode in @('query','partial_query','unknown_image','array_image','invalid_path','success')) {
      Test-ProductCase ("actual CIM reader scan diagnostic $mode preserves guarded policy") { Invoke-ProductScanDiagnosticCase $mode }
    }
    Test-ProductCase 'scan diagnostics use indexed scalar allowlists without extended property text' {
      $state=New-ProductFixture;$null=New-ProductUninstallPolicy $state
      $scan=@{Stage='complete';QueryReturned=$true;QueryRows=1;RowsVisited=1;ImageKind='string';NameKind='unobserved';PidRelation='expected_pid'}
      $state['ProcessScanTrace']=$scan
      $expected=[ordered]@{};Add-ProductPolicyDiagnostic $expected $state
      foreach($name in @('Stage','QueryReturned','QueryRows','RowsVisited','ImageKind','NameKind','PidRelation')) {
        $scan|Add-Member NoteProperty $name 'synthetic-unreported-content' -Force
      }
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($report.Count -eq 15 -and ($report|ConvertTo-Json -Compress) -ceq
        ($expected|ConvertTo-Json -Compress)) 'scan_diagnostic_shadow_copied'
      foreach($entry in @(@{Key='Stage';Value='unknown-unreported-content'},@{Key='ImageKind';Value=@('string')},
        @{Key='QueryRows';Value='1'},@{Key='RowsVisited';Value=-1},@{Key='QueryReturned';Value=1},
        @{Key='PidRelation';Value=('expected_'+[char]0x00ad+'pid')})) {
        $saved=$scan[$entry.Key];$scan[$entry.Key]=$entry.Value
        $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
        Require-ProductTest ($report.Count -eq 8) 'malformed_scan_diagnostic_copied'
        $scan[$entry.Key]=$saved
      }
    }
  }

  function Invoke-ProductNativeCimCore($Fixture, [scriptblock]$Mutation, [scriptblock]$AfterScan) {
    function Get-ProductProcessRows([string]$InstallPath, $ScanState, $ExpectedPid, $NativeImage) {
      $observed=@(& $productionProcessReader $InstallPath $ScanState $ExpectedPid $NativeImage)
      if ($Fixture.ProcessRows.Count -and $AfterScan) { & $AfterScan }
      return $observed
    }
    function Invoke-ProductNativeFakeCim {
      $Fixture.ObservedCimRows=@($Fixture.ProcessRows)
      return @($Fixture.ProcessRows)
    }
    Set-Alias Get-CimInstance Invoke-ProductNativeFakeCim -Scope Local
    try { Invoke-TestProductCore $Fixture $Mutation }
    finally { Remove-Item Alias:Get-CimInstance }
  }

  function Invoke-ProductNativeCimCases {
    Test-ProductCase 'native verified own null CIM image keeps the observed row and resumes once' {
      $state=New-ProductFixture
      $result=Invoke-ProductNativeCimCore $state {
        $state.ProcessRows[0].ExecutablePath=$null
        $state.ProcessRows[0]|Add-Member NoteProperty Name 'uninstall.exe' -Force
      }
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest ($result.EmptyConfirmed -and
        [Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 1 -and
        $state.ObservedCimRows.Count -eq 1 -and $null -eq $state.ObservedCimRows[0].ExecutablePath -and
        $state.ObservedCimRows[0].Name -ceq 'uninstall.exe' -and $report.policy_completed -and
        $report.policy_native_image_relied_on -eq $true -and $report.policy_cim_image_unavailable -eq $true -and
        $report.policy_process_image_source -ceq 'native_verified_cim_unavailable') 'native_own_null_cim_not_bound'
    }
    foreach($mode in @('wrong_pid','text_pid','float_pid','bool_pid','zero_pid','overflow_pid','wrong_name','array_name',
      'empty_image','whitespace_image','wrong_image','outside_image','array_image','duplicate','extra','other_unknown')) {
      Test-ProductCase ("native CIM identity rejects $mode before resume") {
        $state=New-ProductFixture
        $failure=$null
        try { $null=Invoke-ProductNativeCimCore $state { Set-ProductNativeCimTestRows $state $mode } }
        catch { $failure=$_ }
        Require-ProductTest ($null -ne $failure -and
          [Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0) 'invalid_native_cim_identity_resumed'
        if ($mode -ceq 'array_image') { Assert-ProductNativeCimScalarChanges }
      }
    }
    Test-ProductCase 'native CIM initial policy allows no unknown image exception' {
      $state=New-ProductFixture
      $state.ProcessRows=@([pscustomobject]@{ProcessId=2048;Name='uninstall.exe';ExecutablePath=$null})
      Reject-ProductTest {Invoke-ProductNativeCimCore $state $null} 'product_process_image_unknown'
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0 -and
        [Arkism.InstallerChecks.ProductTestHarness]::Last.FactoryCalls -eq 0) 'initial_unknown_cim_reached_adapter'
    }
    Test-ProductCase 'native CIM null row cannot bypass invalid verified native image' {
      $state=New-ProductFixture;$state.FakeIdentityImage=Join-Path $state.Install 'Arkism.exe'
      Reject-ProductTest {Invoke-ProductNativeCimCore $state { Set-ProductNativeCimTestRows $state 'own_null' }} 'product_process_identity_invalid'
      Require-ProductTest ([Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0 -and
        ($state.PolicyPids -join ',') -ceq '0') 'invalid_native_image_reached_late_cim'
    }
    Invoke-ProductNativeCimAllowanceCases
    Invoke-ProductNativeCimDiagnosticCases
  }

  function Set-ProductNativeCimTestRows($Fixture, [string]$Mode) {
    $row=$Fixture.ProcessRows[0];$row.ExecutablePath=$null
    $row|Add-Member NoteProperty Name 'uninstall.exe' -Force
    $changes=@{
      own_null={};wrong_pid={$row.ProcessId=2049};text_pid={$row.ProcessId='2048'}
      float_pid={$row.ProcessId=2048.0};bool_pid={$row.ProcessId=$true};zero_pid={$row.ProcessId=0}
      overflow_pid={$row.ProcessId=[long][uint32]::MaxValue+1};wrong_name={$row.Name='memoryd.exe'}
      array_name={$row.Name=@('uninstall.exe')};empty_image={$row.ExecutablePath=''}
      whitespace_image={$row.ExecutablePath=' '};wrong_image={$row.ExecutablePath=Join-Path $Fixture.Install 'Arkism.exe'}
      outside_image={$row.ExecutablePath=Join-Path $Fixture.Root 'uninstall.exe'}
      array_image={$row.ExecutablePath=@((Join-Path $Fixture.Install 'uninstall.exe'))}
      duplicate={$Fixture.ProcessRows+= $row}
      extra={$Fixture.ProcessRows+=([pscustomobject]@{ProcessId=2049;Name='Arkism.exe';ExecutablePath=(Join-Path $Fixture.Install 'Arkism.exe')})}
      other_unknown={$Fixture.ProcessRows+=([pscustomobject]@{ProcessId=2049;Name='memoryd.exe';ExecutablePath=$null})}
    }
    if (-not $changes.ContainsKey($Mode)) { throw 'native_cim_test_mode_invalid' }
    & $changes[$Mode]
  }

  function New-ProductCimConvertible([string]$Image) {
    $value=[pscustomobject]@{Value=$Image}
    $value|Add-Member ScriptMethod ToString {$this.Value} -Force
    return $value
  }

  function Assert-ProductNativeCimScalarChanges {
    foreach($variant in @('object_before_scan','array_after_scan','object_after_scan')) {
      $state=New-ProductFixture;$image=Join-Path $state.Install 'uninstall.exe'
      $change={ $state.ProcessRows[0].ExecutablePath=New-ProductCimConvertible $image }
      $after=$null
      if ($variant -cne 'object_before_scan') {
        $change=$null
        $after={
          $state.ProcessRows[0].ExecutablePath=if ($variant -ceq 'array_after_scan') { ,@($image) }
            else { New-ProductCimConvertible $image }
        }
      }
      $failure=$null
      try { $null=Invoke-ProductNativeCimCore $state $change $after } catch { $failure=$_ }
      Require-ProductTest ($null -ne $failure -and
        [Arkism.InstallerChecks.ProductTestHarness]::Last.ResumeCalls -eq 0) 'non_scalar_cim_image_resumed'
    }
  }

  function Invoke-ProductNativeCimAllowanceCases {
    Test-ProductCase 'native CIM allowance validates scalar native arguments before querying' {
      $state=New-ProductFixture;$image=Join-Path $state.Install 'uninstall.exe';$calls=@{Count=0}
      function Invoke-ProductAllowanceFakeCim { $calls.Count++;return @() }
      function Get-ProductProcessRows([string]$InstallPath,$ScanState,$ExpectedPid,$NativeImage) {
        & $productionProcessReader $InstallPath $ScanState $ExpectedPid $NativeImage
      }
      Set-Alias Get-CimInstance Invoke-ProductAllowanceFakeCim -Scope Local
      try {
        foreach($rowPid in @($null,'2048',2048.0,$true,-1,([long][uint32]::MaxValue+1))) {
          Reject-ProductTest {Assert-ProductProcessState $state $rowPid $image} 'product_process_allowance_invalid'
        }
        $convertible=[pscustomobject]@{Value=$image}
        $convertible|Add-Member ScriptMethod ToString {$this.Value} -Force
        foreach($invalid in @($null,@($image),$convertible,(Join-Path $state.Install 'Arkism.exe'))) {
          Reject-ProductTest {Assert-ProductProcessState $state 2048 $invalid} 'product_process_allowance_invalid'
        }
      } finally { Remove-Item Alias:Get-CimInstance }
      Require-ProductTest ($calls.Count -eq 0) 'invalid_native_allowance_queried_cim'
    }
  }

  function Invoke-ProductNativeCimDiagnosticCases {
    Test-ProductCase 'native CIM fallback diagnostics use indexed fixed values and never expose extended text' {
      $state=New-ProductFixture
      $null=Invoke-ProductNativeCimCore $state { Set-ProductNativeCimTestRows $state 'own_null' }
      $identity=$state['ProcessIdentityTrace'];$expected=[ordered]@{};Add-ProductPolicyDiagnostic $expected $state
      foreach($name in @('NativeImageReliedOn','CimImageUnavailable','Source')) {
        $identity|Add-Member NoteProperty $name 'synthetic-unreported-content' -Force
      }
      $state|Add-Member NoteProperty ProcessIdentityTrace 'synthetic-unreported-content' -Force
      $report=[ordered]@{};Add-ProductPolicyDiagnostic $report $state
      Require-ProductTest (($report|ConvertTo-Json -Compress) -ceq ($expected|ConvertTo-Json -Compress) -and
        $report.Count -eq 18) 'native_cim_diagnostic_shadow_copied'
      foreach($entry in @(@{Key='NativeImageReliedOn';Value=1},@{Key='CimImageUnavailable';Value=$false},
        @{Key='Source';Value=@('native_verified_cim_unavailable')},@{Key='Source';Value='synthetic-unreported-content'})) {
        $saved=$identity[$entry.Key];$identity[$entry.Key]=$entry.Value;$report=[ordered]@{}
        Add-ProductPolicyDiagnostic $report $state
        Require-ProductTest ($report.Count -eq 15) 'invalid_native_cim_diagnostic_copied'
        $identity[$entry.Key]=$saved
      }
      Require-ProductTest (-not ($expected|ConvertTo-Json -Compress).Contains($state.Root)) 'native_cim_diagnostic_exposed_path'
    }
  }

  function Invoke-ProductManifestDiagnosticCases {
    Test-ProductCase 'manifest diagnostics identify initial profile counts without file paths or contents' {
      $state=New-ProductFixture;$report=[ordered]@{};$failure=$null
      $extra=Join-Path $state.Environment.HOME 'manifest-extra.fixture'
      [IO.File]::WriteAllText($extra,'synthetic-unreported-content')
      try{Set-ProductProfileBaseline $state}catch{$failure=$_}
      Require-ProductTest ($null -ne $failure) 'extra_profile_accepted'
      Add-ProductManifestDiagnostic $report $failure
      Require-ProductTest ($report.manifest_check -ceq 'profile_initial' -and $report.manifest_mismatch_kind -ceq 'count' -and
        $report.manifest_expected_files -eq 3 -and $report.manifest_actual_files -eq 4 -and $report.manifest_first_index -eq -1) 'initial_manifest_diagnostic_incorrect'
      $json=$report|ConvertTo-Json -Compress
      Require-ProductTest ($report.Count -eq 7 -and -not $json.Contains($state.Root) -and
        -not $json.Contains('manifest-extra.fixture') -and -not $json.Contains('synthetic-unreported-content')) 'manifest_diagnostic_exposed_file'
    }
    Test-ProductCase 'manifest diagnostics identify changed profile bytes while exact comparison still rejects' {
      $state=New-ProductFixture;$report=[ordered]@{};$failure=$null
      [IO.File]::WriteAllText($state.Hook,'changed-inert-hook')
      try{Assert-ProductUninstallInputs $state}catch{$failure=$_}
      Require-ProductTest ($null -ne $failure) 'changed_profile_accepted'
      Add-ProductManifestDiagnostic $report $failure
      Require-ProductTest ($report.manifest_check -ceq 'profile_before_removal' -and $report.manifest_mismatch_kind -ceq 'hash' -and
        $report.manifest_expected_files -eq 3 -and $report.manifest_actual_files -eq 3 -and
        $report.manifest_path_case_equal -and $report.manifest_path_separator_equal) 'hash_manifest_diagnostic_incorrect'
    }
    Test-ProductCase 'manifest diagnostics describe path casing without accepting a changed staged path' {
      $state=New-ProductFixture;$report=[ordered]@{};$failure=$null
      $state.Manifest[0].Path=$state.Manifest[0].Path.ToUpperInvariant()
      try{Assert-ProductUninstallInputs $state}catch{$failure=$_}
      Require-ProductTest ($null -ne $failure) 'changed_manifest_path_accepted'
      Add-ProductManifestDiagnostic $report $failure
      Require-ProductTest ($report.manifest_check -ceq 'stage_payload' -and $report.manifest_mismatch_kind -ceq 'path' -and
        $report.manifest_path_case_equal -and -not $report.manifest_path_separator_equal) 'path_manifest_diagnostic_incorrect'
    }
    Test-ProductCase 'manifest diagnostics discard unknown labels and malformed values' {
      $exception=New-ProductManifestFailure @() @([pscustomobject]@{Path='synthetic';Sha256='synthetic'}) 'profile_initial'
      $diagnostic=$exception.Data['ArkismProductManifest'];$diagnostic.Raw='synthetic-unreported-content'
      $record=[pscustomobject]@{Exception=$exception};$report=[ordered]@{}
      Add-ProductManifestDiagnostic $report $record
      Require-ProductTest ($report.Count -eq 7 -and -not ($report|ConvertTo-Json -Compress).Contains('synthetic-unreported-content')) 'unknown_manifest_field_copied'
      foreach($invalid in @(@{Key='Check';Value='synthetic-unreported-content'},@{Key='Kind';Value='synthetic-unreported-content'},
        @{Key='ExpectedFiles';Value='1'},@{Key='PathCaseEqual';Value=1})) {
        $old=$diagnostic[$invalid.Key];$diagnostic[$invalid.Key]=$invalid.Value;$report=[ordered]@{}
        Add-ProductManifestDiagnostic $report $record
        Require-ProductTest ($report.Count -eq 0) 'invalid_manifest_diagnostic_copied'
        $diagnostic[$invalid.Key]=$old
      }
    }
  }

  function Invoke-ProductManifestMalformedCases {
    Test-ProductCase 'manifest diagnostic indexed fields exclude all extended property shadows' {
      $expectedRows=@([pscustomobject]@{Path='first';Sha256='first'})
      $actualRows=@([pscustomobject]@{Path='second';Sha256='second'})
      $exception=New-ProductManifestFailure $expectedRows $actualRows 'profile_initial' 0
      $record=[pscustomobject]@{Exception=$exception};$expected=[ordered]@{}
      Add-ProductManifestDiagnostic $expected $record
      $diagnostic=$exception.Data['ArkismProductManifest']
      foreach($field in @('Check','Kind','ExpectedFiles','ActualFiles','FirstIndex','PathCaseEqual','PathSeparatorEqual')) {
        $shadow=switch($field){Check{'profile_before_removal'} Kind{'count'} default{'synthetic-unreported-content'}}
        $diagnostic|Add-Member -MemberType NoteProperty -Name $field -Value $shadow -Force
      }
      $report=[ordered]@{};Add-ProductManifestDiagnostic $report $record
      Require-ProductTest ($expected.Count -eq 7 -and
        ($report|ConvertTo-Json -Compress) -ceq ($expected|ConvertTo-Json -Compress)) 'extended_manifest_property_serialized'
      Require-ProductTest (-not ($report|ConvertTo-Json -Compress).Contains('synthetic-unreported-content')) 'extended_manifest_content_exposed'
    }
    Test-ProductCase 'manifest diagnostic labels reject string convertible objects without copying properties' {
      foreach($entry in @(@{Key='Check';Label='profile_initial'},@{Key='Kind';Label='count'})) {
        $exception=New-ProductManifestFailure @() @([pscustomobject]@{Path='synthetic';Sha256='synthetic'}) 'profile_initial'
        $value=[pscustomobject]@{Label=$entry.Label;Unreported='synthetic-unreported-content'}
        $value|Add-Member -MemberType ScriptMethod -Name ToString -Value {$this.Label} -Force
        $exception.Data['ArkismProductManifest'][$entry.Key]=$value
        $report=[ordered]@{};Add-ProductManifestDiagnostic $report ([pscustomobject]@{Exception=$exception})
        Require-ProductTest ($report.Count -eq 0) 'non_string_manifest_label_copied'
      }
    }
    Test-ProductCase 'manifest null path retains the original fixed failure rather than a diagnostic exception' {
      $expected=@([pscustomobject]@{Path=$null;Sha256='synthetic'})
      $actual=@([pscustomobject]@{Path='synthetic';Sha256='synthetic'})
      $failure=$null;try{Assert-ProductManifest $expected $actual 'stage_payload'}catch{$failure=$_}
      Require-ProductTest ($null -ne $failure -and $failure.Exception.Message -ceq 'product_manifest_changed') 'manifest_primary_failure_masked'
      $report=[ordered]@{};Add-ProductManifestDiagnostic $report $failure
      Require-ProductTest ($report.manifest_check -ceq 'stage_payload' -and $report.manifest_mismatch_kind -ceq 'path' -and
        -not $report.manifest_path_case_equal -and -not $report.manifest_path_separator_equal) 'null_manifest_path_diagnostic_incorrect'
    }
  }

  function Invoke-ProductSetupProfileCases {
    Test-ProductCase 'setup profile snapshot preserves all five unrelated files through normal removal' {
      $state=New-ProductFixture -SetupExtraFiles 5
      Require-ProductTest (@($state.Profile).Count -eq 8 -and @($state.SetupProfile).Count -eq 7) 'setup_profile_count_incorrect'
      Assert-ProductUninstallInputs $state
      $result=Invoke-TestProductCore $state $null
      Complete-TestProductRemoval $state
      Assert-ProductRemoved $state $result
      Require-ProductTest (@(Get-InstalledSmokeTree $state.Environment.HOME $state.Environment.HOME).Count -eq 7) 'setup_profile_files_lost'
    }
    Test-ProductCase 'profile must be empty before setup and cannot be recaptured afterward' {
      $state=New-ProductFixture -SetupExtraFiles 5
      $before=$state.SetupProfile|ConvertTo-Json -Compress
      Reject-ProductTest {Assert-ProductProfileBeforeSetup $state} 'product_profile_not_empty'
      Reject-ProductTest {Initialize-ProductProfileBaseline $state} 'product_profile_already_captured'
      Require-ProductTest (($state.SetupProfile|ConvertTo-Json -Compress) -ceq $before) 'setup_profile_recaptured'
    }
  }

  function Invoke-ProductSyntheticCollisionCases {
    foreach ($kind in @('hook','backup')) {
      Test-ProductCase ("synthetic data refuses setup $kind collision without overwriting it") {
        $state=New-ProductFixture -SetupCollision $kind -SkipSyntheticData
        $before=Get-InstalledSmokeHash $state.SetupCollisionPath
        $code=if($kind -ceq 'hook'){'product_hook_already_exists'}else{'product_backup_namespace_exists'}
        Reject-ProductTest {New-ProductSyntheticData $state} $code
        Require-ProductTest ((Get-InstalledSmokeHash $state.SetupCollisionPath) -ceq $before) 'setup_collision_overwritten'
      }
    }
  }

  function Invoke-ProductShortcutCaseCases {
    Test-ProductCase 'shortcut metadata casing uses the captured filesystem path without changing the baseline' {
      $state=New-ProductFixture -SetupExtraFiles 5
      $before=$state.SetupProfile|ConvertTo-Json -Compress
      foreach ($shortcut in $state.Global.Shortcuts) { $shortcut.Path=$shortcut.Path.ToUpperInvariant() }
      Assert-ProductUninstallInputs $state
      Set-ProductProfileBaseline $state
      Require-ProductTest (($state.SetupProfile|ConvertTo-Json -Compress) -ceq $before) 'setup_profile_case_rewritten'
      Complete-TestProductRemoval $state
      Assert-ProductRemoved $state ([pscustomobject]@{EmptyConfirmed=$true})
    }
  }

  function Invoke-ProductSetupMutationCases {
    $mutations=@{
      missing = {[IO.File]::Delete((Join-Path $state.Environment.LOCALAPPDATA 'setup-owned-0.fixture'))}
      hash = {[IO.File]::AppendAllText((Join-Path $state.Environment.LOCALAPPDATA 'setup-owned-0.fixture'),'changed')}
      path = {
        $extra=Join-Path $state.Environment.LOCALAPPDATA 'setup-owned-0.fixture'
        [IO.File]::Move($extra,($extra+'.moved'))
      }
      role = {$state.Global.Shortcuts[0].Role='Desktop';$state.Global.Shortcuts[1].Role='Programs'}
      duplicate = {$state.Global.Shortcuts[1].Path=$state.Global.Shortcuts[0].Path}
    }
    foreach ($kind in $mutations.Keys) {
      foreach ($late in @($false,$true)) {
        Test-ProductCase ("captured setup profile $kind late=$late keeps resume zero") {
          $state=New-ProductFixture -SetupExtraFiles 5
          $failure=$null
          try {
            if($late){[void](Invoke-TestProductCore $state $mutations[$kind])}
            else{& $mutations[$kind];[void](Invoke-TestProductCore $state $null)}
          }catch{$failure=$_}
          Require-ProductTest ($null -ne $failure) 'setup_profile_mutation_accepted'
          $api=[Arkism.InstallerChecks.ProductTestHarness]::Last
          Require-ProductTest ($api.ResumeCalls -eq 0 -and $api.FactoryCalls -eq [int]$late) 'setup_profile_mutation_resumed'
        }
      }
    }
  }

  function Invoke-ProductSetupPreservationCases {
    foreach ($kind in @('extra','missing','hash','path')) {
      Test-ProductCase ("removal preserves setup profile strictly and rejects $kind") {
        $state=New-ProductFixture -SetupExtraFiles 5
        Complete-TestProductRemoval $state
        $extra=Join-Path $state.Environment.LOCALAPPDATA 'setup-owned-0.fixture'
        switch($kind) {
          extra {[IO.File]::WriteAllText(($extra+'.new'),'new unrelated file')}
          missing {[IO.File]::Delete($extra)}
          hash {[IO.File]::AppendAllText($extra,'changed')}
          path {[IO.File]::Move($extra,($extra+'.moved'))}
        }
        $failure=$null;try{Assert-ProductRemoved $state ([pscustomobject]@{EmptyConfirmed=$true})}catch{$failure=$_}
        Require-ProductTest ($null -ne $failure) 'removed_setup_profile_mutation_accepted'
        $report=[ordered]@{};Add-ProductManifestDiagnostic $report $failure
        Require-ProductTest ($report.manifest_check -ceq 'profile_after_removal' -and $report.manifest_expected_files -eq 7) 'removed_setup_profile_diagnostic_missing'
      }
    }
  }

  function Invoke-ProductOrdinalProfileCases {
    Test-ProductCase 'ordinal setup profile preserves a distinct culturally equal shortcut name' {
      $state=New-ProductFixture -SetupCollision 'shortcut_name'
      $result=Invoke-TestProductCore $state $null
      Complete-TestProductRemoval $state
      Assert-ProductRemoved $state $result
      Require-ProductTest ([IO.File]::Exists($state.SetupCollisionPath)) 'ordinal_setup_file_removed'
    }
    Test-ProductCase 'ordinal setup profile refuses deletion of a distinct culturally equal shortcut name' {
      $state=New-ProductFixture -SetupCollision 'shortcut_name'
      $result=Invoke-TestProductCore $state $null
      Complete-TestProductRemoval $state
      [IO.File]::Delete($state.SetupCollisionPath)
      Reject-ProductTest {Assert-ProductRemoved $state $result} 'product_manifest_changed'
    }
    Test-ProductCase 'ordinal manifest rejects culturally equal path substitution with identical bytes' {
      $expected=@([pscustomobject]@{Path='Desktop/Arkism.lnk';Sha256=('a'*64)})
      $actual=@([pscustomobject]@{Path=('Desktop/Ar'+[char]0x00ad+'kism.lnk');Sha256=('a'*64)})
      $failure=$null;try{Assert-ProductManifest $expected $actual 'profile_before_removal'}catch{$failure=$_}
      Require-ProductTest ($null -ne $failure) 'ordinal_path_substitution_accepted'
      $report=[ordered]@{};Add-ProductManifestDiagnostic $report $failure
      Require-ProductTest ($report.manifest_mismatch_kind -ceq 'path' -and -not $report.manifest_path_case_equal) 'ordinal_path_diagnostic_incorrect'
    }
    Test-ProductCase 'ordinal manifest order preserves distinct paths and bytes regardless of enumeration' {
      $expected=@([pscustomobject]@{Path='Desktop/Arkism.lnk';Sha256=('a'*64)},
        [pscustomobject]@{Path=('Desktop/Ar'+[char]0x00ad+'kism.lnk');Sha256=('b'*64)})
      Assert-ProductManifest $expected @($expected[1],$expected[0]) 'profile_initial'
    }
    Test-ProductCase 'manifest labels reject culturally equal unknown strings' {
      foreach ($entry in @(@{Key='Check';Label='profile_initial'},@{Key='Kind';Label='count'})) {
        $exception=New-ProductManifestFailure @() @([pscustomobject]@{Path='synthetic';Sha256='synthetic'}) 'profile_initial'
        $exception.Data['ArkismProductManifest'][$entry.Key]=$entry.Label.Insert(2,[string][char]0x00ad)
        $report=[ordered]@{};Add-ProductManifestDiagnostic $report ([pscustomobject]@{Exception=$exception})
        Require-ProductTest ($report.Count -eq 0) 'ordinal_manifest_label_accepted'
      }
      Reject-ProductTest {New-ProductManifestFailure @() @() ('profile_in'+[char]0x00ad+'itial')} 'product_manifest_check_invalid'
    }
    foreach ($field in @('key','value')) {
      Test-ProductCase ("hook backup rejects culturally equal but different manifest $field") {
        $state=New-ProductFixture;Complete-TestProductRemoval $state
        $directory=Join-Path $state.Environment.HOME '.config/arkism/backups/synthetic-remove'
        $key=$state.Hook;$value=Join-Path $directory '0'
        if($field -ceq 'key'){$key=$key.Replace('hook-codex','hook-'+[char]0x00ad+'codex')}
        else{$value=$value.Replace('synthetic-remove','synthetic-'+[char]0x00ad+'remove')}
        $manifest=@{};$manifest[$key]=$value
        [IO.File]::WriteAllText((Join-Path $directory 'manifest.json'),($manifest|ConvertTo-Json))
        Reject-ProductTest {Assert-ProductRemoved $state ([pscustomobject]@{EmptyConfirmed=$true})} 'product_helper_backup_invalid'
      }
    }
  }

  $script:ProductPassed=0
  Invoke-ProductBindingCases
  Invoke-ProductCoreCases
  Invoke-ProductRemovalCases
  Invoke-ProductCoordinatorCases
  Invoke-ProductProcessSetCases
  Invoke-ProductLateProcessCases
  Invoke-ProductIdentityCases
  Invoke-ProductDiagnosticCases
  Invoke-ProductPolicyDiagnosticCases
  Invoke-ProductPolicyMalformedCases
  Invoke-ProductPolicyBindingCases
  Invoke-ProductScanDiagnosticCases
  Invoke-ProductNativeCimCases
  Invoke-ProductManifestDiagnosticCases
  Invoke-ProductManifestMalformedCases
  Invoke-ProductSetupProfileCases
  Invoke-ProductSyntheticCollisionCases
  Invoke-ProductShortcutCaseCases
  Invoke-ProductSetupMutationCases
  Invoke-ProductSetupPreservationCases
  Invoke-ProductOrdinalProfileCases
  Write-Output "All $script:ProductPassed product prototype offline cases passed; native calls: 0. Owned inert fixtures retained."
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-ProductUninstallOfflineTests $Filter }
