// CI-only process-tree completion helper. This is not a security sandbox.
// The injected core below is the same core used by the guarded Windows adapter.
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;

namespace Arkism.InstallerChecks
{
    public sealed class JobRequest
    {
        public string Root;
        public string Owner;
        public string SourceSha;
        public string Executable;
        public string ExpectedSha256;
        public string Arguments = "/S";
        public string Mode = "child";
        public string Marker;
        public string Release;
        public int TimeoutMilliseconds = 180000;
        public int TerminationMilliseconds = 10000;
        public uint MinimumProcesses = 2;
    }

    public struct ProcessHandles
    {
        public IntPtr Process;
        public IntPtr Thread;
    }

    public struct JobAccounting
    {
        public uint Active;
        public uint Total;
    }

    public interface IJobApi
    {
        IntPtr CreateJob();
        void Configure(IntPtr job);
        ProcessHandles CreateSuspended(JobRequest request);
        void Assign(IntPtr job, IntPtr process);
        bool IsMember(IntPtr job, IntPtr process);
        void VerifyImage(JobRequest request, IntPtr process);
        uint Resume(IntPtr thread);
        JobAccounting Query(IntPtr job);
        uint ExitCode(IntPtr process);
        void TerminateRoot(IntPtr process);
        void ReapRoot(IntPtr process, int milliseconds);
        void TerminateJob(IntPtr job);
        void Close(IntPtr handle);
    }

    public interface IMonotonicClock
    {
        long Milliseconds { get; }
        void Sleep(int milliseconds);
    }

    public sealed class JobResult
    {
        public string Phase = "validate";
        public string ResumeState = "not_attempted";
        public bool MayHaveRun;
        public bool EmptyConfirmed;
        public bool RootReaped;
        public bool RootExitedWhileActive;
        public uint TotalProcesses;
        public uint RootExitCode;
        public long ElapsedMilliseconds;
        public readonly List<string> Errors = new List<string>();
    }

    public sealed class JobApiException : Exception
    {
        public readonly string Code;
        public readonly bool NoUserCode;
        public JobApiException(string code) : this(code, false) {}
        public JobApiException(string code, bool noUserCode) : base(code) { Code = code; NoUserCode = noUserCode; }
    }

    public sealed class JobFailure : Exception
    {
        public readonly JobResult Result;
        public readonly IList<Exception> Causes;
        public JobFailure(JobResult result, IList<Exception> causes)
            : base(String.Join(";", result.Errors.ToArray())) { Result = result; Causes = causes; }
    }

    public static class WindowsProcessJob
    {
        public static JobResult RunCore(JobRequest request, IJobApi api, IMonotonicClock clock)
        {
            ValidateBudgets(request);
            if (api == null || clock == null) throw new JobApiException("job_boundary_missing");
            clock = new CheckedClock(clock);
            var result = new JobResult();
            var errors = new List<Exception>();
            IntPtr job = IntPtr.Zero;
            var child = new ProcessHandles();
            bool assigned = false;
            bool resumeAttempted = false;
            long start = clock.Milliseconds;
            try
            {
                result.Phase = "create_job";
                job = api.CreateJob();
                RequireHandle(job);
                api.Configure(job);
                result.Phase = "create_suspended";
                child = api.CreateSuspended(request);
                RequireHandle(child.Process);
                RequireHandle(child.Thread);
                result.Phase = "assign";
                api.Assign(job, child.Process);
                assigned = true;
                if (!api.IsMember(job, child.Process)) throw new JobApiException("job_membership_failed");
                result.Phase = "verify_image";
                api.VerifyImage(request, child.Process);
                CheckDeadline(clock, start, request.TimeoutMilliseconds);
                result.Phase = "resume";
                resumeAttempted = true;
                result.ResumeState = "attempted";
                result.MayHaveRun = true;
                uint previous = api.Resume(child.Thread);
                RecordResume(previous, result);
                result.Phase = "wait_tree";
                var accounting = WaitEmpty(api, job, clock, start, request.TimeoutMilliseconds, delegate {
                    if (api.ExitCode(child.Process) == 0) result.RootExitedWhileActive = true;
                });
                result.EmptyConfirmed = true;
                result.TotalProcesses = accounting.Total;
                result.RootExitCode = api.ExitCode(child.Process);
                if (result.RootExitCode != 0) throw new JobApiException("job_root_exit_nonzero");
                if (accounting.Total < request.MinimumProcesses) throw new JobApiException("job_process_count_low");
                result.Phase = "complete";
            }
            catch (Exception error)
            {
                var known = error as JobApiException;
                if (result.Phase == "resume" && known != null && known.NoUserCode)
                { result.ResumeState = "api_failed"; result.MayHaveRun = false; }
                AddError(result, errors, "primary", error);
                Recover(api, clock, request, job, child.Process, assigned, resumeAttempted, result, errors);
            }
            finally
            {
                Close(api, child.Thread, "close_thread", result, errors);
                Close(api, child.Process, "close_process", result, errors);
                Close(api, job, "close_job", result, errors);
            }
            try { result.ElapsedMilliseconds = Elapsed(clock, start); }
            catch (Exception error) { AddError(result, errors, "clock", error); }
            if (errors.Count != 0) throw new JobFailure(result, errors.AsReadOnly());
            return result;
        }

        public static JobResult RunWindows(JobRequest request)
        {
            WindowsGuard.Validate(request);
            return RunCore(request, new NativeJobApi(), new StopwatchClock());
        }

        public static IDictionary<string, int> LayoutSizes()
        {
            return new Dictionary<string, int> {
                { "pointer", IntPtr.Size }, { "basic", Marshal.SizeOf(typeof(Native.BasicLimits)) },
                { "extended", Marshal.SizeOf(typeof(Native.ExtendedLimits)) },
                { "accounting", Marshal.SizeOf(typeof(Native.Accounting)) },
                { "startup", Marshal.SizeOf(typeof(Native.Startup)) },
                { "process", Marshal.SizeOf(typeof(Native.ProcessInfo)) },
                { "basic_affinity", Marshal.OffsetOf(typeof(Native.BasicLimits), "Affinity").ToInt32() },
                { "startup_flags", Marshal.OffsetOf(typeof(Native.Startup), "Flags").ToInt32() },
                { "accounting_active", Marshal.OffsetOf(typeof(Native.Accounting), "ActiveProcesses").ToInt32() }
            };
        }

        private static void ValidateBudgets(JobRequest request)
        {
            if (request == null || request.TimeoutMilliseconds < 1 || request.TimeoutMilliseconds > 300000 ||
                request.TerminationMilliseconds < 1 || request.TerminationMilliseconds > 10000 || request.MinimumProcesses < 1)
                throw new JobApiException("job_request_invalid");
        }

        private static bool Usable(IntPtr handle) { return handle != IntPtr.Zero && handle != new IntPtr(-1); }

        private static void RequireHandle(IntPtr handle)
        {
            if (handle == IntPtr.Zero || handle == new IntPtr(-1)) throw new JobApiException("job_handle_invalid");
        }

        private static void RecordResume(uint previous, JobResult result)
        {
            if (previous == UInt32.MaxValue) { result.ResumeState = "api_failed"; result.MayHaveRun = false; throw new JobApiException("job_resume_failed"); }
            if (previous == 0)
            {
                result.ResumeState = "already_running";
                result.MayHaveRun = true;
                throw new JobApiException("job_resume_already_running");
            }
            if (previous != 1) { result.ResumeState = "still_suspended"; result.MayHaveRun = false; throw new JobApiException("job_resume_count_invalid"); }
            result.ResumeState = "resumed";
            result.MayHaveRun = true;
        }

        private static long Elapsed(IMonotonicClock clock, long start)
        {
            long now = clock.Milliseconds;
            if (start < 0 || now < start) throw new JobApiException("job_clock_regressed");
            return checked(now - start);
        }

        private static void CheckDeadline(IMonotonicClock clock, long start, int budget)
        {
            if (Elapsed(clock, start) >= budget) throw new JobApiException("job_timeout");
        }

        private static JobAccounting WaitEmpty(IJobApi api, IntPtr job, IMonotonicClock clock, long start, int budget, Action activeObservation)
        {
            RequireHandle(job); // A null handle would query the caller's job instead.
            for (;;)
            {
                var value = api.Query(job);
                CheckDeadline(clock, start, budget);
                if (value.Active == 0) return value;
                if (activeObservation != null) activeObservation(); // Diagnostic only; never the completion authority.
                long before = clock.Milliseconds;
                int delay = (int)Math.Min(20, budget - Elapsed(clock, start));
                if (delay < 1) throw new JobApiException("job_timeout");
                clock.Sleep(delay);
                if (clock.Milliseconds <= before) throw new JobApiException("job_clock_not_advancing");
            }
        }

        private static void Recover(IJobApi api, IMonotonicClock clock, JobRequest request, IntPtr job,
            IntPtr process, bool assigned, bool resumeAttempted, JobResult result, List<Exception> errors)
        {
            if (!Usable(process) || result.EmptyConfirmed) return;
            if (!resumeAttempted)
            {
                Try(delegate { api.TerminateRoot(process); }, "terminate_root", result, errors);
                Try(delegate { api.ReapRoot(process, request.TerminationMilliseconds); result.RootReaped = true; }, "reap_root", result, errors);
                if (!assigned) return;
            }
            else Try(delegate { api.TerminateJob(job); }, "terminate_job", result, errors);
            Try(delegate {
                long start = clock.Milliseconds;
                var value = WaitEmpty(api, job, clock, start, request.TerminationMilliseconds, null);
                result.EmptyConfirmed = true;
                result.TotalProcesses = value.Total;
            }, "confirm_empty", result, errors);
        }

        private static void Close(IJobApi api, IntPtr handle, string phase, JobResult result, List<Exception> errors)
        {
            if (Usable(handle)) Try(delegate { api.Close(handle); }, phase, result, errors);
        }

        private static void Try(Action action, string phase, JobResult result, List<Exception> errors)
        {
            try { action(); } catch (Exception error) { AddError(result, errors, phase, error); }
        }

        private static void AddError(JobResult result, List<Exception> errors, string phase, Exception error)
        {
            var known = error as JobApiException;
            result.Errors.Add(phase + ":" + (known == null ? "job_boundary_failed" : known.Code));
            errors.Add(error);
        }
    }

    internal sealed class CheckedClock : IMonotonicClock
    {
        private readonly IMonotonicClock inner;
        private long last = -1;
        internal CheckedClock(IMonotonicClock value) { inner = value; }
        public long Milliseconds
        {
            get
            {
                long now = inner.Milliseconds;
                if (now < 0 || now < last) throw new JobApiException("job_clock_regressed");
                last = now;
                return now;
            }
        }
        public void Sleep(int milliseconds) { inner.Sleep(milliseconds); }
    }

    internal sealed class StopwatchClock : IMonotonicClock
    {
        private readonly Stopwatch watch = Stopwatch.StartNew();
        public long Milliseconds { get { return watch.ElapsedMilliseconds; } }
        public void Sleep(int milliseconds) { Thread.Sleep(milliseconds); }
    }

    internal static class WindowsGuard
    {
        internal static void Context(JobRequest request)
        {
            if (Environment.OSVersion.Platform != PlatformID.Win32NT ||
                IntPtr.Size != 8 ||
                Environment.GetEnvironmentVariable("GITHUB_ACTIONS") != "true" ||
                Environment.GetEnvironmentVariable("RUNNER_ENVIRONMENT") != "github-hosted")
                throw new JobApiException("job_hosted_windows_required");
            if (request == null || !Regex.IsMatch(request.SourceSha ?? "", "^[0-9a-f]{40}$") ||
                request.SourceSha != Environment.GetEnvironmentVariable("GITHUB_SHA")) throw new JobApiException("job_source_mismatch");
            string runner = Full(Environment.GetEnvironmentVariable("RUNNER_TEMP"));
            Ordinary(runner, true);
            Ordinary(request.Root, true);
            if (!Within(request.Root, runner) || !Regex.IsMatch(request.Owner ?? "", "^[0-9a-f]{32}$"))
                throw new JobApiException("job_root_not_owned");
            string marker = Path.Combine(request.Root, ".installed-smoke-owner");
            Ordinary(marker, false);
            if (new FileInfo(marker).Length != 32 || File.ReadAllText(marker) != request.Owner) throw new JobApiException("job_root_not_owned");
            Ordinary(Path.Combine(request.Root, "temp"), true);
        }

        internal static void Validate(JobRequest request)
        {
            Context(request);
            if (request.Arguments != "/S" && request.Arguments != "") throw new JobApiException("job_fixture_arguments_invalid");
            string name = Path.GetFileName(request.Executable);
            if (name != "fixture-uninstall.exe" && name != "inert-process.exe") throw new JobApiException("job_inert_image_required");
            VerifyFile(request);
            OwnedOptional(request.Marker, request.Root, true);
            OwnedOptional(request.Release, request.Root, false);
            if (request.Mode != "child" && request.Mode != "grandchild" && request.Mode != "hang-parent" &&
                request.Mode != "hang" && request.Mode != "control") throw new JobApiException("job_fixture_mode_invalid");
        }

        internal static void VerifyFile(JobRequest request)
        {
            Ordinary(request.Executable, false);
            if (!Within(request.Executable, request.Root) || !Regex.IsMatch(request.ExpectedSha256 ?? "", "^[0-9a-f]{64}$") ||
                Hash(request.Executable) != request.ExpectedSha256) throw new JobApiException("job_image_mismatch");
        }

        internal static string Full(string path)
        {
            if (String.IsNullOrWhiteSpace(path) || !Regex.IsMatch(path, @"^[A-Za-z]:[\\/]") ||
                path.IndexOfAny(new char[] { '\0', '"' }) >= 0) throw new JobApiException("job_path_invalid");
            return Path.GetFullPath(path);
        }

        internal static bool Within(string path, string root)
        {
            return Full(path).StartsWith(Full(root).TrimEnd('\\', '/') + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase);
        }

        internal static void Ordinary(string path, bool directory)
        {
            path = Full(path);
            string parent = Path.GetDirectoryName(path);
            if (!String.IsNullOrEmpty(parent) && parent != path) Ordinary(parent, true);
            FileAttributes attributes = File.GetAttributes(path);
            if ((attributes & FileAttributes.ReparsePoint) != 0 || ((attributes & FileAttributes.Directory) != 0) != directory)
                throw new JobApiException("job_path_not_ordinary");
        }

        private static void OwnedOptional(string path, string root, bool absentRequired)
        {
            if (String.IsNullOrEmpty(path)) return;
            if (!Within(path, root)) throw new JobApiException("job_output_not_owned");
            Ordinary(Path.GetDirectoryName(path), true);
            OutputLeaf(path, absentRequired);
        }

        internal static void OutputLeaf(string path, bool absentRequired)
        {
            FileAttributes attributes;
            try { attributes = File.GetAttributes(path); }
            catch (FileNotFoundException) { return; }
            catch (DirectoryNotFoundException) { return; }
            if (absentRequired) throw new JobApiException("job_marker_already_exists");
            if ((attributes & (FileAttributes.ReparsePoint | FileAttributes.Directory)) != 0)
                throw new JobApiException("job_release_not_ordinary");
        }

        internal static string Hash(string path)
        {
            using (var file = File.Open(path, FileMode.Open, FileAccess.Read, FileShare.Read))
            using (var hash = SHA256.Create()) return BitConverter.ToString(hash.ComputeHash(file)).Replace("-", "").ToLowerInvariant();
        }

        internal static string EnvironmentBlock(JobRequest request)
        {
            var values = new SortedDictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            foreach (string name in new string[] { "SystemRoot", "WINDIR", "SystemDrive", "ComSpec", "PATHEXT", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS" })
            {
                string value = Environment.GetEnvironmentVariable(name);
                if (!String.IsNullOrEmpty(value)) values[name] = value;
            }
            string system = Environment.GetEnvironmentVariable("SystemRoot");
            values["PATH"] = system + @"\System32;" + system + @"\System32\WindowsPowerShell\v1.0";
            values["HOME"] = values["USERPROFILE"] = Path.Combine(request.Root, "profile");
            values["APPDATA"] = Path.Combine(request.Root, @"profile\AppData\Roaming");
            values["LOCALAPPDATA"] = Path.Combine(request.Root, @"profile\AppData\Local");
            values["TEMP"] = values["TMP"] = Path.Combine(request.Root, "temp");
            values["ARKISM_JOB_MODE"] = request.Mode;
            values["ARKISM_JOB_MARKER"] = request.Marker ?? "";
            values["ARKISM_JOB_RELEASE"] = request.Release ?? "";
            var block = new StringBuilder();
            foreach (var pair in values)
            {
                if (pair.Value.IndexOf('\0') >= 0) throw new JobApiException("job_environment_invalid");
                block.Append(pair.Key).Append('=').Append(pair.Value).Append('\0');
            }
            return block.Append('\0').ToString();
        }
    }

    public sealed class RebootSnapshot
    {
        internal readonly List<string[]> Pairs = new List<string[]>();
        internal byte[] Raw;
        public bool Present { get; internal set; }
        public bool LegacyPresent { get; internal set; }
        public string LegacySha256 { get; internal set; }
        public int EntryCount { get { return Pairs.Count; } }
    }

    public sealed class RebootComparison
    {
        public bool ExistingRowsPreserved;
        public bool RawUnchanged;
        public int NewOwnedDeletes;
    }

    // Observation only. Unknown encodings and unrelated queue changes fail closed;
    // this helper never rewrites a shared reboot queue or removes its reservations.
    public static class RebootObservation
    {
        public static RebootSnapshot Parse(bool present, uint kind, byte[] raw)
        {
            var value = new RebootSnapshot(); value.Present = present;
            if (!present)
            {
                if (raw != null && raw.Length != 0) throw new JobApiException("job_pending_absence_invalid");
                value.Raw = new byte[0]; return value;
            }
            if (kind != 7 || raw == null || raw.Length < 4 || raw.Length > 1048576 || raw.Length % 2 != 0)
                throw new JobApiException("job_pending_encoding_unknown");
            value.Raw = (byte[])raw.Clone();
            string text = new UnicodeEncoding(false, false, true).GetString(raw);
            if (!text.EndsWith("\0\0", StringComparison.Ordinal)) throw new JobApiException("job_pending_unterminated");
            int offset = 0;
            while (offset < text.Length)
            {
                if (text[offset] == '\0') { RequireNullTail(text, offset); break; }
                string source = ReadField(text, ref offset);
                string destination = ReadField(text, ref offset);
                value.Pairs.Add(new string[] { source, destination });
            }
            return value;
        }

        private static string ReadField(string text, ref int offset)
        {
            int end = text.IndexOf('\0', offset);
            if (end < 0) throw new JobApiException("job_pending_unterminated");
            string field = text.Substring(offset, end - offset); offset = end + 1; return field;
        }

        private static void RequireNullTail(string text, int offset)
        {
            for (int i = offset; i < text.Length; i++)
                if (text[i] != '\0') throw new JobApiException("job_pending_empty_source");
        }

        public static RebootComparison Compare(RebootSnapshot before, RebootSnapshot after, string ownedTemp)
        {
            if (before == null || after == null || (before.Present && !after.Present) || after.EntryCount < before.EntryCount)
                throw new JobApiException("job_pending_existing_changed");
            if (before.LegacyPresent != after.LegacyPresent || before.LegacySha256 != after.LegacySha256)
                throw new JobApiException("job_legacy_reboot_changed");
            if (before.EntryCount == after.EntryCount && (before.Present != after.Present || !EqualBytes(before.Raw, after.Raw)))
                throw new JobApiException("job_pending_existing_changed");
            for (int i = 0; i < before.EntryCount; i++)
                if (before.Pairs[i][0] != after.Pairs[i][0] || before.Pairs[i][1] != after.Pairs[i][1])
                    throw new JobApiException("job_pending_existing_changed");
            string prefix = QueuePath(ownedTemp, "owned_temp").TrimEnd('\\') + "\\";
            for (int i = before.EntryCount; i < after.EntryCount; i++)
            {
                string[] pair = after.Pairs[i];
                if (pair[1].Length != 0 || !QueuePath(pair[0], "new_row", prefix).StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
                    throw new JobApiException("job_pending_new_not_owned_delete");
            }
            return new RebootComparison { ExistingRowsPreserved = true,
                RawUnchanged = EqualBytes(before.Raw, after.Raw) && before.Present == after.Present,
                NewOwnedDeletes = after.EntryCount - before.EntryCount };
        }

        public static RebootComparison CompareWindows(RebootSnapshot before, RebootSnapshot after, JobRequest request)
        {
            WindowsGuard.Context(request);
            var result = Compare(before, after, Path.Combine(request.Root, "temp"));
            for (int i = before.EntryCount; i < after.EntryCount; i++) InspectExistingQueuePath(QueuePath(after.Pairs[i][0], "new_row"));
            return result;
        }

        private static void InspectExistingQueuePath(string path)
        {
            string parent = Path.GetDirectoryName(path);
            if (!String.IsNullOrEmpty(parent) && parent != path) InspectExistingQueueDirectory(parent);
            try
            {
                FileAttributes attributes = File.GetAttributes(path);
                if ((attributes & FileAttributes.ReparsePoint) != 0) throw new JobApiException("job_pending_reparse");
            }
            catch (FileNotFoundException) { } // A genuinely absent owned deletion target is permitted.
            catch (DirectoryNotFoundException) { }
        }

        private static void InspectExistingQueueDirectory(string path)
        {
            string parent = Path.GetDirectoryName(path);
            if (!String.IsNullOrEmpty(parent) && parent != path) InspectExistingQueueDirectory(parent);
            try { WindowsGuard.Ordinary(path, true); }
            catch (FileNotFoundException) { }
            catch (DirectoryNotFoundException) { }
        }

        private static bool EqualBytes(byte[] left, byte[] right)
        {
            if (left.Length != right.Length) return false;
            for (int i = 0; i < left.Length; i++) if (left[i] != right[i]) return false;
            return true;
        }

        private static string QueuePath(string path, string context, string diagnosticPrefix = null)
        {
            bool ntPrefix = path.StartsWith(@"\??\", StringComparison.Ordinal);
            if (ntPrefix) path = path.Substring(4);
            if (!Regex.IsMatch(path, @"^[A-Za-z]:\\"))
            {
                int literalLength;
                string rejected = RejectedNamespace(path, ntPrefix, out literalLength);
                string reason = "not_dos_absolute";
                if (literalLength != 0) reason += ":" + DiagnoseTail(path.Substring(literalLength), diagnosticPrefix);
                throw QueuePathError(context, rejected, reason);
            }
            string space = ntPrefix ? "nt_dos" : "dos";
            if (path.IndexOfAny(new char[] { '/', '"', '\0', '*', '?' }) >= 0)
                throw QueuePathError(context, space, "forbidden_character");
            foreach (string part in path.Substring(3).Split('\\'))
                CheckQueueComponent(part, context, space);
            return path;
        }

        private static string RejectedNamespace(string path, bool ntPrefix, out int literalLength)
        {
            string literal = LiteralPrefixKind(path, out literalLength);
            if (literal != null) return (ntPrefix ? "nt_prefix_" : "") + literal;
            if (path.StartsWith(@"\\?\", StringComparison.Ordinal)) return "extended";
            if (path.StartsWith(@"\\.\", StringComparison.Ordinal) || path.StartsWith(@"\Device\", StringComparison.Ordinal)) return "device";
            if (path.StartsWith(@"\\", StringComparison.Ordinal)) return "unc";
            return (ntPrefix ? "nt_other_" : "non_dos_") + FirstCharacterKind(path);
        }

        private static string LiteralPrefixKind(string path, out int length)
        {
            string[] prefixes = { @"*1??\", @"*2??\", @"*1\??\", @"*2\??\" };
            string[] labels = { "literal_star1_compact", "literal_star2_compact", "literal_star1_slash", "literal_star2_slash" };
            for (int i = 0; i < prefixes.Length; i++)
                if (path.StartsWith(prefixes[i], StringComparison.Ordinal)) { length = prefixes[i].Length; return labels[i]; }
            length = 0; return null;
        }

        private static string FirstCharacterKind(string path)
        {
            if (path.Length == 0) return "empty";
            char value = path[0];
            if (value > 127) return "nonascii";
            if (Char.IsControl(value)) return "control";
            if (Char.IsWhiteSpace(value)) return "whitespace";
            if (value == '\\' || value == '/') return "slash";
            if (value == '"' || value == '\'') return "quote";
            if (Char.IsLetter(value)) return "ascii_letter";
            if (Char.IsDigit(value)) return "digit";
            return "punctuation";
        }

        private static string DiagnoseTail(string tail, string diagnosticPrefix)
        {
            if (diagnosticPrefix == null) return "tail_unavailable";
            if (!Regex.IsMatch(tail, @"^[A-Za-z]:\\")) return "tail_invalid";
            try { QueuePath(tail, "tail_probe"); }
            catch (JobApiException) { return "tail_invalid"; }
            // Exact Ordinal text only; this neither proves ownership nor authorizes a row.
            return tail.StartsWith(diagnosticPrefix, StringComparison.Ordinal) ? "tail_literal_match" : "other_tail";
        }

        private static void CheckQueueComponent(string part, string context, string space)
        {
            if (part == "." || part == "..") throw QueuePathError(context, space, "relative_component");
            if (part.IndexOf(':') >= 0) throw QueuePathError(context, space, "component_colon");
            if (part.EndsWith(".")) throw QueuePathError(context, space, "trailing_dot");
            if (part.EndsWith(" ")) throw QueuePathError(context, space, "trailing_space");
        }

        private static JobApiException QueuePathError(string context, string space, string reason)
        {
            // All three arguments are fixed diagnostic labels; no queue text is included.
            return new JobApiException("job_pending_path_unknown:" + context + ":" + space + ":" + reason);
        }

        public static RebootSnapshot ReadWindows(JobRequest request)
        {
            WindowsGuard.Context(request);
            var value = ReadRegistry();
            string legacy = Path.Combine(Environment.GetEnvironmentVariable("SystemRoot"), "wininit.ini");
            try
            {
                WindowsGuard.Ordinary(legacy, false);
                if (new FileInfo(legacy).Length > 1048576) throw new JobApiException("job_legacy_reboot_too_large");
                value.LegacyPresent = true; value.LegacySha256 = WindowsGuard.Hash(legacy);
            }
            catch (FileNotFoundException) { value.LegacyPresent = false; }
            return value;
        }

        private static RebootSnapshot ReadRegistry()
        {
            IntPtr key;
            int error = RegistryNative.RegOpenKeyExW(new IntPtr(unchecked((int)0x80000002)),
                @"SYSTEM\CurrentControlSet\Control\Session Manager", 0, 0x101, out key);
            if (error == 2) return Parse(false, 0, null);
            if (error != 0) throw new JobApiException("job_pending_open:" + error);
            Exception primary = null;
            try { return ReadValue(key); }
            catch (Exception failure) { primary = failure; throw; }
            finally
            {
                int close = RegistryNative.RegCloseKey(key);
                if (close != 0)
                {
                    if (primary != null) throw new AggregateException(primary, new JobApiException("job_pending_close:" + close));
                    throw new JobApiException("job_pending_close:" + close);
                }
            }
        }

        private static RebootSnapshot ReadValue(IntPtr key)
        {
            uint kind, length = 0;
            int error = RegistryNative.RegQueryValueExW(key, "PendingFileRenameOperations", IntPtr.Zero, out kind, null, ref length);
            if (error == 2) return Parse(false, 0, null);
            if (error != 0 || length > 1048576) throw new JobApiException("job_pending_size_unknown:" + error);
            byte[] raw = new byte[length]; uint actual = length;
            error = RegistryNative.RegQueryValueExW(key, "PendingFileRenameOperations", IntPtr.Zero, out kind, raw, ref actual);
            if (error != 0 || actual != length) throw new JobApiException("job_pending_read_changed:" + error);
            return Parse(true, kind, raw);
        }
    }

    internal static class RegistryNative
    {
        [DllImport("advapi32.dll", CharSet = CharSet.Unicode)] internal static extern int RegOpenKeyExW(IntPtr root, string path, uint options, uint access, out IntPtr key);
        [DllImport("advapi32.dll", CharSet = CharSet.Unicode)] internal static extern int RegQueryValueExW(IntPtr key, string name, IntPtr reserved, out uint kind, byte[] data, ref uint length);
        [DllImport("advapi32.dll")] internal static extern int RegCloseKey(IntPtr key);
    }

    internal sealed class NativeJobApi : IJobApi
    {
        public IntPtr CreateJob() { return CheckedHandle(Native.CreateJobObjectW(IntPtr.Zero, null), "job_create"); }
        public void Configure(IntPtr job)
        {
            var limits = new Native.ExtendedLimits();
            limits.Basic.LimitFlags = 0x2000; // KILL_ON_JOB_CLOSE, neither breakaway option.
            Check(Native.SetInformationJobObject(job, 9, ref limits, (uint)Marshal.SizeOf(typeof(Native.ExtendedLimits))), "job_configure");
        }
        public ProcessHandles CreateSuspended(JobRequest request)
        {
            var startup = new Native.Startup(); startup.Size = (uint)Marshal.SizeOf(typeof(Native.Startup));
            Native.ProcessInfo child;
            IntPtr environment = Marshal.StringToHGlobalUni(WindowsGuard.EnvironmentBlock(request));
            try
            {
                var command = new StringBuilder("\"" + WindowsGuard.Full(request.Executable) + "\" " + request.Arguments);
                Check(Native.CreateProcessW(request.Executable, command, IntPtr.Zero, IntPtr.Zero, false,
                    0x00000004 | 0x00000400 | 0x08000000, environment, Path.Combine(request.Root, "temp"), ref startup, out child), "job_create_suspended");
            }
            finally { Marshal.FreeHGlobal(environment); }
            return new ProcessHandles { Process = child.Process, Thread = child.Thread };
        }
        public void Assign(IntPtr job, IntPtr process) { Check(Native.AssignProcessToJobObject(job, process), "job_assign"); }
        public bool IsMember(IntPtr job, IntPtr process)
        {
            bool member; Check(Native.IsProcessInJob(process, job, out member), "job_membership_query"); return member;
        }
        public void VerifyImage(JobRequest request, IntPtr process)
        {
            WindowsGuard.Validate(request);
            var path = new StringBuilder(32768); uint size = (uint)path.Capacity;
            Check(Native.QueryFullProcessImageNameW(process, 0, path, ref size), "job_image_query");
            if (!String.Equals(WindowsGuard.Full(path.ToString()), WindowsGuard.Full(request.Executable), StringComparison.OrdinalIgnoreCase))
                throw new JobApiException("job_process_image_mismatch");
        }
        public uint Resume(IntPtr thread)
        {
            uint value = Native.ResumeThread(thread);
            if (value == UInt32.MaxValue) throw new JobApiException("job_resume:" + Marshal.GetLastWin32Error(), true);
            return value;
        }
        public JobAccounting Query(IntPtr job)
        {
            Native.Accounting value; uint length;
            Check(Native.QueryInformationJobObject(job, 1, out value, (uint)Marshal.SizeOf(typeof(Native.Accounting)), out length), "job_accounting_query");
            return new JobAccounting { Active = value.ActiveProcesses, Total = value.TotalProcesses };
        }
        public uint ExitCode(IntPtr process) { uint code; Check(Native.GetExitCodeProcess(process, out code), "job_exit_query"); return code; }
        public void TerminateRoot(IntPtr process) { Check(Native.TerminateProcess(process, 121), "job_root_terminate"); }
        public void ReapRoot(IntPtr process, int milliseconds)
        {
            if (Native.WaitForSingleObject(process, (uint)milliseconds) != 0) throw new JobApiException("job_root_reap_failed");
        }
        public void TerminateJob(IntPtr job) { Check(Native.TerminateJobObject(job, 121), "job_terminate"); }
        public void Close(IntPtr handle) { Check(Native.CloseHandle(handle), "job_close"); }
        private static void Check(bool value, string code) { if (!value) throw new JobApiException(code + ":" + Marshal.GetLastWin32Error()); }
        private static IntPtr CheckedHandle(IntPtr handle, string code) { Check(handle != IntPtr.Zero, code); return handle; }
    }

    internal static class Native
    {
        [StructLayout(LayoutKind.Sequential)] internal struct BasicLimits
        {
            internal long PerProcessTime, PerJobTime;
            internal uint LimitFlags;
            internal UIntPtr MinimumWorkingSet, MaximumWorkingSet;
            internal uint ActiveProcessLimit;
            internal UIntPtr Affinity;
            internal uint PriorityClass, SchedulingClass;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct IoCounters { internal ulong ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes; }
        [StructLayout(LayoutKind.Sequential)] internal struct ExtendedLimits
        {
            internal BasicLimits Basic;
            internal IoCounters Io;
            internal UIntPtr ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct Accounting
        {
            internal long User, Kernel, PeriodUser, PeriodKernel;
            internal uint Faults, TotalProcesses, ActiveProcesses, Terminated;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct Startup
        {
            internal uint Size;
            internal IntPtr Reserved, Desktop, Title;
            internal uint X, Y, Width, Height, XChars, YChars, Fill, Flags;
            internal ushort Show, ReservedBytes;
            internal IntPtr ReservedData, Input, Output, Error;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct ProcessInfo { internal IntPtr Process, Thread; internal uint Pid, Tid; }
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] internal static extern IntPtr CreateJobObjectW(IntPtr security, string name);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool SetInformationJobObject(IntPtr job, int kind, ref ExtendedLimits value, uint length);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool CreateProcessW(string application, StringBuilder command, IntPtr processSecurity, IntPtr threadSecurity, [MarshalAs(UnmanagedType.Bool)] bool inherit, uint flags, IntPtr environment, string directory, ref Startup startup, out ProcessInfo process);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool IsProcessInJob(IntPtr process, IntPtr job, [MarshalAs(UnmanagedType.Bool)] out bool member);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool QueryFullProcessImageNameW(IntPtr process, uint flags, StringBuilder path, ref uint size);
        [DllImport("kernel32.dll", SetLastError = true)] internal static extern uint ResumeThread(IntPtr thread);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool QueryInformationJobObject(IntPtr job, int kind, out Accounting value, uint length, out uint returned);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool GetExitCodeProcess(IntPtr process, out uint code);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool TerminateProcess(IntPtr process, uint code);
        [DllImport("kernel32.dll", SetLastError = true)] internal static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool TerminateJobObject(IntPtr job, uint code);
        [DllImport("kernel32.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] internal static extern bool CloseHandle(IntPtr handle);
    }
}
