// Hosted-CI product removal policy. The common inert entry point is unchanged.
namespace Arkism.InstallerChecks
{
    using System;
    using System.Collections.Generic;
    using System.IO;
    using System.Runtime.InteropServices;
    using System.Text;
    using System.Text.RegularExpressions;

    public sealed class ProductUninstallBinding
    {
        public readonly string Root, Owner, SourceSha, Install, Setup, SetupHash, UninstallerHash;
        public ProductUninstallBinding(string root, string owner, string sourceSha, string install,
            string setup, string setupHash, string uninstallerHash)
        {
            Root = root; Owner = owner; SourceSha = sourceSha; Install = install;
            Setup = setup; SetupHash = setupHash; UninstallerHash = uninstallerHash;
        }
        internal JobRequest Request()
        {
            return new JobRequest { Root = Root, Owner = Owner, SourceSha = SourceSha,
                Executable = Path.Combine(Install, "uninstall.exe"), ExpectedSha256 = UninstallerHash };
        }
    }

    public static class ProductUninstallCheck
    {
        // verifyProductState is the sibling coordinator's complete same-build/global policy.
        // It is required both before creating an adapter and immediately before resume.
        public static JobResult RunWindows(ProductUninstallBinding binding, Action<long, string> verifyProductState)
        {
            if (binding == null) throw new JobApiException("product_binding_missing");
            return RunCore(binding.Request(), delegate { ValidateWindows(binding); }, verifyProductState,
                delegate { return new ProductNativeApi(binding); }, new StopwatchClock());
        }

        internal static JobResult RunCore(JobRequest request, Action verifyBinding, Action<long, string> verifyProductState,
            Func<IJobApi> createApi, IMonotonicClock clock)
        {
            if (verifyBinding == null || verifyProductState == null || createApi == null)
                throw new JobApiException("product_policy_missing");
            verifyBinding(); verifyProductState(0, "");
            return WindowsProcessJob.RunCore(request,
                new ProductPolicyApi(createApi(), request.Executable, verifyBinding, verifyProductState), clock);
        }

        public static void RequireLiteralInstallPath(string path)
        {
            if (path == null || !Regex.IsMatch(path, @"\A[A-Za-z]:\\[A-Za-z0-9 _.-]+(?:\\[A-Za-z0-9 _.-]+)*\z"))
                throw new JobApiException("product_install_path_unsupported");
            foreach (string part in path.Substring(3).Split('\\'))
                if (part == "." || part == ".." || part.EndsWith(".") || part.EndsWith(" "))
                    throw new JobApiException("product_install_path_unsupported");
        }

        public static void RequireSourceIdentity(string actual, string expected)
        {
            if (actual == null || !Regex.IsMatch(actual, @"\A[0-9a-f]{40}\z") || actual != expected)
                throw new JobApiException("product_source_mismatch");
        }

        private static void ValidateWindows(ProductUninstallBinding value)
        {
            var request = value.Request();
            RequireSourceIdentity(value.SourceSha, Environment.GetEnvironmentVariable("GITHUB_SHA"));
            WindowsGuard.Context(request);
            RequireLiteralInstallPath(value.Install);
            if (value.Install != Path.Combine(value.Root, "installed Arkism"))
                throw new JobApiException("product_install_binding_invalid");
            WindowsGuard.VerifyFile(request);
            WindowsGuard.Ordinary(value.Setup, false);
            if (WindowsGuard.Within(value.Setup, value.Root) || WindowsGuard.Hash(value.Setup) != value.SetupHash)
                throw new JobApiException("product_setup_binding_invalid");
            PendingEchoVerifier.ReadNativeMachine(request.Executable);
            foreach (string path in new string[] { "profile", @"profile\AppData\Roaming", @"profile\AppData\Local", @"profile\.config" })
                WindowsGuard.Ordinary(Path.Combine(value.Root, path), true);
        }

        internal static string EnvironmentBlock(ProductUninstallBinding value)
        {
            var entries = new SortedDictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            foreach (string name in new string[] { "SystemRoot", "WINDIR", "SystemDrive", "ComSpec", "PATHEXT", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS" })
            {
                string item = Environment.GetEnvironmentVariable(name);
                if (!String.IsNullOrEmpty(item)) entries[name] = item;
            }
            string system = Environment.GetEnvironmentVariable("SystemRoot");
            entries["PATH"] = system + @"\System32;" + system + @"\System32\WindowsPowerShell\v1.0";
            entries["HOME"] = entries["USERPROFILE"] = Path.Combine(value.Root, "profile");
            entries["APPDATA"] = Path.Combine(value.Root, @"profile\AppData\Roaming");
            entries["LOCALAPPDATA"] = Path.Combine(value.Root, @"profile\AppData\Local");
            entries["XDG_CONFIG_HOME"] = Path.Combine(value.Root, @"profile\.config");
            entries["TEMP"] = entries["TMP"] = Path.Combine(value.Root, "temp");
            var block = new StringBuilder();
            foreach (var pair in entries)
            {
                if (pair.Value.IndexOf('\0') >= 0) throw new JobApiException("product_environment_invalid");
                block.Append(pair.Key).Append('=').Append(pair.Value).Append('\0');
            }
            return block.Append('\0').ToString();
        }
    }

    internal sealed class ProductProcessIdentity
    {
        internal readonly long ProcessId;
        internal readonly string Image;
        internal ProductProcessIdentity(long processId, string image) { ProcessId = processId; Image = image; }
    }

    internal interface IProductProcessIdentity
    {
        ProductProcessIdentity GetVerifiedIdentity(IntPtr process);
    }

    internal sealed class ProductPolicyApi : IJobApi
    {
        private readonly IJobApi inner;
        private readonly Action binding;
        private readonly Action<long, string> policy;
        private readonly string expectedImage;
        internal ProductPolicyApi(IJobApi api, string image, Action verifyBinding, Action<long, string> verifyPolicy)
        { inner = api; expectedImage = image; binding = verifyBinding; policy = verifyPolicy; }
        public IntPtr CreateJob() { return inner.CreateJob(); }
        public void Configure(IntPtr job) { inner.Configure(job); }
        public ProcessHandles CreateSuspended(JobRequest request) { return inner.CreateSuspended(request); }
        public void Assign(IntPtr job, IntPtr process) { inner.Assign(job, process); }
        public bool IsMember(IntPtr job, IntPtr process) { return inner.IsMember(job, process); }
        public void VerifyImage(JobRequest request, IntPtr process)
        {
            inner.VerifyImage(request, process); binding();
            var source = inner as IProductProcessIdentity;
            if (source == null) throw new JobApiException("product_process_identity_missing");
            var identity = source.GetVerifiedIdentity(process);
            if (identity == null || identity.ProcessId <= 0 || identity.ProcessId > UInt32.MaxValue ||
                String.IsNullOrEmpty(expectedImage) ||
                !String.Equals(identity.Image, expectedImage, StringComparison.OrdinalIgnoreCase))
                throw new JobApiException("product_process_identity_invalid");
            policy(identity.ProcessId, identity.Image);
        }
        public uint Resume(IntPtr thread) { return inner.Resume(thread); }
        public JobAccounting Query(IntPtr job) { return inner.Query(job); }
        public uint ExitCode(IntPtr process) { return inner.ExitCode(process); }
        public void TerminateRoot(IntPtr process) { inner.TerminateRoot(process); }
        public void ReapRoot(IntPtr process, int milliseconds) { inner.ReapRoot(process, milliseconds); }
        public void TerminateJob(IntPtr job) { inner.TerminateJob(job); }
        public void Close(IntPtr handle) { inner.Close(handle); }
    }

    internal sealed class ProductNativeApi : IJobApi, IProductProcessIdentity
    {
        private readonly ProductUninstallBinding binding;
        private readonly NativeJobApi jobs = new NativeJobApi();
        private IntPtr createdProcess, assignedJob, verifiedProcess;
        private bool membershipVerified;
        private string verifiedImage;
        internal ProductNativeApi(ProductUninstallBinding value) { binding = value; }
        public IntPtr CreateJob() { return jobs.CreateJob(); }
        public void Configure(IntPtr job) { jobs.Configure(job); }
        public ProcessHandles CreateSuspended(JobRequest request)
        {
            var startup = new Native.Startup(); startup.Size = (uint)Marshal.SizeOf(typeof(Native.Startup));
            Native.ProcessInfo process;
            IntPtr environment = Marshal.StringToHGlobalUni(ProductUninstallCheck.EnvironmentBlock(binding));
            try
            {
                string executable = binding.Request().Executable;
                var command = new StringBuilder("\"" + executable + "\" /S");
                Check(Native.CreateProcessW(executable, command, IntPtr.Zero, IntPtr.Zero, false,
                    0x00000004 | 0x00000400 | 0x08000000, environment, Path.Combine(binding.Root, "temp"), ref startup, out process), "product_create_suspended");
            }
            finally { Marshal.FreeHGlobal(environment); }
            createdProcess = process.Process;
            return new ProcessHandles { Process = process.Process, Thread = process.Thread };
        }
        public void Assign(IntPtr job, IntPtr process)
        {
            if (process != createdProcess) throw new JobApiException("product_process_not_owned");
            jobs.Assign(job, process); assignedJob = job;
        }
        public bool IsMember(IntPtr job, IntPtr process)
        {
            bool member = jobs.IsMember(job, process);
            membershipVerified = member && job == assignedJob && process == createdProcess;
            return membershipVerified;
        }
        public void VerifyImage(JobRequest request, IntPtr process)
        {
            RequireOwnedMember(process);
            var image = new StringBuilder(32768); uint size = (uint)image.Capacity;
            Check(Native.QueryFullProcessImageNameW(process, 0, image, ref size), "product_image_query");
            if (!String.Equals(WindowsGuard.Full(image.ToString()), WindowsGuard.Full(binding.Request().Executable), StringComparison.OrdinalIgnoreCase))
                throw new JobApiException("product_image_changed");
            verifiedProcess = process; verifiedImage = WindowsGuard.Full(binding.Request().Executable);
        }
        public ProductProcessIdentity GetVerifiedIdentity(IntPtr process)
        {
            RequireOwnedMember(process);
            if (process != verifiedProcess) throw new JobApiException("product_image_not_verified");
            uint pid = ProductProcessNative.GetProcessId(process);
            Check(pid != 0, "product_process_id_query");
            return new ProductProcessIdentity(pid, verifiedImage);
        }
        private void RequireOwnedMember(IntPtr process)
        {
            if (process == IntPtr.Zero || process != createdProcess || assignedJob == IntPtr.Zero || !membershipVerified)
                throw new JobApiException("product_process_not_owned_member");
        }
        public uint Resume(IntPtr thread) { return jobs.Resume(thread); }
        public JobAccounting Query(IntPtr job) { return jobs.Query(job); }
        public uint ExitCode(IntPtr process) { return jobs.ExitCode(process); }
        public void TerminateRoot(IntPtr process) { jobs.TerminateRoot(process); }
        public void ReapRoot(IntPtr process, int milliseconds) { jobs.ReapRoot(process, milliseconds); }
        public void TerminateJob(IntPtr job) { jobs.TerminateJob(job); }
        public void Close(IntPtr handle) { jobs.Close(handle); }
        private static void Check(bool value, string code)
        { if (!value) throw new JobApiException(code + ":" + Marshal.GetLastWin32Error()); }
    }

    internal static class ProductProcessNative
    {
        [DllImport("kernel32.dll", SetLastError = true)] internal static extern uint GetProcessId(IntPtr process);
    }
}
