package server

import (
	"context"

	"turn-proxy/panel/probe"
)

func (a *app) readServerTests() map[string]serverTestResponse {
	a.testMu.Lock()
	defer a.testMu.Unlock()
	tests, _ := probe.ReadResults(a.testPath)
	return tests
}

func (a *app) saveServerTest(server string, result serverTestResponse) error {
	normalized, err := normalizeServer(server)
	if err != nil {
		return err
	}

	a.testMu.Lock()
	defer a.testMu.Unlock()

	tests, err := probe.ReadResults(a.testPath)
	if err != nil {
		return err
	}
	tests[normalized] = result
	return probe.WriteResults(a.testPath, tests)
}

func (a *app) deleteServerTest(server string) {
	normalized, err := normalizeServer(server)
	if err != nil {
		return
	}

	a.testMu.Lock()
	defer a.testMu.Unlock()

	tests, err := probe.ReadResults(a.testPath)
	if err != nil {
		return
	}
	if _, ok := tests[normalized]; !ok {
		return
	}
	delete(tests, normalized)
	_ = probe.WriteResults(a.testPath, tests)
}

func (a *app) testServer(ctx context.Context, server string, info serverInfo, doh string) serverTestResponse {
	runner := probe.Runner{ConfigPath: a.configPath}
	return runner.Test(ctx, probe.Server{Raw: server, Addr: info.Addr}, doh)
}
