// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

// trackedFile holds state for a single tailed file.
type trackedFile struct {
	path string
	file *os.File
	ino  uint64
}

// fileTailer implements dataSource for the perfdata file tailing mode.
type fileTailer struct {
	cfg    *FileConfig
	logger *zap.Logger

	serviceTracker *trackedFile
	hostTracker    *trackedFile // nil if no host perfdata file configured
}

func newFileTailer(cfg *FileConfig, logger *zap.Logger) *fileTailer {
	ft := &fileTailer{
		cfg:            cfg,
		logger:         logger,
		serviceTracker: &trackedFile{path: cfg.ServicePerfdataFile},
	}
	if cfg.HostPerfdataFile != "" {
		ft.hostTracker = &trackedFile{path: cfg.HostPerfdataFile}
	}
	return ft
}

func (f *fileTailer) start(_ context.Context, _ component.Host) error {
	if err := initTracker(f.serviceTracker, f.logger); err != nil {
		return fmt.Errorf("opening service perfdata file: %w", err)
	}
	if f.hostTracker != nil {
		if err := initTracker(f.hostTracker, f.logger); err != nil {
			return fmt.Errorf("opening host perfdata file: %w", err)
		}
	}
	return nil
}

func initTracker(t *trackedFile, logger *zap.Logger) error {
	file, ino, err := openAndGetInode(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("Perfdata file does not exist yet, will wait", zap.String("path", t.path))
			return nil
		}
		return err
	}

	// Seek to end so we only read new data from this point forward
	if _, err := file.Seek(0, os.SEEK_END); err != nil {
		file.Close()
		return fmt.Errorf("seeking to end of file: %w", err)
	}

	t.file = file
	t.ino = ino
	return nil
}

func (f *fileTailer) shutdown(_ context.Context) error {
	var err error
	if f.serviceTracker != nil && f.serviceTracker.file != nil {
		err = f.serviceTracker.file.Close()
	}
	if f.hostTracker != nil && f.hostTracker.file != nil {
		if closeErr := f.hostTracker.file.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

func (f *fileTailer) collect(_ context.Context) ([]NagiosCheckResult, error) {
	format := f.cfg.Format
	if format == "" {
		format = "default"
	}

	var results []NagiosCheckResult

	// Read service perfdata
	serviceLines, err := readNewLinesFromTracker(f.serviceTracker, f.logger)
	if err != nil {
		return nil, fmt.Errorf("reading service perfdata: %w", err)
	}
	for _, line := range serviceLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		result, parseErr := parseLine(line, format, false)
		if parseErr != nil {
			f.logger.Warn("Failed to parse service perfdata line", zap.Error(parseErr), zap.String("line", truncate(line, 200)))
			continue
		}
		results = append(results, result)
	}

	// Read host perfdata if configured
	if f.hostTracker != nil {
		hostLines, err := readNewLinesFromTracker(f.hostTracker, f.logger)
		if err != nil {
			f.logger.Warn("Error reading host perfdata", zap.Error(err))
		}
		for _, line := range hostLines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			result, parseErr := parseLine(line, format, true)
			if parseErr != nil {
				f.logger.Warn("Failed to parse host perfdata line", zap.Error(parseErr), zap.String("line", truncate(line, 200)))
				continue
			}
			results = append(results, result)
		}
	}

	return results, nil
}

func parseLine(line, format string, isHost bool) (NagiosCheckResult, error) {
	switch format {
	case "pnp4nagios":
		if isHost {
			return parsePNP4NagiosHostLine(line)
		}
		return parsePNP4NagiosLine(line)
	default:
		if isHost {
			return parseDefaultHostLine(line)
		}
		return parseDefaultLine(line)
	}
}

// readNewLinesFromTracker reads any new data from a tracked file,
// handles rotation detection, and returns all new complete lines.
func readNewLinesFromTracker(t *trackedFile, logger *zap.Logger) ([]string, error) {
	var allLines []string

	// Read from current fd if we have one
	if t.file != nil {
		lines, err := readLines(t.file)
		if err != nil {
			logger.Warn("Error reading from current fd", zap.Error(err), zap.String("path", t.path))
		}
		allLines = append(allLines, lines...)
	}

	// Check for rotation
	rotated, err := checkRotation(t)
	if err != nil {
		logger.Warn("Error checking rotation", zap.Error(err), zap.String("path", t.path))
	}

	if rotated {
		// Drain remaining data from old fd
		if t.file != nil {
			lines, _ := readLines(t.file)
			allLines = append(allLines, lines...)
			t.file.Close()
			t.file = nil
		}

		// Open new file
		file, ino, err := openAndGetInode(t.path)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Debug("Rotated file not yet created", zap.String("path", t.path))
				return allLines, nil
			}
			return allLines, fmt.Errorf("opening rotated file: %w", err)
		}

		t.file = file
		t.ino = ino

		// Read from new file
		lines, err := readLines(t.file)
		if err != nil {
			logger.Warn("Error reading from new file", zap.Error(err), zap.String("path", t.path))
		}
		allLines = append(allLines, lines...)
	}

	return allLines, nil
}

// checkRotation checks if the file at the tracked path has a different inode
// than the currently held fd.
func checkRotation(t *trackedFile) (bool, error) {
	stat, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return t.file != nil, nil
		}
		return false, err
	}

	sysStat, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}

	newIno := sysStat.Ino

	if t.file == nil {
		return true, nil
	}

	return newIno != t.ino, nil
}

func openAndGetInode(path string) (*os.File, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}

	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}

	sysStat, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		file.Close()
		return nil, 0, fmt.Errorf("unable to get inode for %s", path)
	}

	return file, sysStat.Ino, nil
}

func readLines(file *os.File) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// parseDefaultLine parses a tab-delimited default Nagios service perfdata line.
// Format: [SERVICEPERFDATA]\t<timestamp>\t<hostname>\t<servicedesc>\t<state>\t<output>\t<perfdata>
func parseDefaultLine(line string) (NagiosCheckResult, error) {
	if !strings.HasPrefix(line, "[SERVICEPERFDATA]") {
		return NagiosCheckResult{}, fmt.Errorf("line does not start with [SERVICEPERFDATA]")
	}

	parts := strings.Split(line, "\t")
	if len(parts) < 7 {
		return NagiosCheckResult{}, fmt.Errorf("expected at least 7 tab-delimited fields, got %d", len(parts))
	}

	return NagiosCheckResult{
		HostName:           parts[2],
		ServiceDescription: parts[3],
		State:              parseNagiosState(parts[4]),
		PluginOutput:       parts[5],
		PerfData:           parts[6],
	}, nil
}

// parseDefaultHostLine parses a tab-delimited default Nagios host perfdata line.
// Format: [HOSTPERFDATA]\t<timestamp>\t<hostname>\t<state>\t<output>\t<perfdata>
func parseDefaultHostLine(line string) (NagiosCheckResult, error) {
	if !strings.HasPrefix(line, "[HOSTPERFDATA]") {
		return NagiosCheckResult{}, fmt.Errorf("line does not start with [HOSTPERFDATA]")
	}

	parts := strings.Split(line, "\t")
	if len(parts) < 6 {
		return NagiosCheckResult{}, fmt.Errorf("expected at least 6 tab-delimited fields, got %d", len(parts))
	}

	return NagiosCheckResult{
		HostName:           parts[2],
		ServiceDescription: "Host Check",
		State:              parseNagiosState(parts[3]),
		PluginOutput:       parts[4],
		PerfData:           parts[5],
	}, nil
}

// parsePNP4NagiosLine parses a tab-delimited PNP4Nagios format service perfdata line.
// Format: KEY::VALUE pairs separated by tabs.
func parsePNP4NagiosLine(line string) (NagiosCheckResult, error) {
	fields := parsePNP4NagiosFields(line)

	hostName, ok := fields["HOSTNAME"]
	if !ok {
		return NagiosCheckResult{}, fmt.Errorf("HOSTNAME field missing")
	}

	serviceDesc, ok := fields["SERVICEDESC"]
	if !ok {
		return NagiosCheckResult{}, fmt.Errorf("SERVICEDESC field missing")
	}

	result := NagiosCheckResult{
		HostName:           hostName,
		ServiceDescription: serviceDesc,
		PluginOutput:       fields["SERVICEOUTPUT"],
		PerfData:           fields["SERVICEPERFDATA"],
		State:              parseNagiosState(fields["SERVICESTATE"]),
	}

	if cmd, ok := fields["SERVICECHECKCOMMAND"]; ok && cmd != "" {
		if idx := strings.Index(cmd, "!"); idx >= 0 {
			cmd = cmd[:idx]
		}
		result.CheckCommand = cmd
	}

	return result, nil
}

// parsePNP4NagiosHostLine parses a tab-delimited PNP4Nagios format host perfdata line.
func parsePNP4NagiosHostLine(line string) (NagiosCheckResult, error) {
	fields := parsePNP4NagiosFields(line)

	hostName, ok := fields["HOSTNAME"]
	if !ok {
		return NagiosCheckResult{}, fmt.Errorf("HOSTNAME field missing")
	}

	result := NagiosCheckResult{
		HostName:           hostName,
		ServiceDescription: "Host Check",
		PluginOutput:       fields["HOSTOUTPUT"],
		PerfData:           fields["HOSTPERFDATA"],
		State:              parseNagiosState(fields["HOSTSTATE"]),
	}

	if cmd, ok := fields["HOSTCHECKCOMMAND"]; ok && cmd != "" {
		if idx := strings.Index(cmd, "!"); idx >= 0 {
			cmd = cmd[:idx]
		}
		result.CheckCommand = cmd
	}

	return result, nil
}

func parsePNP4NagiosFields(line string) map[string]string {
	fields := make(map[string]string)
	parts := strings.Split(line, "\t")
	for _, part := range parts {
		idx := strings.Index(part, "::")
		if idx < 0 {
			continue
		}
		fields[part[:idx]] = part[idx+2:]
	}
	return fields
}

func parseNagiosState(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "OK", "UP", "0":
		return 0
	case "WARNING", "1":
		return 1
	case "CRITICAL", "DOWN", "2":
		return 2
	default:
		return 3 // UNKNOWN / UNREACHABLE
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
