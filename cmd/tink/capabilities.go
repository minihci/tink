package main

// The converger (internal/resolve) imports no capability: each one registers itself with the seam the converger owns, and a program has the
// capability when it links the package. Backup is registered by internal/backupmeta; the blank import says so here rather than relying on another
// import (the backup commands) happening to pull it in.
import _ "github.com/minihci/tink/internal/backupmeta"
