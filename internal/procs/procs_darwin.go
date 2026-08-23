//go:build darwin

package procs

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func init() {
	platformList = listDarwin
	osGetpid = os.Getpid
}

// listDarwin shells out to ps(1): there is no pure-Go API for the BSD process
// table without cgo. One call yields pid, %cpu, RSS(kB) and full command.
func listDarwin() ([]raw, error) {
	out, err := exec.Command("ps", "-axo", "pid=,%cpu=,rss=,command=").Output()
	if err != nil {
		return nil, err
	}
	var list []raw
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 4)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
		rssKB, _ := strconv.ParseUint(strings.TrimSpace(fields[2]), 10, 64)

		command := fields[3]
		args := strings.Fields(command)
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		list = append(list, raw{
			pid:        pid,
			name:       name,
			args:       args,
			rss:        rssKB << 10,
			cpuPercent: cpu,
		})
	}
	return list, nil
}
