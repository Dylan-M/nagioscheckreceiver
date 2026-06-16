// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestParsers_ErrorCases(t *testing.T) {
	_, err := parseDefaultHostLine("[SERVICEPERFDATA]\t1\th\ts\to\tp")
	require.Error(t, err) // wrong prefix

	_, err = parseDefaultHostLine("[HOSTPERFDATA]\t1\th")
	require.Error(t, err) // too few fields

	_, err = parsePNP4NagiosLine("SERVICEDESC::svc\tSERVICEPERFDATA::x=1")
	require.Error(t, err) // missing HOSTNAME

	_, err = parsePNP4NagiosLine("HOSTNAME::h\tSERVICEPERFDATA::x=1")
	require.Error(t, err) // missing SERVICEDESC

	_, err = parsePNP4NagiosHostLine("HOSTPERFDATA::rta=1")
	require.Error(t, err) // missing HOSTNAME
}

func TestFileTailer_MissingFileWaits(t *testing.T) {
	cfg := &FileConfig{ServicePerfdataFile: filepath.Join(t.TempDir(), "does-not-exist"), Format: "pnp4nagios"}
	tailer := newFileTailer(cfg, zaptest.NewLogger(t))
	require.NoError(t, tailer.start(context.Background(), nil)) // missing file is not fatal
	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestFileTailer_SkipsUnparseableLines(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "service-perfdata")
	cfg := &FileConfig{ServicePerfdataFile: svcFile, Format: "pnp4nagios"}
	tailer := newFileTailer(cfg, zaptest.NewLogger(t))
	require.NoError(t, os.WriteFile(svcFile, []byte(""), 0644))
	require.NoError(t, tailer.start(context.Background(), nil))
	defer func() { _ = tailer.shutdown(context.Background()) }()

	// one good line, one missing HOSTNAME (skipped with a warning, not an error)
	appendToFile(t, svcFile, "GARBAGE::nope\tSERVICEPERFDATA::x=1\n")
	appendToFile(t, svcFile, "HOSTNAME::h1\tSERVICEDESC::s1\tSERVICESTATE::OK\tSERVICEPERFDATA::x=1\n")

	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "h1", results[0].HostName)
}

func TestFileTailer_DetectsRotation(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "service-perfdata")
	cfg := &FileConfig{ServicePerfdataFile: svcFile, Format: "pnp4nagios"}
	tailer := newFileTailer(cfg, zaptest.NewLogger(t))
	require.NoError(t, os.WriteFile(svcFile, []byte(""), 0644))
	require.NoError(t, tailer.start(context.Background(), nil))
	defer func() { _ = tailer.shutdown(context.Background()) }()

	appendToFile(t, svcFile, "HOSTNAME::before\tSERVICEDESC::s\tSERVICESTATE::OK\tSERVICEPERFDATA::x=1\n")
	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "before", results[0].HostName)

	// rotate: remove and recreate (new inode) with fresh content
	require.NoError(t, os.Remove(svcFile))
	require.NoError(t, os.WriteFile(svcFile, []byte("HOSTNAME::after\tSERVICEDESC::s\tSERVICESTATE::OK\tSERVICEPERFDATA::x=1\n"), 0644))

	results, err = tailer.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "after", results[0].HostName, "rotated file should be reopened and read from the start")
}
