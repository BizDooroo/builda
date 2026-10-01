package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Process ownership verification. After an agent restart the agent must decide
// whether the process group it started is gone. It never kills a PID it cannot
// prove it owns and never accepts new work on an unproven execution.
type orphanVerdict struct {
	Ended   bool
	Owned   bool
	Details string
}

// processStartToken returns a stable identity for a PID. It is only ever
// compared against the same PID, so its job is to change when that PID has
// been recycled by an unrelated process. On Linux it combines the boot ID with
// the kernel start time; elsewhere it falls back to the start timestamp
// reported by ps. Two processes launched in the same clock tick can share a
// token value, which is harmless here because the PID is part of the match.
func processStartToken(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid pid")
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		start, err := procStartTime(string(data))
		if err != nil {
			return "", err
		}
		boot := strings.TrimSpace(readFileString("/proc/sys/kernel/random/boot_id"))
		return "linux:" + boot + ":" + start, nil
	}
	output, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	start := strings.TrimSpace(string(output))
	if start == "" {
		return "", errors.New("process not found")
	}
	return "ps:" + start, nil
}

// procStartTime extracts field 22 of /proc/<pid>/stat, skipping the comm field
// which may itself contain spaces and parentheses.
func procStartTime(stat string) (string, error) {
	end := strings.LastIndex(stat, ")")
	if end < 0 || end+2 >= len(stat) {
		return "", errors.New("unexpected /proc stat format")
	}
	fields := strings.Fields(stat[end+2:])
	// After the comm field, state is field 3, so starttime (field 22) is at
	// index 19 of the remainder.
	if len(fields) < 20 {
		return "", errors.New("unexpected /proc stat field count")
	}
	return fields[19], nil
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// processGroupExists reports whether any process still belongs to a group.
// A permission error means the group exists but is not ours, which is treated
// as "exists" so the agent never assumes an unrelated group ended.
func processGroupExists(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, errors.New("invalid process group")
	}
	err := syscall.Kill(-pgid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	case errors.Is(err, syscall.EPERM):
		return true, errors.New("process group exists but is not owned by this agent")
	default:
		return true, err
	}
}

// verifyOrphan decides what happened to a process group the agent started
// before it restarted.
func verifyOrphan(entry *journalEntry) orphanVerdict {
	if entry.Phase != journalStarted {
		return orphanVerdict{Ended: true, Owned: true, Details: "the execution never reached process start"}
	}
	if entry.PID <= 0 || entry.PGID <= 0 {
		// The agent recorded the intent to start but crashed before it could
		// record the process identity, so nothing can be proven about it.
		return orphanVerdict{Ended: false, Owned: false, Details: "the agent crashed between launching the script and recording its process identity"}
	}
	if entry.ProcessToken == "" {
		// The identity was never recorded, so ownership cannot be proven
		// either way. Report that rather than guessing.
		groupExists, groupErr := processGroupExists(entry.PGID)
		if !groupExists && groupErr == nil {
			return orphanVerdict{Ended: true, Owned: true, Details: "no process identity was recorded, and process group " + strconv.Itoa(entry.PGID) + " no longer exists"}
		}
		details := "no process identity was recorded for pid " + strconv.Itoa(entry.PID) + ", so this process group cannot be proven to belong to this execution"
		if entry.ProcessTokenError != "" {
			details += ": " + entry.ProcessTokenError
		}
		return orphanVerdict{Ended: false, Owned: false, Details: details}
	}

	token, tokenErr := processStartToken(entry.PID)
	groupExists, groupErr := processGroupExists(entry.PGID)

	switch {
	case tokenErr != nil && !groupExists && groupErr == nil:
		return orphanVerdict{Ended: true, Owned: true, Details: "the leader process and its process group are gone"}
	case tokenErr == nil && token == entry.ProcessToken:
		return orphanVerdict{Ended: false, Owned: true, Details: "the original process group is still running and is provably owned by this execution"}
	case tokenErr == nil:
		return orphanVerdict{Ended: false, Owned: false, Details: "pid " + strconv.Itoa(entry.PID) + " was reused by an unrelated process"}
	case groupExists:
		details := "process group " + strconv.Itoa(entry.PGID) + " still has members that cannot be proven to belong to this execution"
		if groupErr != nil {
			details += ": " + groupErr.Error()
		}
		return orphanVerdict{Ended: false, Owned: false, Details: details}
	default:
		details := "process group state could not be determined"
		if groupErr != nil {
			details += ": " + groupErr.Error()
		}
		return orphanVerdict{Ended: false, Owned: false, Details: details}
	}
}

// killProcessGroup terminates a process group the agent provably owns.
func killProcessGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return errors.New("invalid process group")
	}
	return syscall.Kill(-pgid, signal)
}
