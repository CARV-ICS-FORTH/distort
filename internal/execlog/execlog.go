// Package execlog provides thin wrappers around os/exec that log every command
// the program tries to run, along with its output on both success and failure.
//
// All logging goes to the kernel log (/dev/kmsg), so messages are visible via
// `dmesg` (provided the process has permission to write to /dev/kmsg).
package execlog

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Run builds and executes name+args, logging the command line before running
// and the combined (stdout+stderr) output afterwards. It returns the combined
// output and error so callers can still inspect the result (e.g. to grep the
// output for "already connected").
func Run(name string, args ...string) ([]byte, error) {
	return RunCmd(exec.Command(name, args...))
}

// RunCmd runs an already-constructed *exec.Cmd with the same logging behaviour
// as Run. Use it for commands built with `bash -c ...` or with a custom Env.
func RunCmd(cmd *exec.Cmd) ([]byte, error) {
	line := commandLine(cmd)
	logKernel(6, "[exec] running: %s", line)

	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimRight(string(out), "\n")

	if err != nil {
		logKernel(3, "[exec] FAILED: %s\n  error: %v\n  output: %s", line, err, trimmed)
		return out, err
	}

	logKernel(6, "[exec] OK: %s\n  output: %s", line, trimmed)
	return out, nil
}

// LogStart logs a command that is about to be launched as a long-running
// background process via cmd.Start(). Combined output cannot be captured for
// these, so only the command line is logged.
func LogStart(cmd *exec.Cmd) {
	logKernel(6, "[exec] starting (background): %s", commandLine(cmd))
}

func commandLine(cmd *exec.Cmd) string {
	return strings.Join(cmd.Args, " ")
}

// logKernel writes a message to the kernel log (/dev/kmsg).
// Priority values:
//
//	0 = emerg
//	1 = alert
//	2 = crit
//	3 = err
//	4 = warning
//	5 = notice
//	6 = info
//	7 = debug
func logKernel(priority int, format string, args ...any) {
	f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "<%d>execlog: %s\n", priority, fmt.Sprintf(format, args...))
}
