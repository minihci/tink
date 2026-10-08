package resolve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lxc/incus/v7/shared/cliconfig"

	"github.com/minihci/tink/internal/secrets"
)

// An OCI remote's credentials helper.
//
// An OCI remote can name a credentials helper (`incus remote add --credentials-helper`, `credentials_helper:` in the client configuration): a
// program in the Docker credential-helper protocol. Incus runs it as `HELPER get` with the registry's host on standard input, reads
// {"Username": ..., "Secret": ...} from its output, and writes them into the remote's address (https://user:secret@host). That address is
// what Incus then uses in its own client AND what it sends the server as the place to pull from: the API has no other field for a
// credential, so the server's pull is authenticated only if the address it is given carries them.
//
// tink does the same, in one place, so that its lookup of an image and the pull it asks the server for use the same credentials as
// `incus launch` would. (The mechanism is Incus's own, in shared/cliconfig/remote.go, and is not exported.)

// credentialsHelperTimeout bounds one run of a helper, which may wait on a keychain.
const credentialsHelperTimeout = 30 * time.Second

// withHelperCredentials is remote with the credentials its helper supplies written into its address, as Incus writes them. A remote with
// no helper (or no address) is returned as it is. The helper replaces any credentials already in the address, as in Incus.
func withHelperCredentials(remote cliconfig.Remote) (cliconfig.Remote, error) {
	if remote.CredHelper == "" || len(remote.Addrs) == 0 {
		return remote, nil
	}
	u, err := url.Parse(remote.Addrs[0])
	if err != nil {
		return remote, fmt.Errorf("reading the remote's address: %w", err)
	}
	if u.Host == "" {
		return remote, fmt.Errorf("the remote's address %q has no host to ask the credentials helper about", u.Redacted())
	}

	ctx, cancel := context.WithTimeout(context.Background(), credentialsHelperTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, remote.CredHelper, "get")
	cmd.Stdin = strings.NewReader(u.Host)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return remote, fmt.Errorf("the credentials helper %q did not answer within %s", remote.CredHelper, credentialsHelperTimeout)
		}
		// The helper's own words are what say why (not found in the keychain, locked); it is not given the credentials, so they are not in them.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return remote, fmt.Errorf("the credentials helper %q failed: %w: %s", remote.CredHelper, err, msg)
		}
		return remote, fmt.Errorf("the credentials helper %q failed: %w", remote.CredHelper, err)
	}

	var got struct{ Username, Secret string }
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		return remote, fmt.Errorf("the credentials helper %q did not answer with {\"Username\", \"Secret\"} JSON: %w", remote.CredHelper, err)
	}
	if got.Username == "" && got.Secret == "" {
		return remote, fmt.Errorf("the credentials helper %q answered with no username and no secret", remote.CredHelper)
	}

	u.User = url.UserPassword(got.Username, got.Secret)
	out := remote
	out.Addrs = append([]string{u.String()}, remote.Addrs[1:]...) // the caller's slice is shared with the loaded configuration
	return out, nil
}

// scrubCredentials removes the password written into addr from err's text, for the places where the server or a library might echo the
// address it was given. An error that does not mention it is returned as it is.
func scrubCredentials(err error, addr string) error {
	if err == nil {
		return nil
	}
	u, perr := url.Parse(addr)
	if perr != nil || u.User == nil {
		return err
	}
	password, _ := u.User.Password()
	msg := err.Error()
	// A short password is not scrubbed from the text (it would mangle unrelated words), but the whole address is.
	forms := []string{addr}
	if len(password) >= secrets.MinLength {
		_, escaped, _ := strings.Cut(u.User.String(), ":")
		forms = append(forms, password, escaped)
	}
	scrubbed := msg
	for _, f := range forms {
		if f != "" {
			scrubbed = strings.ReplaceAll(scrubbed, f, "***")
		}
	}
	if scrubbed == msg {
		return err
	}
	return errors.New(scrubbed)
}

// helperResult is one remote's credentials, resolved once however many images, and instances at once, ask for them.
type helperResult struct {
	once   sync.Once
	remote cliconfig.Remote
	err    error
}

// authedRemote is remote as withHelperCredentials makes it, with the helper run at most once per run for a given remote: instances apply
// concurrently, and a helper that asks for a keychain password should ask once.
func (e *imageEnv) authedRemote(remote cliconfig.Remote) (cliconfig.Remote, error) {
	if remote.CredHelper == "" || len(remote.Addrs) == 0 {
		return remote, nil
	}
	key := remote.CredHelper + "\x00" + remote.Addrs[0]
	e.mu.Lock()
	if e.helped == nil {
		e.helped = map[string]*helperResult{}
	}
	h, ok := e.helped[key]
	if !ok {
		h = &helperResult{}
		e.helped[key] = h
	}
	e.mu.Unlock()
	h.once.Do(func() { h.remote, h.err = withHelperCredentials(remote) })
	return h.remote, h.err
}
