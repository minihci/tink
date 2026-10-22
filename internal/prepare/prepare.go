// Package prepare is the one place where the stack files named on a command line become something the resolver can plan and apply:
// the resources loaded, their secrets expanded, a connection to the server, the images qualified, and the dependency levels built.
// `tink plan` and `tink plan apply` (and the `plan explain` that is to come) all call Prepare, so the order of the passes is written
// down once.
//
// The order is not a detail. Most passes change the resources in place (the stack's default project, image names, the publish stamps,
// project inheritance), and each one reads what the passes before it left. The sequence is:
//
//  1. load the files (which also gives every resource the stack's default project);
//  2. fill the stack's `defaults:` into the resources that do not set a value (docs/stack-defaults.md). It comes straight after the load because
//     it needs only the loaded documents, and before everything else because everything else must see the resources as they will be planned: an
//     environment.* default may carry a secret reference that the next pass has to expand, and the graph is built from the filled resources, so a
//     default is never a surprise to a later pass. It needs no server, so a bad `defaults:` fails before a connection is made;
//  3. expand ${secret:NAME} references, so nothing after this point sees a reference, and nothing before it holds a value;
//  4. connect to the server, once, for everything that follows (the commands reuse the connection for planning or applying);
//  5. derive what the stack leaves to tink (internal/derive), which needs the server's view of the platform: a bridge of its own, its ACL and the
//     static address of each published app for a stack that says `network: own`, then the ingress rules of every ACL that says derive_ingress. It comes before the graph is built, because the graph and every check read the derived values as if they had
//     been written, and it reads no secret, so it can sit after the secrets pass;
//  6. qualify each instance's image the way Incus resolves it, which needs the server and must come before the graph is built;
//  7. build the dependency levels (resolve.Levels), which applies the passes inside the resolver in their own fixed order.
//
// A command that reads a stack's files and never plans them (`tink join`, `tink volume backup`) calls Load, which is passes 1 and 2, so that it sees
// the same resources `plan` does.
//
// A new pass is added to the list in prepare, in the place its inputs require, and every command that prepares a stack has it.
package prepare

import (
	"fmt"
	"path/filepath"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/derive"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/secrets"
)

// Request is what a command knows before anything is read: the flags the commands that prepare a stack share.
type Request struct {
	Files       []string // the stack files given on the command line; none means resolve.DefaultFile
	Socket      string   // the Incus unix socket; empty is Incus's own resolution (and is not a choice alongside --remote)
	SecretStore string   // the secret store; empty is secrets.yaml beside the first stack file
	Identity    string   // the age identity that decrypts it; empty is $TINK_AGE_IDENTITY, else the default file
	// Redactor is told every secret that is decrypted, so that nothing printed afterwards can carry one.
	Redactor *secrets.Redactor
	// Notice receives what the person should be told as the passes run, one line each (an image reference read another way than written).
	// It is called as each line is known, so it is seen even when a later pass fails. Nil drops them.
	Notice func(line string)
}

// Prepared is a stack ready to be planned or applied.
type Prepared struct {
	// Server is the one connection made for this run; the caller plans or applies with it and does not connect again.
	Server incus.InstanceServer
	// Resources are the whole stack, after every pass that changes the loaded resources in place. A caller that needs all of it at once
	// (the backup targets, the profiles) takes it from here; a caller that walks the stack in order takes Levels.
	Resources []resolve.Resource
	// Levels are the resources in dependency order: everything within one level is independent of the rest of it.
	Levels [][]resolve.Resource
}

// Prepare runs the passes, in order, and returns the stack ready to plan or apply. Any error stops it, with nothing returned.
func Prepare(req Request) (*Prepared, error) {
	return prepare(req, defaultSteps())
}

// Load is the first two passes on their own: the files loaded, and the stack's defaults filled in. It is for the commands that read a stack and
// never plan it, so that they see the resources `plan` would plan and not the ones as they were written.
func Load(files []string) ([]resolve.Resource, error) {
	resources, err := resolve.LoadFiles(files)
	if err != nil {
		return nil, err
	}
	return resolve.ApplyDefaults(resources)
}

// steps are the passes that touch the world, so a test can replace them and watch the order they are called in.
type steps struct {
	load     func(files []string) ([]resolve.Resource, error)
	defaults func(resources []resolve.Resource) ([]resolve.Resource, error)
	secrets  func(resources []resolve.Resource, req Request) ([]resolve.Resource, error)
	connect  func(socket string) (incus.InstanceServer, error)
	derive   func(server incus.InstanceServer, resources []resolve.Resource) ([]resolve.Resource, []string, error)
	qualify  func(server incus.InstanceServer, resources []resolve.Resource) ([]string, error)
	levels   func(resources []resolve.Resource) ([][]resolve.Resource, error)
}

func defaultSteps() steps {
	return steps{
		load:     resolve.LoadFiles,
		defaults: resolve.ApplyDefaults,
		secrets:  expandSecrets,
		connect:  incusapi.Connect,
		derive:   derive.Run,
		qualify:  resolve.QualifyImages,
		levels:   resolve.Levels,
	}
}

// prepare is the sequence itself. Every pass of the pipeline is named here, in order, and nowhere else.
func prepare(req Request, s steps) (*Prepared, error) {
	resources, err := s.load(req.Files)
	if err != nil {
		return nil, err
	}
	if resources, err = s.defaults(resources); err != nil {
		return nil, err
	}
	if resources, err = s.secrets(resources, req); err != nil {
		return nil, err
	}
	server, err := s.connect(req.Socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to incus: %w", err)
	}
	// before the graph is built, and before the images are qualified: it fills values that the graph and the checks must see as written
	resources, derived, err := s.derive(server, resources)
	if err != nil {
		return nil, err
	}
	say := func(lines []string) {
		if req.Notice != nil {
			for _, n := range lines {
				req.Notice(n)
			}
		}
	}
	say(derived)
	// before the graph is built: it rewrites each instance's image: to the form Incus resolves
	notices, err := s.qualify(server, resources)
	if err != nil {
		return nil, err
	}
	say(notices)
	levels, err := s.levels(resources)
	if err != nil {
		return nil, err
	}
	return &Prepared{Server: server, Resources: resources, Levels: levels}, nil
}

// expandSecrets replaces ${secret:NAME} references in resources. A stack with no references never touches the store or an identity.
// The store is beside the first stack file unless one is named.
func expandSecrets(resources []resolve.Resource, req Request) ([]resolve.Resource, error) {
	if !resolve.NeedsSecrets(resources) {
		return resources, nil
	}
	path := req.SecretStore
	if path == "" {
		dir := "."
		if len(req.Files) > 0 {
			dir = filepath.Dir(req.Files[0])
		}
		path = filepath.Join(dir, secrets.DefaultFile)
	}
	store, err := secrets.Open(path)
	if err != nil {
		return nil, err
	}
	return resolve.ExpandSecrets(resources, secrets.NewResolver(store, req.Identity, req.Redactor)), nil
}
