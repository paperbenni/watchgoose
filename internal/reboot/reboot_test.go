package reboot

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"watchgoose/internal/state"
)

// ADR 0002 is a promise about the shape of this package, so it is tested
// against the source rather than only against the behaviour: the one function
// that touches /proc/sysrq-trigger takes no argument, it is the only thing
// that references that path, and the only byte anywhere in the package is a
// hardcoded 'b'. A parameter here would be the whole failure mode.
func TestTheForcefulRungIsAHardcodedByte(t *testing.T) {
	fset := token.NewFileSet()
	dir, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}
	pkg, ok := dir["reboot"]
	if !ok {
		t.Fatalf("no reboot package here, found %v", dir)
	}

	var (
		triggerFuncs int
		triggerRefs  int
		bytesIn      int
	)
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.Ident:
					// Anything that reaches /proc/sysrq-trigger.
					if node.Name == "sysrqTriggerPath" {
						triggerRefs++
					}
				case *ast.CompositeLit:
					if fn.Name.Name != "TriggerForcefulReboot" {
						return true
					}
					// Check bytes built by the trigger function itself.
					id, ok := node.Type.(*ast.ArrayType)
					if !ok {
						return true
					}
					elem, ok := id.Elt.(*ast.Ident)
					if !ok || elem.Name != "byte" {
						return true
					}
					for _, elt := range node.Elts {
						lit, ok := elt.(*ast.BasicLit)
						if !ok {
							continue
						}
						bytesIn++
						if lit.Kind != token.CHAR || lit.Value != "'b'" {
							t.Errorf("%s: a byte other than a hardcoded 'b' is written by this package: %s",
								fset.Position(lit.Pos()), lit.Value)
						}
					}
				}
				return true
			})
			if fn.Name.Name == "TriggerForcefulReboot" {
				triggerFuncs++
				if fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
					t.Errorf("TriggerForcefulReboot takes arguments %s; it must not, so that no byte can be "+
						"chosen by a caller", fset.Position(fn.Type.Params.Pos()))
				}
			}
		}
	}

	if triggerFuncs != 1 {
		t.Errorf("%d functions are named TriggerForcefulReboot, want exactly 1", triggerFuncs)
	}
	if triggerRefs != 1 {
		t.Errorf("sysrq-trigger is referenced %d times, want once: only TriggerForcefulReboot may write to it", triggerRefs)
	}
	if bytesIn == 0 {
		t.Error("no byte literal found; this test has stopped looking at the code it is meant to police")
	}
}

// The no-op cases must not fork anything: a switch that gives up because a
// configuration value was zero should say so rather than spawn a process that
// would reboot immediately.
func TestSpawnEscalationChildRefusesANonPositiveTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Minute} {
		spec := EscalationSpec{GracefulTimeout: timeout, Deadline: time.Minute, StateFile: "/nonexistent", LogFile: "/nonexistent"}
		pid, err := SpawnEscalationChild(spec, nil)
		if err == nil {
			t.Errorf("SpawnEscalationChild with a timeout of %s returned pid %d and no error", timeout, pid)
		}
		if pid != 0 {
			t.Errorf("SpawnEscalationChild with a timeout of %s returned pid %d, want 0", timeout, pid)
		}
	}
}

// The escalation child is what honours a poke that arrives while the graceful
// rung is in flight, so its idea of staleness has to match the parent's.
func TestUnreassured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-reassurance")
	spec := EscalationSpec{Deadline: 20 * time.Minute, StateFile: path}

	// No state file at all: never reassured, so escalate.
	got, err := unreassured(spec)
	if err != nil {
		t.Fatalf("unreassured with no state file: %v", err)
	}
	if !got {
		t.Error("a machine with no state file was treated as reassured")
	}

	// A fresh reassurance: stand down. This is the poke stopping the loop.
	if err := state.New(path).Record(time.Now()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err = unreassured(spec)
	if err != nil {
		t.Fatalf("unreassured after reassurance: %v", err)
	}
	if got {
		t.Error("a machine that has just been reassured was treated as unreassured")
	}

	// An old one: escalate again.
	if err := state.New(path).Record(time.Now().Add(-24 * time.Hour)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err = unreassured(spec)
	if err != nil {
		t.Fatalf("unreassured after a stale reassurance: %v", err)
	}
	if !got {
		t.Error("a machine that has not been reassured for a day was treated as reassured")
	}

	// A corrupt state file is a warning, and the answer is still yes: a lost
	// state file must not disarm the switch.
	if err := os.WriteFile(path, []byte("nonsense\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err = unreassured(spec)
	if err == nil {
		t.Error("a corrupt state file was read without complaint")
	}
	if !got {
		t.Error("a corrupt state file was treated as reassurance")
	}
}

// The child must not be able to outlive its usefulness: whatever happens, it
// is gone by the end of the hard bound.
func TestSleepUntilGivesUpAtTheHardBound(t *testing.T) {
	if !sleepUntil(time.Now().Add(10*time.Millisecond), time.Now().Add(time.Minute)) {
		t.Error("sleepUntil gave up before the time it was asked to wait for")
	}
	start := time.Now()
	if sleepUntil(time.Now().Add(time.Hour), start.Add(30*time.Millisecond)) {
		t.Error("sleepUntil waited past its hard bound")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("sleepUntil took %s to notice its hard bound", elapsed)
	}
}

func TestHardBoundIsTheGracefulTimeoutPlusTheCap(t *testing.T) {
	spec := EscalationSpec{GracefulTimeout: 5 * time.Minute}
	bound := spec.hardBound()
	if remaining := time.Until(bound); remaining < 9*time.Minute+59*time.Second || remaining > 10*time.Minute {
		t.Errorf("the hard bound is %s away, want the graceful timeout plus five minutes", remaining)
	}
}

func TestCancelEscalationChildrenMatchesExecutableAndFlag(t *testing.T) {
	root := t.TempDir()
	add := func(pid, exe string, args ...string) {
		t.Helper()
		dir := filepath.Join(root, pid)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	add("101", "/srv/watchgoose/watchgoose (deleted)", "/srv/watchgoose/watchgoose", "-escalate-child")
	add("102", "/srv/watchgoose/watchgoose", "/srv/watchgoose/watchgoose", "-check")
	add("103", "/other/watchgoose", "/other/watchgoose", "-escalate-child")
	var killed []int
	got, err := cancelEscalationChildren(root, "/srv/watchgoose/watchgoose", func(pid int) error {
		killed = append(killed, pid)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 101 || len(killed) != 1 || killed[0] != 101 {
		t.Fatalf("cancelled %v, killed %v; want only pid 101", got, killed)
	}
}
