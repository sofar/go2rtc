package integration

import "os/exec"

// execCmd wraps os/exec.Cmd — a type alias to avoid confusion with
// other "Cmd" types while keeping the test file focused.
type execCmd = exec.Cmd

func newExecCmd(name string, args ...string) *execCmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = repoRoot()
	return cmd
}

func repoRoot() string {
	// Walk up from test dir to find go.mod
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "."
	}
	mod := string(out)
	// Strip /go.mod\n
	if i := len(mod) - len("go.mod\n"); i > 0 {
		return mod[:i]
	}
	return "."
}
