// Copyright 2018 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"syscall"
	"text/tabwriter"
	"time"

	"gvisor.googlesource.com/gvisor/pkg/abi/linux"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs/host"
	"gvisor.googlesource.com/gvisor/pkg/sentry/kernel"
	"gvisor.googlesource.com/gvisor/pkg/sentry/kernel/auth"
	"gvisor.googlesource.com/gvisor/pkg/sentry/kernel/kdefs"
	ktime "gvisor.googlesource.com/gvisor/pkg/sentry/kernel/time"
	"gvisor.googlesource.com/gvisor/pkg/sentry/limits"
	"gvisor.googlesource.com/gvisor/pkg/sentry/usage"
	"gvisor.googlesource.com/gvisor/pkg/urpc"
)

// Proc includes task-related functions.
//
// At the moment, this is limited to exec support.
type Proc struct {
	Kernel *kernel.Kernel
}

// ExecArgs is the set of arguments to exec.
type ExecArgs struct {
	// Filename is the filename to load.
	//
	// If this is provided as "", then the file will be guessed via Argv[0].
	Filename string `json:"filename"`

	// Argv is a list of arguments.
	Argv []string `json:"argv"`

	// Envv is a list of environment variables.
	Envv []string `json:"envv"`

	// WorkingDirectory defines the working directory for the new process.
	WorkingDirectory string `json:"wd"`

	// KUID is the UID to run with in the root user namespace. Defaults to
	// root if not set explicitly.
	KUID auth.KUID

	// KGID is the GID to run with in the root user namespace. Defaults to
	// the root group if not set explicitly.
	KGID auth.KGID

	// ExtraKGIDs is the list of additional groups to which the user
	// belongs.
	ExtraKGIDs []auth.KGID

	// Capabilities is the list of capabilities to give to the process.
	Capabilities *auth.TaskCapabilities

	// FilePayload determines the files to give to the new process.
	urpc.FilePayload
}

// Exec runs a new task.
func (proc *Proc) Exec(args *ExecArgs, waitStatus *uint32) error {
	// Import file descriptors.
	l := limits.NewLimitSet()
	fdm := proc.Kernel.NewFDMap()
	defer fdm.DecRef()

	creds := auth.NewUserCredentials(
		args.KUID,
		args.KGID,
		args.ExtraKGIDs,
		args.Capabilities,
		proc.Kernel.RootUserNamespace())

	initArgs := kernel.CreateProcessArgs{
		Filename:             args.Filename,
		Argv:                 args.Argv,
		Envv:                 args.Envv,
		WorkingDirectory:     args.WorkingDirectory,
		Credentials:          creds,
		FDMap:                fdm,
		Umask:                0022,
		Limits:               l,
		MaxSymlinkTraversals: linux.MaxSymlinkTraversals,
		UTSNamespace:         proc.Kernel.RootUTSNamespace(),
		IPCNamespace:         proc.Kernel.RootIPCNamespace(),
	}
	ctx := initArgs.NewContext(proc.Kernel)
	mounter := fs.FileOwnerFromContext(ctx)

	for appFD, f := range args.FilePayload.Files {
		// Copy the underlying FD.
		newFD, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			return err
		}
		f.Close()

		// Install the given file as an FD.
		file, err := host.NewFile(ctx, newFD, mounter)
		if err != nil {
			syscall.Close(newFD)
			return err
		}
		defer file.DecRef()
		if err := fdm.NewFDAt(kdefs.FD(appFD), file, kernel.FDFlags{}, l); err != nil {
			return err
		}
	}

	// Start the new task.
	newTG, err := proc.Kernel.CreateProcess(initArgs)
	if err != nil {
		return err
	}

	// Wait for completion.
	newTG.WaitExited()
	*waitStatus = newTG.ExitStatus().Status()
	return nil
}

// PsArgs is the set of arguments to ps.
type PsArgs struct {
	// JSON will force calls to Ps to return the result as a JSON payload.
	JSON bool
}

// Ps provides a process listing for the running kernel.
func (proc *Proc) Ps(args *PsArgs, out *string) error {
	var p []*Process
	if e := Processes(proc.Kernel, &p); e != nil {
		return e
	}
	if !args.JSON {
		*out = ProcessListToTable(p)
	} else {
		s, e := ProcessListToJSON(p)
		if e != nil {
			return e
		}
		*out = s
	}
	return nil
}

// Process contains information about a single process in a Sandbox.
// TODO: Implement TTY field.
type Process struct {
	UID auth.KUID       `json:"uid"`
	PID kernel.ThreadID `json:"pid"`
	// Parent PID
	PPID kernel.ThreadID `json:"ppid"`
	// Processor utilization
	C int32 `json:"c"`
	// Start time
	STime string `json:"stime"`
	// CPU time
	Time string `json:"time"`
	// Executable shortname (e.g. "sh" for /bin/sh)
	Cmd string `json:"cmd"`
}

// ProcessListToTable prints a table with the following format:
// UID       PID       PPID      C         STIME     TIME       CMD
// 0         1         0         0         14:04     505262ns   tail
func ProcessListToTable(pl []*Process) string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 10, 1, 3, ' ', 0)
	fmt.Fprint(tw, "UID\tPID\tPPID\tC\tSTIME\tTIME\tCMD")
	for _, d := range pl {
		fmt.Fprintf(tw, "\n%d\t%d\t%d\t%d\t%s\t%s\t%s",
			d.UID,
			d.PID,
			d.PPID,
			d.C,
			d.STime,
			d.Time,
			d.Cmd)
	}
	tw.Flush()
	return buf.String()
}

// ProcessListToJSON will return the JSON representation of ps.
func ProcessListToJSON(pl []*Process) (string, error) {
	b, err := json.Marshal(pl)
	if err != nil {
		return "", fmt.Errorf("couldn't marshal process list %v: %v", pl, err)
	}
	return string(b), nil
}

// PrintPIDsJSON prints a JSON object containing only the PIDs in pl. This
// behavior is the same as runc's.
func PrintPIDsJSON(pl []*Process) (string, error) {
	pids := make([]kernel.ThreadID, 0, len(pl))
	for _, d := range pl {
		pids = append(pids, d.PID)
	}
	b, err := json.Marshal(pids)
	if err != nil {
		return "", fmt.Errorf("couldn't marshal PIDs %v: %v", pids, err)
	}
	return string(b), nil
}

// Processes retrieves information about processes running in the sandbox.
func Processes(k *kernel.Kernel, out *[]*Process) error {
	ts := k.TaskSet()
	now := k.RealtimeClock().Now()
	for _, tg := range ts.Root.ThreadGroups() {
		pid := ts.Root.IDOfThreadGroup(tg)
		// If tg has already been reaped ignore it.
		if pid == 0 {
			continue
		}

		*out = append(*out, &Process{
			UID: tg.Leader().Credentials().EffectiveKUID,
			PID: pid,
			// If Parent is null (i.e. tg is the init process), PPID will be 0.
			PPID:  ts.Root.IDOfTask(tg.Leader().Parent()),
			STime: formatStartTime(now, tg.Leader().StartTime()),
			C:     percentCPU(tg.CPUStats(), tg.Leader().StartTime(), now),
			Time:  tg.CPUStats().SysTime.String(),
			Cmd:   tg.Leader().Name(),
		})
	}
	return nil
}

// formatStartTime formats startTime depending on the current time:
// - If startTime was today, HH:MM is used.
// - If startTime was not today but was this year, MonDD is used (e.g. Jan02)
// - If startTime was not this year, the year is used.
func formatStartTime(now, startTime ktime.Time) string {
	nowS, nowNs := now.Unix()
	n := time.Unix(nowS, nowNs)
	startTimeS, startTimeNs := startTime.Unix()
	st := time.Unix(startTimeS, startTimeNs)
	format := "15:04"
	if st.YearDay() != n.YearDay() {
		format = "Jan02"
	}
	if st.Year() != n.Year() {
		format = "2006"
	}
	return st.Format(format)
}

func percentCPU(stats usage.CPUStats, startTime, now ktime.Time) int32 {
	// Note: In procps, there is an option to include child CPU stats. As
	// it is disabled by default, we do not include them.
	total := stats.UserTime + stats.SysTime
	lifetime := now.Sub(startTime)
	if lifetime <= 0 {
		return 0
	}
	percentCPU := total * 100 / lifetime
	// Cap at 99% since procps does the same.
	if percentCPU > 99 {
		percentCPU = 99
	}
	return int32(percentCPU)
}
