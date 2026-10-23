package compose

// How each field of run.Options reaches the importer, or why it does not (#205).
//
// tink run and tink import share the translation (run.Build) but not the front door: the importer fills run.Options key by key from a compose
// service. A flag added to tink run therefore never reaches import unless someone remembers, which is how devices, privileged and a tmpfs came to
// be accepted by one and refused by the other. This table is the reminder that cannot be skipped: TestEveryRunOptionIsClassified walks run.Options
// by reflection and fails on a field that is not here, so adding a flag to tink run forces the decision in the same change.
//
// An entry is either the compose keys that fill the field (each must be a translated key in serviceKeys) or, in why, the reason import
// deliberately does not. Exactly one of the two is set.
type optionClass struct {
	keys []string
	why  string
}

var optionClasses = map[string]optionClass{
	"Name":        {keys: []string{"services"}}, // the service's name, with its underscores made hyphens
	"Image":       {keys: []string{"image"}},
	"Cmd":         {keys: []string{"entrypoint", "command"}},
	"Env":         {keys: []string{"environment"}},
	"EnvFile":     {keys: []string{"env_file"}},
	"Publish":     {keys: []string{"ports"}},
	"Volume":      {keys: []string{"volumes"}},
	"Restart":     {keys: []string{"restart"}},
	"User":        {keys: []string{"user"}},
	"Privileged":  {keys: []string{"privileged"}},
	"Device":      {keys: []string{"devices"}},
	"IncusDevice": {keys: []string{"tmpfs", "shm_size", "volumes"}}, // a tmpfs, which compose says in these keys (a volume of type tmpfs)
	"Pool":        {why: "the importer's own option (--pool), which is where managed volumes are made; not a compose key"},

	"Socket":      {why: "where tink run connects; import writes a file and connects to nothing"},
	"Project":     {why: "a site decision: the stack carries no project, the person names it where it is applied"},
	"Network":     {why: "a site decision: a stack gets its own network (network: own), and host networking is refused with the way out (run.HostNetworkAdvice)"},
	"IP":          {why: "a site decision: addresses come from the stack's network"},
	"Profiles":    {why: "a site decision: which profiles an instance layers belong to the site, not to the app"},
	"Rm":          {why: "run-only: an ephemeral instance is not something a stack keeps"},
	"VM":          {why: "compose has no key for a virtual machine; the person who wants one edits the stack"},
	"DryRun":      {why: "run-only: import never connects, and prints a file"},
	"Out":         {why: "run-only: progress lines for a command that creates"},
	"Memory":      {why: "not built: compose's mem_limit and deploy.resources.limits.memory have no file of the corpus using them yet; add the key when one does (the translation, run's -m, exists)"},
	"CPUs":        {why: "not built: compose's cpus and deploy.resources.limits.cpus have no file of the corpus using them yet; add the key when one does (the translation, run's --cpus, exists)"},
	"IncusConfig": {why: "the escape hatch for what no flag says; a compose file has no key for it, and the person edits the stack's config instead"},
}

// notOptions are the translated service keys that do not become a run.Options field: they are part of the instance document.
var notOptions = map[string]string{
	"depends_on": "the instance's depends_on: the start order",
}
