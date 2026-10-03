package agent

import (
	"os"
	"syscall"
)

// notRegularFileError reports that a read tool was pointed at something other
// than a regular file (directory, FIFO, socket, device).
type notRegularFileError struct{ mode os.FileMode }

func (e *notRegularFileError) Error() string {
	kind := "special file"
	switch m := e.mode; {
	case m.IsDir():
		kind = "directory"
	case m&os.ModeNamedPipe != 0:
		kind = "named pipe"
	case m&os.ModeSocket != 0:
		kind = "socket"
	case m&os.ModeDevice != 0:
		kind = "device"
	}
	return "not a regular file (" + kind + ")"
}

// openRegularFile opens path read-only and refuses anything that is not a
// regular file.
//
// The open uses O_NONBLOCK so that a FIFO with no writer returns immediately
// instead of blocking in open(2) forever, and the type check is an fstat on the
// opened descriptor, so a path swapped for a FIFO between a stat and the open
// cannot slip through. O_NONBLOCK has no effect on reads from regular files.
func openRegularFile(path string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, &notRegularFileError{mode: info.Mode()}
	}
	return f, info, nil
}
