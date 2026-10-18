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
  function New-ProductFixture {
    $parent = Join-Path $suite ([guid]::NewGuid().ToString('N')); $root = Join-Path $parent 'owned'
    [IO.Directory]::CreateDirectory($root) | Out-Null
    $owner='a'*32; [IO.File]::WriteAllText((Join-Path $root '.installed-smoke-owner'),$owner)
    $context=@{OwnedRoots=@{};RunnerTemp=$parent;SourceSha=('b'*40)}; $context.OwnedRoots[$root]=$owner
    $fixture=@{Context=$context;Root=$root;Install=(Join-Path $root 'installed Arkism');Stage=(Join-Path $parent 'stage');
      Setup=(Join-Path $parent 'Arkism-0.1.0-setup.exe');Version='0.1.0';Environment=(New-InstalledSmokeEnvironment $root)}
    foreach($path in @($fixture.Stage,$fixture.Install,$fixture.Environment.HOME,$fixture.Environment.APPDATA,$fixture.Environment.LOCALAPPDATA,$fixture.Environment.TEMP)) {
      [IO.Directory]::CreateDirectory($path) | Out-Null
    }
    $names=@('Arkism.exe','integration/arkism.exe','runtime/memoryd.exe','runtime/memory-principal.exe','runtime/roles.sql',
      'runtime/pgsql/bin/postgres.exe','runtime/pgsql/bin/pg_ctl.exe','runtime/pgsql/bin/initdb.exe','runtime/pgsql/bin/psql.exe',
      'runtime/pgsql/bin/pg_dump.exe','runtime/pgsql/bin/pg_restore.exe','runtime/pgsql/lib/vector.dll','runtime/pgsql/share/extension/vector.control')
    foreach($name in $names) {
      foreach($base in @($fixture.Stage,$fixture.Install)) {
        $path=Join-Path $base $name;[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path))|Out-Null
        [IO.File]::WriteAllText($path,'inert_'+$name)
      }
    }
    [IO.File]::WriteAllText($fixture.Setup,'inert setup; never executed')
    [IO.File]::WriteAllText((Join-Path $fixture.Install 'uninstall.exe'),'inert uninstaller; never executed')
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
    New-ProductSyntheticData $fixture
    return $fixture
  }
  function Invoke-TestProductCore($State,[scriptblock]$Mutation,[bool]$Hang=$false,[string]$Faults='') {
    $image=Join-Path $State.Install 'uninstall.exe'
    $State.PolicyPids=@()
    $policy=[Action[long,string]]{param($AllowedPid,$AllowedImage);$State.PolicyPids+=$AllowedPid;Assert-ProductUninstallInputs $State $AllowedPid $AllowedImage}
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

  $script:ProductPassed=0
  Invoke-ProductBindingCases
  Invoke-ProductCoreCases
  Invoke-ProductRemovalCases
  Invoke-ProductCoordinatorCases
  Invoke-ProductProcessSetCases
  Invoke-ProductLateProcessCases
  Invoke-ProductIdentityCases
  Invoke-ProductDiagnosticCases
  Write-Output "All $script:ProductPassed product prototype offline cases passed; native calls: 0. Owned inert fixtures retained."
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-ProductUninstallOfflineTests $Filter }
