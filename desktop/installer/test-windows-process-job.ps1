# Offline tests for the actual injected core. No native adapter is instantiated.
param([string]$Filter = '')

function Invoke-WindowsProcessJobTests([string]$Filter = '') {
  $source = Join-Path $PSScriptRoot 'WindowsProcessJob.cs'
  $compiler = @{}
  if ($PSVersionTable.PSVersion.Major -ge 7) { $compiler.CompilerOptions = '/langversion:5' }
  $core = [IO.File]::ReadAllText($source)
  $tests = @'
using System;
using System.Collections.Generic;
using System.Text;
using Arkism.InstallerChecks;

public sealed class JobFakeClock : IMonotonicClock
{
    public long Now;
    public bool Freeze, Regress;
    public long Milliseconds { get { return Now; } }
    public void Sleep(int delay) { if (Regress) Now--; else if (!Freeze) Now += delay; }
}

public sealed class JobFakeApi : IJobApi
{
    public readonly List<string> Calls = new List<string>();
    public readonly List<string> Faults = new List<string>();
    public readonly JobFakeClock Clock;
    public IntPtr Job = new IntPtr(11), Process = new IntPtr(12), Thread = new IntPtr(13);
    public uint ResumeValue = 1, Code = 0, Total = 3;
    public bool Member = true, Killed, OutsideControlAlive = true;
    public long EmptyAt = 40;
    public JobFakeApi(JobFakeClock clock) { Clock = clock; }
    private void Call(string name) { Calls.Add(name); if (Faults.Contains(name)) throw new JobApiException("fake_" + name); }
    public IntPtr CreateJob() { Call("create"); return Job; }
    public void Configure(IntPtr job) { Own(job); Call("configure"); }
    public ProcessHandles CreateSuspended(JobRequest request) { Call("suspended"); return new ProcessHandles { Process = Process, Thread = Thread }; }
    public void Assign(IntPtr job, IntPtr process) { Own(job); Call("assign"); }
    public bool IsMember(IntPtr job, IntPtr process) { Own(job); Call("member"); return Member; }
    public void VerifyImage(JobRequest request, IntPtr process) { Call("verify"); }
    public uint Resume(IntPtr thread) { Call("resume"); return ResumeValue; }
    public JobAccounting Query(IntPtr job)
    {
        Own(job); Call("query");
        return new JobAccounting { Active = Killed || Clock.Now >= EmptyAt ? 0U : 1U, Total = Total };
    }
    public uint ExitCode(IntPtr process) { Call("exit"); return Code; }
    public void TerminateRoot(IntPtr process) { Call("terminate_root"); Killed = true; }
    public void ReapRoot(IntPtr process, int milliseconds) { Call("reap"); }
    public void TerminateJob(IntPtr job) { Own(job); Call("terminate_job"); Killed = true; }
    public void Close(IntPtr handle) { Call("close_" + handle.ToInt64()); }
    private void Own(IntPtr job) { if (job == IntPtr.Zero || job != Job) throw new Exception("foreign job queried"); }
}

public static class JobCoreTests
{
    private static readonly List<string> Passed = new List<string>();
    private static string Selected;
    private static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }
    private static JobRequest Request() { return new JobRequest { TimeoutMilliseconds = 100, TerminationMilliseconds = 60 }; }
    private static JobFailure Failure(JobRequest request, JobFakeApi api, JobFakeClock clock)
    {
        try { WindowsProcessJob.RunCore(request, api, clock); }
        catch (JobFailure failure) { return failure; }
        throw new Exception("failure expected");
    }
    private static void Has(JobFailure error, string code) { Check(error.Message.Contains(code), "missing " + code); }
    private static void Test(string name, Action test)
    {
        if (!String.IsNullOrEmpty(Selected) && !System.Text.RegularExpressions.Regex.IsMatch(name, Selected)) return;
        test(); Passed.Add(name);
    }
    private static RebootSnapshot Snapshot(string text) { return RebootObservation.Parse(true, 7, Encoding.Unicode.GetBytes(text)); }
    private static void Reject(Action action, string code)
    {
        try { action(); } catch (JobApiException error) { Check(error.Code.Contains(code), "wrong rejection"); return; }
        throw new Exception("rejection expected: " + code);
    }

    public static string[] Run(string selected)
    {
        Selected = selected; Passed.Clear();
        Test("actual core waits for descendants after root has exited", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock);
            JobResult result = WindowsProcessJob.RunCore(Request(), api, clock);
            Check(result.EmptyConfirmed && result.TotalProcesses == 3 && clock.Now == 40 && result.RootExitedWhileActive, "tree not awaited");
            Check(String.Join(",", api.Calls.GetRange(0, 7).ToArray()) == "create,configure,suspended,assign,member,verify,resume", "unsafe order");
            Check(api.Calls.LastIndexOf("exit") > api.Calls.LastIndexOf("query"), "root exit used as authority");
            Check(String.Join(",", api.Calls.GetRange(api.Calls.Count - 3, 3).ToArray()) == "close_13,close_12,close_11", "handles not closed");
        });
        Test("configure failure creates no root", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Faults.Add("configure");
            Has(Failure(Request(), api, clock), "fake_configure");
            Check(!api.Calls.Contains("suspended") && api.Calls[api.Calls.Count - 1] == "close_11", "unexpected child");
        });
        Test("assignment failure terminates and reaps only suspended root", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Faults.Add("assign");
            JobFailure error = Failure(Request(), api, clock);
            Check(error.Result.RootReaped && !error.Result.MayHaveRun, "suspended root not reaped");
            Check(!api.Calls.Contains("resume") && !api.Calls.Contains("terminate_job") && api.Calls.Contains("terminate_root"), "wrong recovery");
        });
        Test("membership failure cannot resume", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Member = false;
            JobFailure error = Failure(Request(), api, clock); Has(error, "membership_failed");
            Check(!api.Calls.Contains("verify") && !api.Calls.Contains("resume") && error.Result.EmptyConfirmed, "membership bypass");
        });
        Test("image recheck failure cannot resume", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Faults.Add("verify");
            JobFailure error = Failure(Request(), api, clock); Has(error, "fake_verify");
            Check(!api.Calls.Contains("resume") && error.Result.EmptyConfirmed && error.Result.RootReaped, "image bypass");
        });
        Test("resume API failure and already running are distinct", delegate {
            foreach (uint previous in new uint[] { UInt32.MaxValue, 0, 2 }) {
                var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.ResumeValue = previous;
                JobFailure error = Failure(Request(), api, clock);
                Check(error.Result.MayHaveRun == (previous == 0), "incorrect zero effects claim");
                Check(error.Result.EmptyConfirmed && api.Calls.Contains("terminate_job"), "own job not reaped");
            }
        });
        Test("unknown resume exception conservatively may have run", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Faults.Add("resume");
            JobFailure error = Failure(Request(), api, clock);
            Check(error.Result.MayHaveRun && error.Result.EmptyConfirmed, "unknown resume claimed no effects");
        });
        Test("timeout kills own tree while separate control survives", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.EmptyAt = Int64.MaxValue;
            JobFailure error = Failure(Request(), api, clock); Has(error, "job_timeout");
            Check(error.Result.EmptyConfirmed && api.OutsideControlAlive && api.Calls.Contains("terminate_job"), "timeout escaped boundary");
        });
        Test("primary termination accounting and close errors all survive", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock);
            foreach (string name in new string[] { "query", "terminate_job", "close_13", "close_11" }) api.Faults.Add(name);
            JobFailure error = Failure(Request(), api, clock);
            Has(error, "primary:fake_query"); Has(error, "terminate_job:fake_terminate_job");
            Has(error, "confirm_empty:fake_query"); Has(error, "close_thread:fake_close_13"); Has(error, "close_job:fake_close_11");
            Check(!error.Result.EmptyConfirmed && api.Calls.Contains("close_12") && error.Causes.Count == 5, "failure lost");
        });
        Test("root nonzero is an error after authoritative empty", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Code = 7;
            JobFailure error = Failure(Request(), api, clock); Has(error, "root_exit_nonzero");
            Check(error.Result.EmptyConfirmed && !api.Calls.Contains("terminate_job"), "empty recovery changed");
        });
        Test("accounting minimum alone is not a success claim", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Total = 1;
            Has(Failure(Request(), api, clock), "process_count_low");
        });
        Test("regressing and frozen clocks fail boundedly", delegate {
            foreach (bool regress in new bool[] { true, false }) {
                var clock = new JobFakeClock { Regress = regress, Freeze = !regress }; var api = new JobFakeApi(clock);
                JobFailure error = Failure(Request(), api, clock);
                Has(error, regress ? "clock_regressed" : "clock_not_advancing");
                Check(api.Calls.Count < 20, "unbounded fake polling");
            }
        });
        Test("invalid job handle never reaches caller job query", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Job = IntPtr.Zero;
            Has(Failure(Request(), api, clock), "handle_invalid");
            Check(String.Join(",", api.Calls.ToArray()) == "create", "null job used");
        });
        Test("pseudo root handle is never terminated or closed", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.Process = new IntPtr(-1);
            Has(Failure(Request(), api, clock), "handle_invalid");
            Check(!api.Calls.Contains("terminate_root") && !api.Calls.Contains("close_-1"), "pseudo handle used");
        });
        Test("invalid budget fails before any API effects", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); var request = Request(); request.TimeoutMilliseconds = 0;
            Reject(delegate { WindowsProcessJob.RunCore(request, api, clock); }, "request_invalid");
            Check(api.Calls.Count == 0, "invalid request effected system");
        });
        Test("missing hosted identity cannot instantiate Windows adapter", delegate {
            Reject(delegate { WindowsProcessJob.RunWindows(new JobRequest()); }, "job_");
        });
        Test("x64 layout matches documented Windows ABI without native calls", delegate {
            var sizes = WindowsProcessJob.LayoutSizes();
            Check(sizes["pointer"] == 8 && sizes["basic"] == 64 && sizes["extended"] == 144 && sizes["accounting"] == 48 && sizes["startup"] == 104 && sizes["process"] == 24, "ABI layout mismatch");
            Check(sizes["basic_affinity"] == 48 && sizes["startup_flags"] == 60 && sizes["accounting_active"] == 40, "ABI offset mismatch");
        });
        Test("raw pending delete keeps empty destination", delegate {
            var before = Snapshot("C:\\old\\a\0\0"); var after = Snapshot("C:\\old\\a\0\0\\??\\D:\\owned\\temp\\Un.exe\0\0");
            var result = RebootObservation.Compare(before, after, "D:\\owned\\temp");
            Check(before.EntryCount == 1 && result.NewOwnedDeletes == 1 && result.ExistingRowsPreserved && !result.RawUnchanged, "empty destination lost");
        });
        Test("unchanged pending observation preserves bytes", delegate {
            var value = Snapshot("C:\\old\\a\0C:\\old\\b\0\0");
            Check(RebootObservation.Compare(value, value, "D:\\owned\\temp").RawUnchanged, "unchanged queue changed");
        });
        Test("existing queue order and source destination cannot change", delegate {
            var before = Snapshot("C:\\a\0\0C:\\b\0\0"); var after = Snapshot("C:\\b\0\0C:\\a\0\0");
            Reject(delegate { RebootObservation.Compare(before, after, "D:\\owned\\temp"); }, "existing_changed");
        });
        Test("foreign deletes rename destinations and device paths are rejected", delegate {
            var before = RebootObservation.Parse(false, 0, null);
            foreach (string text in new string[] { "D:\\outside\\a\0\0", "D:\\owned\\temp\\a\0D:\\owned\\temp\\b\0\0", "\\\\server\\a\0\0", "D:\\owned\\temp\\..\\a\0\0" }) {
                try { RebootObservation.Compare(before, Snapshot(text), "D:\\owned\\temp"); }
                catch (JobApiException) { continue; }
                throw new Exception("unsafe reservation accepted");
            }
        });
        Test("unknown pending type malformed bytes and empty middle source fail", delegate {
            Reject(delegate { RebootObservation.Parse(true, 1, Encoding.Unicode.GetBytes("\0\0")); }, "encoding_unknown");
            Reject(delegate { RebootObservation.Parse(true, 7, new byte[] { 0, 0, 0 }); }, "encoding_unknown");
            Reject(delegate { Snapshot("C:\\a\0C:\\b\0"); }, "unterminated");
            Reject(delegate { Snapshot("\0C:\\a\0\0"); }, "empty_source");
        });
        Test("existing pending value cannot disappear", delegate {
            Reject(delegate { RebootObservation.Compare(Snapshot("\0\0"), RebootObservation.Parse(false, 0, null), "D:\\owned\\temp"); }, "existing_changed");
        });
        Test("legacy reboot file changes cannot be hidden by an unchanged queue", delegate {
            var before = Snapshot("\0\0"); var after = Snapshot("\0\0");
            after.LegacyPresent = true; after.LegacySha256 = new string('a', 64);
            Reject(delegate { RebootObservation.Compare(before, after, "D:\\owned\\temp"); }, "legacy_reboot_changed");
        });
        Test("unexplained empty pending value creation is not an owned deletion", delegate {
            Reject(delegate { RebootObservation.Compare(RebootObservation.Parse(false, 0, null), Snapshot("\0\0"), "D:\\owned\\temp"); }, "existing_changed");
        });
        Test("output leaf ownership rejects existing marker and unsafe release", delegate {
            string root = Path.Combine(Path.GetTempPath(), "arkism-job-leaf-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(root);
            try {
                string marker = Path.Combine(root, "marker");
                WindowsGuard.OutputLeaf(marker, true);
                File.WriteAllText(marker, "unchanged");
                Reject(delegate { WindowsGuard.OutputLeaf(marker, true); }, "marker_already_exists");
                WindowsGuard.OutputLeaf(marker, false);
                Reject(delegate { WindowsGuard.OutputLeaf(root, false); }, "release_not_ordinary");
                bool invalid = false;
                try { WindowsGuard.OutputLeaf(root + "\0", false); } catch (ArgumentException) { invalid = true; }
                Check(invalid && File.ReadAllText(marker) == "unchanged", "leaf inspection swallowed error or mutated input");
            } finally { Directory.Delete(root, true); }
        });
        return Passed.ToArray();
    }
}
'@
  $declarations = $tests -replace '(?m)^using [^;]+;\r?\n', ''
  Add-Type -TypeDefinition ("using Arkism.InstallerChecks;`n" + $core + "`n" + $declarations) @compiler
  $passed = [JobCoreTests]::Run($Filter)
  foreach ($name in $passed) { Write-Output "PASS $name" }
  Write-Output "All $($passed.Count) offline process job cases passed; native API calls: 0."
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-WindowsProcessJobTests $Filter }
