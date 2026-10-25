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

        [StructLayout(LayoutKind.Sequential)]
        private struct StartupInfoEx
        {
            public StartupInfo Startup;
            public IntPtr Attributes;
        }

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern SafeFileHandle CreateFile(string path, uint access, uint share,
            ref SecurityAttributes attributes, uint disposition, uint flags, IntPtr template);

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool CreateProcess(string application, StringBuilder command,
            IntPtr processAttributes, IntPtr threadAttributes, bool inheritHandles, uint flags,
            IntPtr environment, string directory, ref StartupInfoEx startup, out ProcessInfo process);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool InitializeProcThreadAttributeList(IntPtr list, int count,
            int flags, ref IntPtr size);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool UpdateProcThreadAttribute(IntPtr list, uint flags,
            IntPtr attribute, IntPtr value, IntPtr size, IntPtr previous, IntPtr returnedSize);

        [DllImport("kernel32.dll")]
        private static extern void DeleteProcThreadAttributeList(IntPtr list);

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
                var startup = new StartupInfoEx();
                startup.Startup.Size = Marshal.SizeOf(typeof(StartupInfoEx));
                startup.Startup.Flags = 0x100; // STARTF_USESTDHANDLES.
                startup.Startup.Input = input.DangerousGetHandle();
                startup.Startup.Output = output.DangerousGetHandle();
                startup.Startup.Error = error.DangerousGetHandle();
                IntPtr size = IntPtr.Zero;
                InitializeProcThreadAttributeList(IntPtr.Zero, 1, 0, ref size);
                if (size == IntPtr.Zero)
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot size dashboard handle list");
                startup.Attributes = Marshal.AllocHGlobal(size);
                IntPtr handles = IntPtr.Zero;
                bool initialized = false;
                try
                {
                    if (!InitializeProcThreadAttributeList(startup.Attributes, 1, 0, ref size))
                        throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot initialize dashboard handle list");
                    initialized = true;
                    handles = Marshal.AllocHGlobal(3 * IntPtr.Size);
                    Marshal.Copy(new[] { startup.Startup.Input, startup.Startup.Output, startup.Startup.Error }, 0, handles, 3);
                    // Inherit only these streams, never the launcher's unrelated pipe writers.
                    if (!UpdateProcThreadAttribute(startup.Attributes, 0, new IntPtr(0x20002), handles,
                        new IntPtr(3 * IntPtr.Size), IntPtr.Zero, IntPtr.Zero))
                        throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot restrict dashboard handle inheritance");
                    ProcessInfo native;
                    // CREATE_NO_WINDOW | CREATE_SUSPENDED | EXTENDED_STARTUPINFO_PRESENT.
                    if (!CreateProcess(executable, command, IntPtr.Zero, IntPtr.Zero, true,
                        0x08080004, IntPtr.Zero, directory, ref startup, out native))
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
                finally
                {
                    if (initialized) DeleteProcThreadAttributeList(startup.Attributes);
                    if (handles != IntPtr.Zero) Marshal.FreeHGlobal(handles);
                    Marshal.FreeHGlobal(startup.Attributes);
                }
            }
        }
    }
}
