package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/compose"
	"github.com/minihci/tink/internal/dockerrun"
)

// Guard 3 of #205 (the corpus through both doors). For an app whose upstream page gives both a `docker run` line and a compose file, the line
// goes through tink run's own flags and dockerrun.Build, the compose file goes through the importer, and the two must give the same instance config and
// devices. What differs is the site's half (project, network, profiles), which neither side states, and the command line tink run records.
//
// The line is hand-transcribed into internal/compose/testdata/APP/run.txt, beside the compose file. Where upstream's two forms disagree, run.txt
// says what the compose file says and a comment names the difference, so the disagreement is upstream's and is on record. An app with a compose
// file the importer accepts and a docker run line upstream (corpus/APP/upstream.md) must have a run.txt: either the line or a "# skip: reason".

const compose3Dir = "../../internal/compose/testdata"

func TestCorpusThroughBothDoors(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(compose3Dir, "*", "compose.yaml"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no survey files found: %v", err)
	}
	for _, f := range dirs {
		dir := filepath.Dir(f)
		app := filepath.Base(dir)
		t.Run(app, func(t *testing.T) {
			line, err := os.ReadFile(filepath.Join(dir, "run.txt"))
			if errors.Is(err, os.ErrNotExist) {
				if importable(dir) && upstreamHasDockerRun(t, app) {
					t.Fatalf("%s has a compose file that imports and a docker run line upstream (corpus/%s/upstream.md), but no testdata/%s/run.txt: transcribe the line, or write \"# skip: reason\"", app, app, app)
				}
				t.Skip("no docker run line upstream, or the compose file is refused")
			}
			if err != nil {
				t.Fatal(err)
			}
			args, skip, err := transcribed(string(line))
			if err != nil {
				t.Fatalf("run.txt: %v", err)
			}
			if skip != "" {
				t.Skip(skip)
			}

			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			res, err := compose.Import(src, compose.Options{Dir: dir, Stack: app})
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if len(res.Refused) > 0 {
				t.Fatalf("the importer refuses the compose file, so there is nothing to compare: %v", res.Refused)
			}
			doors := instances(t, res.YAML)
			if len(doors) != 1 {
				t.Fatalf("run.txt is one command, but the compose file makes %d instances: write \"# skip: reason\" or split the case", len(doors))
			}
			imported := doors[0]

			opts := dockerrun.DefaultOptions() // as newRunCmd starts: the pool a bare volume name is made in
			cmd := &cobra.Command{Use: "run"}
			bindRunFlags(cmd, &opts)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Flags().Parse(args); err != nil {
				t.Fatalf("run.txt is not a tink run command line: %v", err)
			}
			rest := cmd.Flags().Args()
			if len(rest) == 0 {
				t.Fatal("run.txt has no image")
			}
			opts.Image, opts.Cmd = rest[0], rest[1:]
			spec, err := dockerrun.Build(opts)
			if err != nil {
				t.Fatalf("tink run refuses the line: %v", err)
			}
			delete(spec.Config, dockerrun.KeyCommand) // how tink run was typed: the importer has no such command

			if spec.Name != imported.Name {
				t.Errorf("name: tink run %q, import %q", spec.Name, imported.Name)
			}
			if spec.Image != imported.Image {
				t.Errorf("image: tink run %q, import %q", spec.Image, imported.Image)
			}
			for _, d := range diffStrings("config", spec.Config, imported.Config) {
				t.Error(d)
			}
			if !reflect.DeepEqual(spec.Devices, imported.Devices) {
				t.Errorf("devices differ:\n tink run: %v\n   import: %v", spec.Devices, imported.Devices)
			}
		})
	}
}

// instanceDoc is the part of an imported `kind: instance` document the guard compares.
type instanceDoc struct {
	Kind    string                       `yaml:"kind"`
	Name    string                       `yaml:"name"`
	Image   string                       `yaml:"image"`
	Config  map[string]string            `yaml:"config"`
	Devices map[string]map[string]string `yaml:"devices"`
}

func instances(t *testing.T, doc string) []instanceDoc {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(doc))
	var out []instanceDoc
	for {
		var d instanceDoc
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("the imported stack does not parse: %v", err)
		}
		if d.Kind == "instance" {
			out = append(out, d)
		}
	}
}

// importable reports whether the importer accepts the compose file in dir: it writes a stack for it (testdata/APP/stack.golden.yaml).
func importable(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "stack.golden.yaml"))
	return err == nil
}

func upstreamHasDockerRun(t *testing.T, app string) bool {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("../..", "corpus", app, "upstream.md"))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(page, []byte("docker run"))
}

// transcribed reads run.txt: lines starting with # are comments (one starting "# skip:" gives the reason the case is not compared), and the rest is
// one command line, split as a shell would split it for quotes and spaces. There is no expansion of variables.
func transcribed(text string) (args []string, skip string, err error) {
	var words strings.Builder
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if reason, ok := strings.CutPrefix(trimmed, "# skip:"); ok {
			return nil, strings.TrimSpace(reason), nil
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		words.WriteString(line + "\n")
	}
	args, err = splitWords(words.String())
	if err == nil && len(args) == 0 {
		err = errors.New("no command line, and no \"# skip: reason\"")
	}
	return args, "", err
}

func splitWords(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("an unclosed %c", quote)
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// diffStrings names every key on which two string maps differ.
func diffStrings(what string, a, b map[string]string) []string {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []string
	for _, k := range sorted {
		av, aok := a[k]
		bv, bok := b[k]
		switch {
		case aok && !bok:
			out = append(out, fmt.Sprintf("%s %s: tink run sets %q, import does not", what, k, av))
		case !aok && bok:
			out = append(out, fmt.Sprintf("%s %s: import sets %q, tink run does not", what, k, bv))
		case av != bv:
			out = append(out, fmt.Sprintf("%s %s: tink run %q, import %q", what, k, av, bv))
		}
	}
	return out
}
