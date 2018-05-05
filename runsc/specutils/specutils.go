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

// Package specutils contains utility functions for working with OCI runtime
// specs.
package specutils

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"gvisor.googlesource.com/gvisor/pkg/abi/linux"
	"gvisor.googlesource.com/gvisor/pkg/log"
	"gvisor.googlesource.com/gvisor/pkg/sentry/kernel/auth"
)

// LogSpec logs the spec in a human-friendly way.
func LogSpec(spec *specs.Spec) {
	log.Debugf("Spec: %+v", spec)
	log.Debugf("Spec.Hooks: %+v", spec.Hooks)
	log.Debugf("Spec.Linux: %+v", spec.Linux)
	log.Debugf("Spec.Process: %+v", spec.Process)
	log.Debugf("Spec.Root: %+v", spec.Root)
}

// ReadSpec reads an OCI runtime spec from the given bundle directory.
//
// TODO: This should validate the spec.
func ReadSpec(bundleDir string) (*specs.Spec, error) {
	// The spec file must be in "config.json" inside the bundle directory.
	specFile := filepath.Join(bundleDir, "config.json")
	specBytes, err := ioutil.ReadFile(specFile)
	if err != nil {
		return nil, fmt.Errorf("error reading spec from file %q: %v", specFile, err)
	}
	var spec specs.Spec
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		return nil, fmt.Errorf("error unmarshaling spec from file %q: %v\n %s", specFile, err, string(specBytes))
	}
	return &spec, nil
}

// GetExecutablePath returns the absolute path to the executable, relative to
// the root.  It searches the environment PATH for the first file that exists
// with the given name.
func GetExecutablePath(exec, root string, env []string) (string, error) {
	exec = filepath.Clean(exec)

	// Don't search PATH if exec is a path to a file (absolute or relative).
	if strings.IndexByte(exec, '/') >= 0 {
		return exec, nil
	}

	// Get the PATH from the environment.
	const prefix = "PATH="
	var path []string
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			path = strings.Split(strings.TrimPrefix(e, prefix), ":")
			break
		}
	}

	// Search the PATH for a file whose name matches the one we are looking
	// for.
	for _, p := range path {
		abs := filepath.Join(root, p, exec)
		if _, err := os.Stat(abs); err == nil {
			// We found it!  Return the path relative to the root.
			return filepath.Join("/", p, exec), nil
		}
	}

	// Could not find a suitable path, just return the original string.
	log.Warningf("could not find executable %s in path %s", exec, path)
	return exec, nil
}

// Capabilities takes in spec and returns a TaskCapabilities corresponding to
// the spec.
func Capabilities(specCaps *specs.LinuxCapabilities) (*auth.TaskCapabilities, error) {
	var caps auth.TaskCapabilities
	if specCaps != nil {
		var err error
		if caps.BoundingCaps, err = capsFromNames(specCaps.Bounding); err != nil {
			return nil, err
		}
		if caps.EffectiveCaps, err = capsFromNames(specCaps.Effective); err != nil {
			return nil, err
		}
		if caps.InheritableCaps, err = capsFromNames(specCaps.Inheritable); err != nil {
			return nil, err
		}
		if caps.PermittedCaps, err = capsFromNames(specCaps.Permitted); err != nil {
			return nil, err
		}
		// TODO: Support ambient capabilities.
	}
	return &caps, nil
}

var capFromName = map[string]linux.Capability{
	"CAP_CHOWN":            linux.CAP_CHOWN,
	"CAP_DAC_OVERRIDE":     linux.CAP_DAC_OVERRIDE,
	"CAP_DAC_READ_SEARCH":  linux.CAP_DAC_READ_SEARCH,
	"CAP_FOWNER":           linux.CAP_FOWNER,
	"CAP_FSETID":           linux.CAP_FSETID,
	"CAP_KILL":             linux.CAP_KILL,
	"CAP_SETGID":           linux.CAP_SETGID,
	"CAP_SETUID":           linux.CAP_SETUID,
	"CAP_SETPCAP":          linux.CAP_SETPCAP,
	"CAP_LINUX_IMMUTABLE":  linux.CAP_LINUX_IMMUTABLE,
	"CAP_NET_BIND_SERVICE": linux.CAP_NET_BIND_SERVICE,
	"CAP_NET_BROADCAST":    linux.CAP_NET_BROADCAST,
	"CAP_NET_ADMIN":        linux.CAP_NET_ADMIN,
	"CAP_NET_RAW":          linux.CAP_NET_RAW,
	"CAP_IPC_LOCK":         linux.CAP_IPC_LOCK,
	"CAP_IPC_OWNER":        linux.CAP_IPC_OWNER,
	"CAP_SYS_MODULE":       linux.CAP_SYS_MODULE,
	"CAP_SYS_RAWIO":        linux.CAP_SYS_RAWIO,
	"CAP_SYS_CHROOT":       linux.CAP_SYS_CHROOT,
	"CAP_SYS_PTRACE":       linux.CAP_SYS_PTRACE,
	"CAP_SYS_PACCT":        linux.CAP_SYS_PACCT,
	"CAP_SYS_ADMIN":        linux.CAP_SYS_ADMIN,
	"CAP_SYS_BOOT":         linux.CAP_SYS_BOOT,
	"CAP_SYS_NICE":         linux.CAP_SYS_NICE,
	"CAP_SYS_RESOURCE":     linux.CAP_SYS_RESOURCE,
	"CAP_SYS_TIME":         linux.CAP_SYS_TIME,
	"CAP_SYS_TTY_CONFIG":   linux.CAP_SYS_TTY_CONFIG,
	"CAP_MKNOD":            linux.CAP_MKNOD,
	"CAP_LEASE":            linux.CAP_LEASE,
	"CAP_AUDIT_WRITE":      linux.CAP_AUDIT_WRITE,
	"CAP_AUDIT_CONTROL":    linux.CAP_AUDIT_CONTROL,
	"CAP_SETFCAP":          linux.CAP_SETFCAP,
	"CAP_MAC_OVERRIDE":     linux.CAP_MAC_OVERRIDE,
	"CAP_MAC_ADMIN":        linux.CAP_MAC_ADMIN,
	"CAP_SYSLOG":           linux.CAP_SYSLOG,
	"CAP_WAKE_ALARM":       linux.CAP_WAKE_ALARM,
	"CAP_BLOCK_SUSPEND":    linux.CAP_BLOCK_SUSPEND,
	"CAP_AUDIT_READ":       linux.CAP_AUDIT_READ,
}

func capsFromNames(names []string) (auth.CapabilitySet, error) {
	var caps []linux.Capability
	for _, n := range names {
		c, ok := capFromName[n]
		if !ok {
			return 0, fmt.Errorf("unknown capability %q", n)
		}
		caps = append(caps, c)
	}
	return auth.CapabilitySetOfMany(caps), nil
}

// Is9PMount returns true if the given mount can be mounted as an external gofer.
func Is9PMount(m specs.Mount) bool {
	return m.Type == "bind" && m.Source != "" && !strings.HasPrefix(m.Destination, "/dev")
}

// BinPath returns the real path to self, resolving symbolink links. This is done
// to make the process name appears as 'runsc', instead of 'exe'.
func BinPath() (string, error) {
	binPath, err := filepath.EvalSymlinks("/proc/self/exe")
	if err != nil {
		return "", fmt.Errorf(`error resolving "/proc/self/exe" symlink: %v`, err)
	}
	return binPath, nil
}

// WaitForReady waits for a process to become ready. The process is ready when
// the 'ready' function returns true. It continues to wait if 'ready' returns
// false. It returns error on timeout, if the process stops or if 'ready' fails.
func WaitForReady(pid int, timeout time.Duration, ready func() (bool, error)) error {
	backoff := 1 * time.Millisecond
	for start := time.Now(); time.Now().Sub(start) < timeout; {
		if ok, err := ready(); err != nil {
			return err
		} else if ok {
			return nil
		}

		// Check if the process is still running.
		var ws syscall.WaitStatus
		var ru syscall.Rusage
		child, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, &ru)
		if err != nil || child == pid {
			return fmt.Errorf("process (%d) is not running, err: %v", pid, err)
		}

		// Process continues to run, backoff and retry.
		time.Sleep(backoff)
		backoff *= 2
		if backoff > 1*time.Second {
			backoff = 1 * time.Second
		}
	}
	return fmt.Errorf("timed out waiting for process (%d)", pid)
}
