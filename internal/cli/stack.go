package cli

import (
	"os"
	"path/filepath"

	"github.com/sheon-sek/igdev/internal/project"
)

// The init Wizard's first step: what kind of project this repository is. A
// maintainer onboarding an existing repository should not have to invent
// `[commands]`, so the Wizard reads the layout and offers the stages that layout
// implies as defaults. Detection is a Wizard value source only: Silent Mode
// writes what the flags say and nothing else, so an agent's contract never
// depends on which files happen to sit in the directory.

// Stack is a project kind the init Wizard recognizes.
type Stack string

const (
	StackGradle Stack = "gradle"
	StackMaven  Stack = "maven"
	StackNPM    Stack = "npm"
	StackPython Stack = "python"
	// StackNone is a repository nothing identified: the Wizard keeps the schema
	// defaults.
	StackNone Stack = "none"
)

// markers are the files that identify a stack, in the order detection prefers
// them. An Ignition repository with both a Gradle build and a Python tree is a
// Gradle project whose scripts happen to live in Python, so Gradle wins.
var markers = []struct {
	stack Stack
	files []string
}{
	{StackGradle, []string{"build.gradle.kts", "build.gradle", "settings.gradle.kts", "settings.gradle"}},
	{StackMaven, []string{"pom.xml"}},
	{StackNPM, []string{"package.json"}},
	{StackPython, []string{"pyproject.toml", "setup.py", "requirements.txt"}},
}

// stackCommands are the `[commands]` stages a stack suggests. A stage the stack
// has no answer for stays empty, which is how the check pipeline already reads
// "this project declares none".
var stackCommands = map[Stack]project.Commands{
	StackGradle: {Check: "./gradlew check", Test: "./gradlew test", Build: "./gradlew build"},
	StackMaven:  {Check: "mvn verify", Test: "mvn test", Build: "mvn package"},
	StackNPM:    {Check: "npm run check", Test: "npm test", Build: "npm run build"},
	StackPython: {Test: "pytest"},
}

// scanCandidates are the directories the Wizard offers as scan roots, after the
// schema default: an Ignition repository keeps its Jython under `project/`, and
// a hand-rolled one often under `python/`.
var scanCandidates = append(append([]string{}, project.DefaultScanPaths...), "project", "python")

// detection is what the init Wizard found in a repository: which stack the
// layout suggests, the file that identified it, the `[commands]` that implies,
// and the scan roots that exist.
type detection struct {
	Stack    Stack
	Marker   string
	Commands project.Commands
	// Scan is never empty: a repository with none of the candidate directories
	// keeps the schema default rather than writing a scan root it does not have.
	Scan []string
}

// detectStack reads a Project Root's layout. It never fails: a repository
// nothing identifies is StackNone with the schema's own defaults.
func detectStack(root string) detection {
	out := detection{Stack: StackNone, Scan: existingScanRoots(root)}
	for _, candidate := range markers {
		for _, name := range candidate.files {
			if !isFile(filepath.Join(root, name)) {
				continue
			}
			out.Stack = candidate.stack
			out.Marker = name
			out.Commands = stackCommands[candidate.stack]
			return out
		}
	}
	return out
}

// existingScanRoots keeps the candidate directories that are there, falling back
// to the schema default when none is: a scan root is a place to walk, and
// offering one that does not exist would put a dead path in the contract.
func existingScanRoots(root string) []string {
	out := make([]string, 0, len(scanCandidates))
	for _, dir := range scanCandidates {
		if isDir(filepath.Join(root, dir)) {
			out = append(out, dir)
		}
	}
	if len(out) == 0 {
		return append([]string{}, project.DefaultScanPaths...)
	}
	return out
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
