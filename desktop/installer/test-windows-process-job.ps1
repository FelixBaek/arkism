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
    public Action QueryHook;
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
        if (QueryHook != null) QueryHook();
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
    private static byte[] PeImage(int machine)
    {
        int size = machine == 0x8664 ? 240 : 224;
        byte[] bytes = new byte[88 + size]; bytes[0] = 77; bytes[1] = 90; bytes[60] = 64;
        bytes[64] = 80; bytes[65] = 69; bytes[68] = (byte)machine; bytes[69] = (byte)(machine >> 8);
        bytes[84] = (byte)size; bytes[86] = 2;
        int magic = machine == 0x8664 ? 0x20b : 0x10b; bytes[88] = (byte)magic; bytes[89] = (byte)(magic >> 8);
        bytes[88 + (machine == 0x8664 ? 108 : 92)] = 16;
        return bytes;
    }
    private static void Reject(Action action, string code)
    {
        try { action(); } catch (JobApiException error) { Check(error.Code.Contains(code), "wrong rejection"); return; }
        throw new Exception("rejection expected: " + code);
    }
    private static void RejectExact(Action action, string code)
    {
        try { action(); } catch (JobApiException error) { Check(error.Code == code && error.Message == code, "unsafe or incorrect diagnostic"); return; }
        throw new Exception("diagnostic rejection expected");
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
        Test("pending diagnostics retain accepted DOS and NT forms", delegate {
            var before = RebootObservation.Parse(false, 0, null);
            foreach (string prefix in new string[] { "", @"\??\" }) {
                var after = Snapshot(prefix + @"D:\owned\temp\Un.exe" + "\0\0");
                var result = RebootObservation.Compare(before, after, prefix + @"D:\owned\temp");
                Check(result.NewOwnedDeletes == 1 && result.ExistingRowsPreserved, "accepted path changed");
            }
        });
        Test("pending diagnostics distinguish rejected namespaces and origin contexts", delegate {
            var before = RebootObservation.Parse(false, 0, null);
            string[][] shapes = new string[][] {
                new string[] { @"\\?\D:\owned\temp\a", "extended" },
                new string[] { @"\\server\share\a", "unc" },
                new string[] { @"\\.\D:\owned\temp\a", "device" },
                new string[] { @"\Device\volume\a", "device" },
                new string[] { @"relative\a", "non_dos_ascii_letter" },
                new string[] { @"\??\UNC\server\share\a", "nt_other_ascii_letter" }
            };
            foreach (string[] shape in shapes) {
                var after = Snapshot(shape[0] + "\0\0");
                RejectExact(delegate { RebootObservation.Compare(before, after, @"D:\owned\temp"); },
                    "job_pending_path_unknown:new_row:" + shape[1] + ":not_dos_absolute");
                var valid = Snapshot(@"D:\owned\temp\a" + "\0\0");
                RejectExact(delegate { RebootObservation.Compare(before, valid, shape[0]); },
                    "job_pending_path_unknown:owned_temp:" + shape[1] + ":not_dos_absolute");
            }
        });
        Test("pending diagnostics distinguish characters components and suffixes without text", delegate {
            var before = RebootObservation.Parse(false, 0, null);
            string[][] shapes = new string[][] {
                new string[] { "a/b", "forbidden_character" }, new string[] { "a?", "forbidden_character" },
                new string[] { "a*", "forbidden_character" }, new string[] { "a\"", "forbidden_character" },
                new string[] { ".", "relative_component" }, new string[] { @"..\a", "relative_component" },
                new string[] { "a:b", "component_colon" }, new string[] { "a.", "trailing_dot" },
                new string[] { "a ", "trailing_space" }
            };
            foreach (string prefix in new string[] { "", @"\??\" }) {
                string space = prefix.Length == 0 ? "dos" : "nt_dos";
                foreach (string[] shape in shapes) {
                    string invalid = prefix + @"D:\owned\temp\" + shape[0];
                    RejectExact(delegate { RebootObservation.Compare(before, Snapshot(invalid + "\0\0"), @"D:\owned\temp"); },
                        "job_pending_path_unknown:new_row:" + space + ":" + shape[1]);
                    RejectExact(delegate { RebootObservation.Compare(before, Snapshot(@"D:\owned\temp\a" + "\0\0"), invalid); },
                        "job_pending_path_unknown:owned_temp:" + space + ":" + shape[1]);
                }
            }
        });
        Test("pending prefix diagnostics identify literal candidates without accepting them", delegate {
            var before = RebootObservation.Parse(false, 0, null);
            string[][] prefixes = new string[][] {
                new string[] { @"*1??\", "literal_star1_compact" }, new string[] { @"*2??\", "literal_star2_compact" },
                new string[] { @"*1\??\", "literal_star1_slash" }, new string[] { @"*2\??\", "literal_star2_slash" }
            };
            string[][] tails = new string[][] {
                new string[] { @"D:\owned\temp\a", "tail_literal_match" }, new string[] { @"D:\other\a", "other_tail" },
                new string[] { @"d:\OWNED\TEMP\a", "other_tail" }, new string[] { @"D:\owned\temp", "other_tail" },
                new string[] { @"D:\owned\temp\..\a", "tail_invalid" }, new string[] { @"D:\owned\temp\a ", "tail_invalid" },
                new string[] { @"\\?\D:\owned\temp\a", "tail_invalid" }, new string[] { @"\??\D:\owned\temp\a", "tail_invalid" }
            };
            foreach (string[] prefix in prefixes) {
                foreach (string[] tail in tails) {
                    RejectExact(delegate { RebootObservation.Compare(before, Snapshot(prefix[0] + tail[0] + "\0\0"), @"D:\owned\temp"); },
                        "job_pending_path_unknown:new_row:" + prefix[1] + ":not_dos_absolute:" + tail[1]);
                }
                var valid = Snapshot(@"D:\owned\temp\a" + "\0\0");
                RejectExact(delegate { RebootObservation.Compare(before, valid, prefix[0] + @"D:\owned\temp"); },
                    "job_pending_path_unknown:owned_temp:" + prefix[1] + ":not_dos_absolute:tail_unavailable");
            }
        });
        Test("pending prefix diagnostics expose only first character categories", delegate {
            var before = RebootObservation.Parse(false, 0, null);
            string[][] shapes = new string[][] {
                new string[] { @"\relative\a", "slash" }, new string[] { "/relative/a", "slash" },
                new string[] { "\"relative", "quote" }, new string[] { "'relative", "quote" },
                new string[] { " relative", "whitespace" }, new string[] { "\trelative", "control" },
                new string[] { "relative", "ascii_letter" }, new string[] { "1relative", "digit" },
                new string[] { @"*3??\D:\owned\temp\a", "punctuation" }, new string[] { "\uAC00relative", "nonascii" }
            };
            foreach (string[] shape in shapes) {
                RejectExact(delegate { RebootObservation.Compare(before, Snapshot(shape[0] + "\0\0"), @"D:\owned\temp"); },
                    "job_pending_path_unknown:new_row:non_dos_" + shape[1] + ":not_dos_absolute");
            }
            RejectExact(delegate { RebootObservation.Compare(before, Snapshot(@"D:\owned\temp\a" + "\0\0"), ""); },
                "job_pending_path_unknown:owned_temp:non_dos_empty:not_dos_absolute");
        });
        Test("echo binds two opaque NSIS rows to ordered exact single API additions", delegate {
            string old = "C:\\old\0\0", dir = "*1\\??\\D:\\owned\\temp\\copy\0\0", file = "*1\\??\\D:\\owned\\temp\\copy\\Un.exe\0\0";
            var first = Snapshot(old + dir + file); var rows = PendingEchoVerifier.Capture(Snapshot(old), first);
            var second = Snapshot(old + dir + file + dir); var third = Snapshot(old + dir + file + dir + file);
            PendingEchoVerifier.VerifyEcho(rows, 0, first, second); PendingEchoVerifier.VerifyEcho(rows, 1, second, third);
            PendingEchoVerifier.AssertUnchanged(third, Snapshot(old + dir + file + dir + file));
            Check(rows.Count == 2, "row capture failed");
            Reject(delegate { RebootObservation.Compare(Snapshot(old), first, @"D:\owned\temp"); }, "path_unknown");
        });
        Test("echo refuses dedup extra rows and nonempty destinations", delegate {
            string dir = "opaque-dir\0\0", file = "opaque-file\0\0";
            var first = Snapshot(dir + file); var rows = PendingEchoVerifier.Capture(RebootObservation.Parse(false, 0, null), first);
            Reject(delegate { PendingEchoVerifier.VerifyEcho(rows, 0, first, first); }, "delta_unsupported");
            Reject(delegate { PendingEchoVerifier.VerifyEcho(rows, 0, first, Snapshot(dir + file + dir + dir)); }, "delta_unsupported");
            Reject(delegate { PendingEchoVerifier.VerifyEcho(rows, 0, first, Snapshot(dir + file + "opaque-dir\0renamed\0\0")); }, "not_delete");
        });
        Test("echo equality is full Ordinal and ordered not suffix matching", delegate {
            string dir = "prefix-dir\0\0", file = "prefix-file\0\0";
            var first = Snapshot(dir + file); var rows = PendingEchoVerifier.Capture(RebootObservation.Parse(false, 0, null), first);
            foreach (string wrong in new string[] { file, "other-dir\0\0", "PREFIX-dir\0\0" })
                Reject(delegate { PendingEchoVerifier.VerifyEcho(rows, 0, first, Snapshot(dir + file + wrong)); }, "pair_mismatch");
        });
        Test("echo detects existing queue mutation and legacy changes", delegate {
            string old = "old\0\0", dir = "dir\0\0", file = "file\0\0";
            Reject(delegate { PendingEchoVerifier.Capture(Snapshot(old), Snapshot("changed\0\0" + dir + file)); }, "existing_changed");
            var after = Snapshot(old + dir + file); after.LegacyPresent = true;
            Reject(delegate { PendingEchoVerifier.Capture(Snapshot(old), after); }, "legacy_changed");
            Reject(delegate { PendingEchoVerifier.Capture(Snapshot(old), Snapshot(old + dir)); }, "delta_unsupported");
        });
        Test("echo final observation catches concurrent data and terminator changes", delegate {
            var before = Snapshot("dir\0\0file\0\0");
            Reject(delegate { PendingEchoVerifier.AssertUnchanged(before, Snapshot("dir\0\0file\0\0extra\0\0")); }, "final_changed");
            Reject(delegate { PendingEchoVerifier.AssertUnchanged(before, Snapshot("dir\0\0file\0\0\0")); }, "final_changed");
            var after = Snapshot("dir\0\0file\0\0"); after.LegacyPresent = true;
            Reject(delegate { PendingEchoVerifier.AssertUnchanged(before, after); }, "legacy_changed");
        });
        Test("echo leaf ownership failure prevents the API edge", delegate {
            int calls = 0;
            Reject(delegate { BoundEchoLeaf.RunCore(4, 4, delegate { throw new JobApiException("job_root_not_owned"); },
                delegate { calls++; return true; }, delegate { return 0; }); }, "root_not_owned");
            Check(calls == 0, "ownership failure reached API");
        });
        Test("echo leaf bitness mismatch precedes validation and API", delegate {
            int validation = 0, calls = 0;
            Reject(delegate { BoundEchoLeaf.RunCore(4, 8, delegate { validation++; }, delegate { calls++; return true; }, delegate { return 0; }); }, "bitness_mismatch");
            Check(validation == 0 && calls == 0, "wrong architecture ran");
        });
        Test("echo leaf performs only one API attempt and preserves failure result", delegate {
            int calls = 0, errorReads = 0;
            var success = BoundEchoLeaf.RunCore(4, 4, delegate {}, delegate { calls++; return true; }, delegate { errorReads++; return 5; });
            Check(success.Ok && success.Calls == 1 && calls == 1 && errorReads == 0, "unexpected success flow");
            var failed = BoundEchoLeaf.RunCore(4, 4, delegate {}, delegate { calls++; return false; }, delegate { return 5; });
            Check(!failed.Ok && failed.Error == 5 && failed.Calls == 1 && calls == 2, "failure retried or lost");
            var unknown = BoundEchoLeaf.RunCore(4, 4, delegate {}, delegate { calls++; throw new IOException("synthetic"); }, delegate { return 0; });
            Check(!unknown.Ok && unknown.Code == "leaf_boundary_failed" && calls == 3, "unknown API failure retried");
        });
        Test("echo result requires successful exit actual pointer and exactly one call", delegate {
            PendingEchoVerifier.ValidateResult(4, 4, true, 1, 0, 0);
            Reject(delegate { PendingEchoVerifier.ValidateResult(4, 8, true, 1, 0, 0); }, "bitness_mismatch");
            Reject(delegate { PendingEchoVerifier.ValidateResult(4, 4, false, 1, 5, 92); }, "api_unsuccessful");
            Reject(delegate { PendingEchoVerifier.ValidateResult(4, 4, true, 0, 0, 0); }, "api_unsuccessful");
            Reject(delegate { PendingEchoVerifier.ValidateResult(4, 4, true, 2, 0, 0); }, "api_unsuccessful");
        });
        Test("echo PE reader binds actual x86 and x64 headers with bounded reads", delegate {
            foreach (int machine in new int[] { 0x14c, 0x8664 }) {
                byte[] bytes = PeImage(machine);
                Check(PendingEchoVerifier.ReadMachine(new MemoryStream(bytes)) == machine, "wrong image machine");
                Check(PendingEchoVerifier.ReadNativeMachine(new MemoryStream(bytes)) == machine, "wrong native image machine");
            }
        });
        Test("echo PE reader rejects unsupported oversized offset and truncation", delegate {
            byte[] bytes = PeImage(0xaa64);
            Reject(delegate { PendingEchoVerifier.ReadMachine(new MemoryStream(bytes)); }, "machine_unsupported");
            bytes[62] = 0x20;
            Reject(delegate { PendingEchoVerifier.ReadMachine(new MemoryStream(bytes)); }, "pe_invalid");
            Reject(delegate { PendingEchoVerifier.ReadMachine(new MemoryStream(new byte[2])); }, "pe_truncated");
        });
        Test("echo native reference rejects DLL managed CLR and magic mismatch", delegate {
            byte[] managed = PeImage(0x14c); managed[88 + 96 + 112] = 1;
            Reject(delegate { PendingEchoVerifier.ReadNativeMachine(new MemoryStream(managed)); }, "reference_managed");
            Check(PendingEchoVerifier.ReadMachine(new MemoryStream(managed)) == 0x14c, "managed leaf machine unreadable");
            byte[] dll = PeImage(0x14c); dll[87] = 0x20;
            Reject(delegate { PendingEchoVerifier.ReadNativeMachine(new MemoryStream(dll)); }, "pe_invalid");
            byte[] magic = PeImage(0x14c); magic[89] = 2;
            Reject(delegate { PendingEchoVerifier.ReadNativeMachine(new MemoryStream(magic)); }, "pe_invalid");
        });
        Test("echo safe guard codes preserve enums without paths", delegate {
            Check(BoundEchoLeaf.SafeCode("job_root_not_owned") == "job_root_not_owned", "guard code lost");
            Check(BoundEchoLeaf.SafeCode("job_C:\\private") == "leaf_guard_failed", "unsafe text escaped");
            Check(BoundEchoLeaf.SafeCode("job_guard\n") == "leaf_guard_failed", "control character escaped");
            Check(BoundEchoLeaf.SafeCode(new string('x', 120)) == "leaf_guard_failed", "oversized text escaped");
        });
        Test("deadline boundary exact empty is successful", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.EmptyAt = 100;
            var result = WindowsProcessJob.RunCore(Request(), api, clock);
            Check(result.EmptyConfirmed && result.ElapsedMilliseconds == 100, "exact deadline empty rejected");
        });
        Test("deadline beyond boundary empty is still a timeout", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock); api.EmptyAt = 100;
            api.QueryHook = delegate { if (clock.Now == 100) clock.Now = 101; };
            Has(Failure(Request(), api, clock), "primary:job_timeout");
        });
        Test("deadline query clock regression cannot accept empty", delegate {
            var clock = new JobFakeClock(); var api = new JobFakeApi(clock);
            api.QueryHook = delegate { if (clock.Now == 40) { clock.Now = 30; api.EmptyAt = 0; } };
            var result = Failure(Request(), api, clock);
            Has(result, "clock_regressed"); Check(!result.Result.EmptyConfirmed, "regressed clock accepted empty");
        });
        return Passed.ToArray();
    }
}
'@
  $declarations = $tests -replace '(?m)^using [^;]+;\r?\n', ''
  Add-Type -TypeDefinition ("using Arkism.InstallerChecks;`n" + $core + "`n" + $declarations) @compiler
  $passed = [JobCoreTests]::Run($Filter)
  foreach ($name in $passed) { Write-Output "PASS $name" }
  $coordinator = Invoke-WindowsEchoCoordinatorTests $Filter
  Write-Output "All $($passed.Count + $coordinator) offline process job cases passed; native API calls: 0."
}

function Invoke-WindowsEchoCoordinatorTests([string]$Filter) {
  if ($Filter -and 'echo coordinator boundaries' -notmatch $Filter) { return 0 }
  . (Join-Path $PSScriptRoot 'test-nsis-uninstall-job.ps1')
  function New-JobEchoExecutable { return [pscustomobject]@{ Sha256 = 'synthetic'; PointerBytes = 4 } }
  function Get-JobEchoSnapshot { $value = $state.Reads[$state.Index]; $state.Index++; return $value }
  function Invoke-JobEchoLeaf($Context, $Root, $Copy, $Leaf, $Environment, $Kind, $Report) {
    $state.Calls += $Kind
    if ($state.Mode -eq 'api_failure') { throw 'synthetic API failure' }
  }
  function New-EchoSnapshot([string]$Text) {
    return [Arkism.InstallerChecks.RebootObservation]::Parse($true, 7, [Text.Encoding]::Unicode.GetBytes($Text))
  }
  $dir = 'opaque-dir' + [char]0 + [char]0; $file = 'opaque-file' + [char]0 + [char]0
  $after = New-EchoSnapshot ($dir + $file)
  $last = New-EchoSnapshot ($dir + $file + $dir + $file)
  $seconds = @{ success = New-EchoSnapshot ($dir + $file + $dir); mismatch = New-EchoSnapshot ($dir + $file + $file)
    api_failure = $after; dedup = $after; extra = $last }
  $phases = @{ success = 'complete'; mismatch = 'directory_match'; api_failure = 'directory_call'; dedup = 'directory_match'; extra = 'directory_match' }
  $deltas = @{ success = 1; mismatch = 1; api_failure = $null; dedup = 0; extra = 2 }
  foreach ($mode in @('success', 'mismatch', 'api_failure', 'dedup', 'extra')) {
    $second = $seconds[$mode]
    $state = @{ Mode = $mode; Calls = @(); Index = 0; Reads = @($after, $second, $second, $last) }
    $failure = $null; $report = [ordered]@{}
    try {
      $before = [Arkism.InstallerChecks.RebootObservation]::Parse($false, 0, $null)
      [void](Invoke-JobExactEcho @{} 'synthetic-root' @{} 332 @{} $before $after @{} $report)
    } catch { $failure = $_ }
    if ($mode -eq 'success') {
      if ($failure -or ($state.Calls -join ',') -cne 'directory,file') { throw 'echo coordinator success failed' }
    } elseif (-not $failure -or ($state.Calls -join ',') -cne 'directory') { throw 'echo coordinator did not stop before second API' }
    if ($report.nsis_observed_delta -ne 2 -or $report.echo_phase -cne $phases[$mode] -or
      $report.echo_last_observed_delta -cne $deltas[$mode]) { throw 'echo diagnostic stage or count is incorrect' }
    Write-Host "PASS echo coordinator $mode; fake native boundaries only"
  }
  return 5
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-WindowsProcessJobTests $Filter }
