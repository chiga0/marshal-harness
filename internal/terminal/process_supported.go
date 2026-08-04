//go:build darwin || linux

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type nativeProcessController struct{}

func defaultProcessController() processController { return nativeProcessController{} }
func (nativeProcessController) Supported() bool   { return true }

func (nativeProcessController) GroupID(pid int) (int, error) {
	if pid <= 1 {
		return 0, ErrAmbiguousProcess
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 1 {
		return 0, ErrAmbiguousProcess
	}
	return pgid, nil
}

func (nativeProcessController) Pause(pid int) ([]int, error) {
	return pauseProcessTree(pid)
}

func (nativeProcessController) Resume(pid int, groups []int) error {
	rootGroup, err := syscall.Getpgid(pid)
	if err != nil || len(groups) == 0 {
		return ErrAmbiguousProcess
	}
	return signalGroups(groups, rootGroup, syscall.SIGCONT)
}

func (nativeProcessController) Terminate(ctx context.Context, pid int, grace time.Duration) error {
	if pid <= 1 || grace < 0 {
		return ErrInvalidRequest
	}
	groups, err := pauseProcessTree(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	rootGroup, err := syscall.Getpgid(pid)
	if err != nil {
		return nil
	}
	if err := signalGroups(groups, rootGroup, syscall.SIGTERM); err != nil {
		return err
	}
	// SIGTERM remains pending for stopped processes; SIGCONT lets them handle
	// it without leaving separately grouped tool processes frozen forever.
	_ = signalGroups(groups, rootGroup, syscall.SIGCONT)
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !anyGroupAlive(groups) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err := signalGroups(groups, rootGroup, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		case <-ticker.C:
		}
	}
}

type processEntry struct {
	pid, parent, group int
	state              string
}

// pauseProcessTree targets the root foreground group and every descendant
// process group. Agent tools frequently create their own PGID, so controlling
// only the root group would falsely report pause while tools keep running.
func pauseProcessTree(root int) ([]int, error) {
	if root <= 1 {
		return nil, ErrAmbiguousProcess
	}
	allGroups, rootGroup := map[int]bool{}, 0
	for round := 0; round < 4; round++ {
		groups, currentRootGroup, found, err := processGroups(root)
		if err != nil {
			return nil, err
		}
		if !found {
			if round == 0 {
				return nil, syscall.ESRCH
			}
			break
		}
		rootGroup = currentRootGroup
		for _, group := range groups {
			allGroups[group] = true
		}
		merged := make([]int, 0, len(allGroups))
		for group := range allGroups {
			merged = append(merged, group)
		}
		sort.Ints(merged)
		if err := signalGroups(merged, rootGroup, syscall.SIGSTOP); err != nil {
			return nil, err
		}
	}
	result := make([]int, 0, len(allGroups))
	for group := range allGroups {
		result = append(result, group)
	}
	sort.Ints(result)
	return result, nil
}

func processTable() ([]processEntry, error) {
	command := exec.Command("/bin/ps", "-axo", "pid=,ppid=,pgid=,state=")
	command.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	entries := make([]processEntry, 0, 256)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		group, groupErr := strconv.Atoi(fields[2])
		if pidErr != nil || parentErr != nil || groupErr != nil || pid <= 0 || group <= 0 {
			continue
		}
		entries = append(entries, processEntry{pid: pid, parent: parent, group: group, state: fields[3]})
	}
	if len(entries) == 0 {
		return nil, ErrAmbiguousProcess
	}
	return entries, nil
}

func processGroups(root int) ([]int, int, bool, error) {
	entries, err := processTable()
	if err != nil {
		return nil, 0, false, err
	}
	groups, rootGroup, found := descendantGroups(root, entries)
	return groups, rootGroup, found, nil
}

func descendantGroups(root int, entries []processEntry) ([]int, int, bool) {
	descendants := map[int]bool{root: true}
	groups := map[int]bool{}
	found, rootGroup := false, 0
	for changed := true; changed; {
		changed = false
		for _, entry := range entries {
			if entry.pid == root {
				found, rootGroup = true, entry.group
			}
			if descendants[entry.pid] || descendants[entry.parent] {
				if !descendants[entry.pid] {
					descendants[entry.pid], changed = true, true
				}
				groups[entry.group] = true
			}
		}
	}
	result := make([]int, 0, len(groups))
	for group := range groups {
		result = append(result, group)
	}
	sort.Ints(result)
	return result, rootGroup, found
}

func signalGroups(groups []int, rootGroup int, signal syscall.Signal) error {
	ordered := append([]int(nil), groups...)
	if signal != syscall.SIGSTOP {
		for index, group := range ordered {
			if group == rootGroup {
				ordered = append(append(ordered[:index], ordered[index+1:]...), rootGroup)
				break
			}
		}
	} else {
		for index, group := range ordered {
			if group == rootGroup {
				copy(ordered[1:index+1], ordered[:index])
				ordered[0] = rootGroup
				break
			}
		}
	}
	for _, group := range ordered {
		if group <= 1 || group == syscall.Getpgrp() {
			return ErrAmbiguousProcess
		}
		if err := syscall.Kill(-group, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("signal process group %d with %d: %w", group, signal, err)
		}
	}
	return nil
}

func anyGroupAlive(groups []int) bool {
	entries, err := processTable()
	if err != nil {
		return true
	}
	for _, entry := range entries {
		for _, group := range groups {
			if entry.group == group && !strings.HasPrefix(entry.state, "Z") {
				return true
			}
		}
	}
	return false
}
