package server

import (
	"context"

	"github.com/lyaurora/turnsocks/panel/probe"
)

func (a *app) readServerTests(path string) map[string]serverTestResponse {
	a.testMu.Lock()
	defer a.testMu.Unlock()
	tests, _ := probe.ReadResults(path)
	return tests
}

func (a *app) saveServerTest(server string, result serverTestResponse) error {
	normalized, err := normalizeServer(server)
	if err != nil {
		return err
	}

	a.testMu.Lock()
	defer a.testMu.Unlock()

	path := a.testPath
	if result.Mode == probe.ModeCheck {
		path = a.checkPath
	}
	tests, err := probe.ReadResults(path)
	if err != nil {
		return err
	}
	tests[normalized] = result
	return probe.WriteResults(path, tests)
}

func (a *app) deleteServerTest(server string) {
	normalized, err := normalizeServer(server)
	if err != nil {
		return
	}

	a.testMu.Lock()
	defer a.testMu.Unlock()

	for _, path := range []string{a.testPath, a.checkPath} {
		tests, err := probe.ReadResults(path)
		if err != nil {
			continue
		}
		if _, ok := tests[normalized]; !ok {
			continue
		}
		delete(tests, normalized)
		_ = probe.WriteResults(path, tests)
	}
}

func (a *app) testServer(ctx context.Context, server string, info serverInfo, doh string, mode probe.Mode) serverTestResponse {
	runner := probe.Runner{ConfigPath: a.configPath}
	return runner.Test(ctx, probe.Server{Raw: server, Addr: info.Addr}, doh, mode)
}
