// Package fakeinventory builds fakesoho rules for the soho commands
// the inventory slice needs: `capabilities --json` and
// `inventory --json`. Tests install the returned rules with
// fakesoho.Install and run the fake as an argv subprocess.
package fakeinventory

import (
	"fmt"
	"os"

	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// CapabilitiesLine is the capabilities output the current tests use
// (internal/soho), plus the inventory capability.
const CapabilitiesLine = `{"schema":1,"worker_collaboration":1,"ephemeral_job":1,"job_events":1,"inventory":1}`

// NoCapabilityLine is the capabilities output of a soho that does not
// know the inventory command.
const NoCapabilityLine = `{"schema":1,"worker_collaboration":1,"ephemeral_job":1,"job_events":1}`

// Rules returns the happy-path rules: `capabilities --json`
// advertises the inventory capability and `inventory --json` prints
// the fixture file verbatim and exits 0.
func Rules(fixture string) []fakesoho.Rule {
	data, err := os.ReadFile(fixture)
	if err != nil {
		panic(fmt.Sprintf("fakeinventory: read fixture %s: %v", fixture, err))
	}
	return []fakesoho.Rule{
		{Argv: []string{"capabilities", "--json"}, Stdout: CapabilitiesLine + "\n", Code: 0},
		{Argv: []string{"inventory", "--json"}, Stdout: string(data), Code: 0},
	}
}

// Unavailable returns the rules for an unreadable terminal server:
// `inventory --json` exits 4 and prints nothing.
func Unavailable() []fakesoho.Rule {
	return []fakesoho.Rule{
		{Argv: []string{"inventory", "--json"}, Code: 4},
	}
}

// NoCapability returns the rules for a soho without the inventory
// capability: `capabilities --json` answers without the inventory key.
func NoCapability() []fakesoho.Rule {
	return []fakesoho.Rule{
		{Argv: []string{"capabilities", "--json"}, Stdout: NoCapabilityLine + "\n", Code: 0},
	}
}
