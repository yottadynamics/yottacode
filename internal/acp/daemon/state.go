package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type State struct {
	PID     int    `json:"pid"`
	Address string `json:"address"`
}

func WriteState(path, address string) error {
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"+address+"\n"), 0o600)
}

func ReadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		return State{}, errors.New("invalid daemon state")
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return State{}, err
	}
	return State{PID: pid, Address: lines[1]}, nil
}

func Stop(path string) error {
	state, err := ReadState(path)
	if err != nil {
		return err
	}
	process, err := os.FindProcess(state.PID)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop daemon: %w", err)
	}
	return nil
}

func filepathDir(path string) string { return filepath.Dir(path) }
