package assert

import "time"

type Result struct {
	Name     string
	Passed   bool
	Message  string
	Duration time.Duration
}
