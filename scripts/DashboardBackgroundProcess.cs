using System;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;

namespace HemSoft
{
    // Direct file handles keep logs working after the short-lived launcher exits.
    // Start-Process -WindowStyle Hidden/-NoNewWindow does not set CREATE_NO_WINDOW.
    public static class DashboardBackgroundProcess
    {
        [StructLayout(LayoutKind.Sequential)]
        private struct SecurityAttributes
        {
            public int Length;
            public IntPtr Descriptor;
            [MarshalAs(UnmanagedType.Bool)] public bool Inherit;
        }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        private struct StartupInfo
        {
            public int Size;
            public string Reserved, Desktop, Title;
            public int X, Y, Width, Height, XChars, YChars, Fill, Flags;
            public short ShowWindow, ReservedSize;
            public IntPtr ReservedBytes, Input, Output, Error;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct ProcessInfo
        {
            public IntPtr Process, Thread;
            public int ProcessId, ThreadId;
        }

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern SafeFileHandle CreateFile(string path, uint access, uint share,
            ref SecurityAttributes attributes, uint disposition, uint flags, IntPtr template);

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool CreateProcess(string application, StringBuilder command,
            IntPtr processAttributes, IntPtr threadAttributes, bool inheritHandles, uint flags,
            IntPtr environment, string directory, ref StartupInfo startup, out ProcessInfo process);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern uint ResumeThread(IntPtr thread);

        [DllImport("kernel32.dll")]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool TerminateProcess(IntPtr process, uint code);

        [DllImport("kernel32.dll")]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool CloseHandle(IntPtr handle);

        public static string Quote(string value)
        {
            // CommandLineToArgvW/CRT quoting, including quotes and trailing slashes.
            var result = new StringBuilder("\"");
            int slashes = 0;
            foreach (char c in value)
            {
                if (c == '\\') { slashes++; continue; }
                result.Append('\\', c == '"' ? slashes * 2 + 1 : slashes);
                result.Append(c);
                slashes = 0;
            }
            return result.Append('\\', slashes * 2).Append('"').ToString();
        }

        private static SafeFileHandle OpenFile(string path, uint access, uint disposition)
        {
            var security = new SecurityAttributes();
            security.Length = Marshal.SizeOf(typeof(SecurityAttributes));
            security.Inherit = true;
            var file = CreateFile(path, access, 3, ref security, disposition, 0x80, IntPtr.Zero);
            if (file.IsInvalid)
            {
                int error = Marshal.GetLastWin32Error();
                file.Dispose();
                throw new Win32Exception(error, "Cannot open dashboard process stream");
            }
            return file;
        }

        public static Process Start(string executable, string[] arguments, string directory,
            string stdout, string stderr)
        {
            if (Environment.OSVersion.Platform != PlatformID.Win32NT)
                throw new PlatformNotSupportedException("The dashboard launcher requires Windows");
            var command = new StringBuilder(Quote(executable));
            foreach (string argument in arguments) command.Append(' ').Append(Quote(argument));
            using (var input = OpenFile("NUL", 0x80000000, 3))
            using (var output = OpenFile(stdout, 0x40000000, 2))
            using (var error = OpenFile(stderr, 0x40000000, 2))
            {
                var startup = new StartupInfo();
                startup.Size = Marshal.SizeOf(typeof(StartupInfo));
                startup.Flags = 0x100; // STARTF_USESTDHANDLES; no shell or window-style fallback.
                startup.Input = input.DangerousGetHandle();
                startup.Output = output.DangerousGetHandle();
                startup.Error = error.DangerousGetHandle();
                ProcessInfo native;
                // CREATE_NO_WINDOW | CREATE_SUSPENDED. No console is allocated.
                if (!CreateProcess(executable, command, IntPtr.Zero, IntPtr.Zero, true,
                    0x08000004, IntPtr.Zero, directory, ref startup, out native))
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "Windowless dashboard launch failed");
                Process process = null;
                try
                {
                    process = Process.GetProcessById(native.ProcessId);
                    // Acquire the original process handle before it can run or exit.
                    IntPtr handle = process.Handle;
                    if (ResumeThread(native.Thread) == UInt32.MaxValue)
                        throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot resume dashboard process");
                    return process;
                }
                catch
                {
                    TerminateProcess(native.Process, 1);
                    if (process != null) process.Dispose();
                    throw;
                }
                finally
                {
                    CloseHandle(native.Thread);
                    CloseHandle(native.Process);
                }
            }
        }
    }
}
