package resolve

import "fmt"

// A kind: file with seed: true is written when its path does not exist and never again. See Resource.Seed, and docs/first-start-files-design.md, "Seed".
//
// The plan (planFile, planVolumeFile) says `no changes` for a seed file that exists, whatever it holds, so the apply writes nothing and a `restart: true`
// has nothing to follow. Where the live content differs from the declared, the plan says so in a note, once the question is "is it still what I
// declared?": the answer is no and that is fine, but a person reading the plan should not have to wonder.

// seedNote is the note under a seed file that exists with other content.
func seedNote(f Resource) string {
	return fmt.Sprintf("seed: %s on %s exists and differs from the declared content; it is left as it is", f.Path, f.Instance)
}

// seededFile is the plan of a seed file that exists with other content than declared: nothing to do.
func seededFile(f Resource) PlannedResource {
	return PlannedResource{Resource: f, Action: ActionNone, Notes: []string{seedNote(f)}}
}
