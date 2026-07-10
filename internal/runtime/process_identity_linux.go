//go:build linux

package runtime

import (
	"fmt"
	"os"
	"strings"
)

func processIdentity(pid int) (string, error) {
	identity, _, err := linuxProcessStat(pid)
	return identity, err
}

func linuxProcessStat(pid int) (identity, state string, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", "", err
	}
	// The comm field is parenthesized and may contain spaces or ')'. Parse
	// from its final ')' so fields[0] below is stat field 3 (state).
	endComm := strings.LastIndexByte(string(data), ')')
	if endComm < 0 {
		return "", "", fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[endComm+1:]))
	if len(fields) <= 19 {
		return "", "", fmt.Errorf("short /proc/%d/stat", pid)
	}
	return fields[19], fields[0], nil // fields 22 (starttime) and 3 (state)
}
