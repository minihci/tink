package daemon

import "os"

func pid() int { return os.Getpid() }
