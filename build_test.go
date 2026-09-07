package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSourceInstallRefreshesExistingFrontendDependencies(t *testing.T) {
	installer, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, source, ok := strings.Cut(string(installer), "if [ \"$BUILD_FROM_SOURCE\" = \"1\" ]; then\n")
	if !ok {
		t.Fatal("source-build branch not found")
	}
	source, _, ok = strings.Cut(source, "\nelif download_release_binaries")
	if !ok {
		t.Fatal("source-build branch end not found")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "panel/ui/node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"go": "#!/bin/sh\nexit 0\n",
		"npm": `#!/bin/sh
set -eu
case "$*" in
  "--prefix panel/ui ci") touch panel/ui/node_modules/refreshed ;;
  "--prefix panel/ui run build")
    test -f panel/ui/node_modules/refreshed
    touch ui-built
    ;;
  *) exit 1 ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise the actual source-build branch without touching system configuration.
	cmd := exec.Command("sh", "-c", "set -eu\ninstall_binary() { :; }\n"+source)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SOURCE_CHECKOUT=1", "INSTALL_DIR="+dir, "tmp_dir="+dir,
		"GO_CMD="+filepath.Join(dir, "go"), "NPM_CMD="+filepath.Join(dir, "npm"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("source build did not refresh existing dependencies: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, "ui-built")); err != nil {
		t.Fatalf("frontend was not built: %v", err)
	}
}

func TestBuildStopsOnFailure(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	// Track Makefile changes in Go's test cache and run the recipes in isolation.
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		target       string
		failAt       int
		checksumFail bool
		wantCalls    int
	}{
		{name: "build proxy", target: "build", failAt: 1, wantCalls: 1},
		{name: "build panel", target: "build", failAt: 2, wantCalls: 2},
		{name: "release proxy", target: "release", failAt: 1, wantCalls: 1},
		{name: "release panel", target: "release", failAt: 2, wantCalls: 2},
		{name: "release arm64", target: "release", failAt: 3, wantCalls: 3},
		{name: "checksum", target: "release", checksumFail: true, wantCalls: 4},
		{name: "complete build", target: "build", wantCalls: 2},
		{name: "complete release", target: "release", wantCalls: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0600); err != nil {
				t.Fatal(err)
			}
			compiler := filepath.Join(dir, "go")
			script := `#!/bin/sh
set -eu
output=
while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then output=$2; shift; fi
    shift
done
printf '%s\n' "$output" >> "$BUILD_TEST_LOG"
count=0
while IFS= read -r line; do count=$((count + 1)); done < "$BUILD_TEST_LOG"
[ "$count" -ne "$BUILD_TEST_FAIL_AT" ] || exit 1
printf 'test binary for %s\n' "$GOARCH" > "$output"
`
			if err := os.WriteFile(compiler, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if tc.checksumFail {
				if err := os.WriteFile(filepath.Join(dir, "sha256sum"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			outputDir := filepath.Join(dir, "output")
			logPath := filepath.Join(dir, "build.log")
			cmd := exec.Command("make", "--no-print-directory", "-o", "panel-ui", tc.target,
				"GO="+compiler, "BINDIR="+outputDir, "DIST_DIR="+outputDir,
				"TARGET=linux-amd64", "TARGETS=linux-amd64 linux-arm64")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"BUILD_TEST_LOG="+logPath, "BUILD_TEST_FAIL_AT="+strconv.Itoa(tc.failAt), "MAKEFLAGS=")
			output, err := cmd.CombinedOutput()
			wantFailure := tc.failAt != 0 || tc.checksumFail
			if (err != nil) != wantFailure {
				t.Errorf("make error = %v, want failure %v:\n%s", err, wantFailure, output)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(log), "\n"); got != tc.wantCalls {
				t.Errorf("compiler ran %d times, want %d", got, tc.wantCalls)
			}
			if tc.target == "release" && !wantFailure {
				manifest, err := os.ReadFile(filepath.Join(outputDir, "SHA256SUMS"))
				if err != nil || len(strings.Split(strings.TrimSpace(string(manifest)), "\n")) != 4 {
					t.Fatalf("incomplete release manifest: %s, %v", manifest, err)
				}
				check := exec.Command("sha256sum", "-c", "SHA256SUMS")
				check.Dir = outputDir
				if output, err := check.CombinedOutput(); err != nil {
					t.Fatalf("invalid release checksums: %v\n%s", err, output)
				}
			}
		})
	}
}
