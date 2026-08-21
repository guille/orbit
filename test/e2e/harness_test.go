//go:build e2e

// Package e2e is a black-box integration harness for the orbit binary.
//
// It runs the real compiled binary in a fully isolated environment (temp
// XDG/HOME dirs) with fake systemd tools shadowed onto PATH, so nothing
// touches the developer's real orbit config or systemd user manager.
//
// Run with:
//
//	go test -tags e2e ./test/e2e/...
//
// systemd-analyze is used for real (orbit verifies generated units with it);
// tests skip when it is not available.
package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Set once by TestMain: the compiled orbit binary and the dir holding the fake
// systemd tools.
var (
	orbitBin string
	fakeBin  string
)

func TestMain(m *testing.M) {
	// When invoked under a shadowed name (via the PATH symlinks below), act as
	// the fake systemd tool instead of running tests.
	switch filepath.Base(os.Args[0]) {
	case "systemctl", "journalctl":
		os.Exit(runFakeSystemd())
	}

	tmp, err := os.MkdirTemp("", "orbit-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	orbitBin = filepath.Join(tmp, "orbit")
	if out, err := exec.Command("go", "build", "-o", orbitBin, "go.guillerg.dev/orbit/cmd/orbit").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building orbit: %v\n%s", err, out)
		os.Exit(1)
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin = filepath.Join(tmp, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, name := range []string{"systemctl", "journalctl"} {
		if err := os.Symlink(self, filepath.Join(fakeBin, name)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

// runFakeSystemd records the invocation (for contract assertions) and returns
// canned output. It reflects the installed unit files for `list-unit-files` so
// drift/orphan scenarios are exercised against real on-disk state, and answers
// `show` status queries by replaying the invocation log; other queries succeed
// with no output.
func runFakeSystemd() int {
	logPath := os.Getenv("ORBIT_FAKE_LOG")
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintln(f, strings.Join(os.Args, " "))
			_ = f.Close()
		}
	}

	for i, arg := range os.Args {
		switch arg {
		case "list-unit-files":
			fakeListUnitFiles()
			return 0
		case "show":
			fakeShow(logPath, os.Args[i+1:])
			return 0
		}
	}
	return 0
}

// fakeShow answers `systemctl show --property=... <units>` queries. Unit
// status is derived by replaying the invocation log, so a unit reports
// active/enabled iff orbit actually enabled or started it. Result queries
// (failure detection) print nothing, which orbit parses as "no failures".
func fakeShow(logPath string, args []string) {
	var props string
	var units []string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--property="); ok {
			props = v
		} else if !strings.HasPrefix(a, "-") {
			units = append(units, a)
		}
	}
	if !strings.Contains(props, "ActiveState") {
		return
	}

	up := replayUnitStates(logPath)
	for i, u := range units {
		if i > 0 {
			fmt.Println()
		}
		if up[u] {
			fmt.Printf("Id=%s\nActiveState=active\nUnitFileState=enabled\n", u)
		} else {
			fmt.Printf("Id=%s\nActiveState=inactive\nUnitFileState=disabled\n", u)
		}
	}
}

// replayUnitStates scans the invocation log for lifecycle verbs and returns
// each unit's latest state (true = enabled/active). enable and start are
// conflated (orbit always uses enable --now), as are disable and stop.
func replayUnitStates(logPath string) map[string]bool {
	up := map[string]bool{}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return up
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		idx, on := -1, false
		for i, f := range fields {
			if f == "enable" || f == "start" {
				idx, on = i, true
				break
			}
			if f == "disable" || f == "stop" {
				idx, on = i, false
				break
			}
		}
		if idx < 0 {
			continue
		}
		for _, f := range fields[idx+1:] {
			if !strings.HasPrefix(f, "-") {
				up[f] = on
			}
		}
	}
	return up
}

// fakeListUnitFiles prints the orbit unit files present in the isolated unit
// dir, mimicking `systemctl --user list-unit-files` output that orbit parses.
func fakeListUnitFiles() {
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if name := ent.Name(); strings.HasPrefix(name, "orbit-") {
			fmt.Printf("%s enabled\n", name)
		}
	}
}

// orbitEnv is an isolated run environment for one test.
type orbitEnv struct {
	home    string
	fakeLog string
}

func newEnv(t *testing.T) *orbitEnv {
	t.Helper()
	home := t.TempDir()
	return &orbitEnv{home: home, fakeLog: filepath.Join(home, "systemctl.log")}
}

func (e *orbitEnv) env() []string {
	return []string{
		"HOME=" + e.home,
		"XDG_CONFIG_HOME=" + filepath.Join(e.home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(e.home, ".local", "share"),
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"ORBIT_FAKE_LOG=" + e.fakeLog,
		"NO_COLOR=1",
	}
}

type result struct {
	stdout, stderr string
	exit           int
}

// run executes the orbit binary with the given stdin and args in isolation.
func (e *orbitEnv) run(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(orbitBin, args...)
	cmd.Env = e.env()
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb

	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running orbit %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return result{stdout: out.String(), stderr: errb.String(), exit: code}
}

// writeConfig writes orbit.toml into the isolated config dir.
func (e *orbitEnv) writeConfig(t *testing.T, content string) {
	t.Helper()
	dir := filepath.Join(e.home, ".config", "orbit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orbit.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// applyConfig writes body as orbit.toml (with orbit_bin wired to the test
// binary so generated units pass systemd-analyze) and runs `orbit apply`,
// passing any extra flags through.
func (e *orbitEnv) applyConfig(t *testing.T, body string, extraArgs ...string) result {
	t.Helper()
	e.writeConfig(t, "orbit_bin = \""+orbitBin+"\"\n\n"+body)
	return e.run(t, "", append([]string{"apply"}, extraArgs...)...)
}

// resetCalls discards recorded systemctl invocations, so a later assertion sees
// only the calls made by the next command.
func (e *orbitEnv) resetCalls(t *testing.T) {
	t.Helper()
	if err := os.Remove(e.fakeLog); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// unitDir is where orbit installs generated systemd units.
func (e *orbitEnv) unitDir() string {
	return filepath.Join(e.home, ".config", "systemd", "user")
}

// sentinelPath is orbit's pending-reminder sentinel file (drives the prompt).
func (e *orbitEnv) sentinelPath() string {
	return filepath.Join(e.home, ".local", "share", "orbit", "pending")
}

// systemctlCalls returns the recorded fake-systemctl/journalctl invocations.
func (e *orbitEnv) systemctlCalls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(e.fakeLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// hasCall reports whether any recorded invocation contains sub.
func hasCall(calls []string, sub string) bool {
	for _, c := range calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func replaceOnce(s, old, new string) string { return strings.Replace(s, old, new, 1) }

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

// requireSystemdAnalyze skips the test when systemd-analyze is unavailable,
// since orbit verifies generated units with it during apply.
func requireSystemdAnalyze(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not available")
	}
}
