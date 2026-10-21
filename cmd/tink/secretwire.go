package main

import "github.com/minihci/tink/internal/secrets"

// redactor scrubs every secret tink decrypts from anything it prints (best effort: see
// secrets.Redactor). The process-wide writers in main() go through it, and so does the final error.
var redactor = secrets.NewRedactor()

// stackSecretFlags are how a command finds the secret store and the identity that decrypts it.
type stackSecretFlags struct {
	store    string
	identity string
}
