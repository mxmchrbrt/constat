package assert

type Env struct {
	RestoreDir string
	// DECIDE: what else does a check need to see? Add only what you use today.
}

type Assertion interface {
	// DECIDE: does Check return (Result, error), or just Result?
	// "couldn't run the check" vs "check ran and failed" — same thing or not?
	Check(env Env) Result
}
