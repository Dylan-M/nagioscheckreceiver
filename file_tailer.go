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

// fileTailer implements dataSource for the perfdata file tailing mode.
type fileTailer struct {
	cfg    *FileConfig
	logger *zap.Logger

	serviceFile *os.File
	serviceIno  uint64
}

func newFileTailer(cfg *FileConfig, logger *zap.Logger) *fileTailer {
	return &fileTailer{
		cfg:    cfg,
		logger: logger,
	}
}

func (f *fileTailer) start(_ context.Context, _ component.Host) error {
	file, ino, err := openAndGetInode(f.cfg.ServicePerfdataFile)
	if err != nil {
		if os.IsNotExist(err) {
			f.logger.Info("Service perfdata file does not exist yet, will wait", zap.String("path", f.cfg.ServicePerfdataFile))
			return nil
		}
		return fmt.Errorf("opening service perfdata file: %w", err)
	}

	// Seek to end so we only read new data from this point forward
	if _, err := file.Seek(0, os.SEEK_END); err != nil {
		file.Close()
		return fmt.Errorf("seeking to end of file: %w", err)
	}

	f.serviceFile = file
	f.serviceIno = ino
	return nil
}

func (f *fileTailer) shutdown(_ context.Context) error {
	if f.serviceFile != nil {
		return f.serviceFile.Close()
	}
	return nil
}

func (f *fileTailer) collect(_ context.Context) ([]NagiosCheckResult, error) {
	var results []NagiosCheckResult

	lines, err := f.readNewLines()
	if err != nil {
		return nil, err
	}

	format := f.cfg.Format
	if format == "" {
		format = "default"
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var result NagiosCheckResult
		var parseErr error

		switch format {
		case "pnp4nagios":
			result, parseErr = parsePNP4NagiosLine(line)
		default:
			result, parseErr = parseDefaultLine(line)
		}

		if parseErr != nil {
			f.logger.Warn("Failed to parse perfdata line", zap.Error(parseErr), zap.String("line", truncate(line, 200)))
			continue
		}

		results = append(results, result)
	}

	return results, nil
}

// readNewLines reads any new data from the current file descriptor,
// handles rotation detection, and returns all new complete lines.
func (f *fileTailer) readNewLines() ([]string, error) {
	var allLines []string

	// Read from current fd if we have one
	if f.serviceFile != nil {
		lines, err := readLines(f.serviceFile)
		if err != nil {
			f.logger.Warn("Error reading from current fd", zap.Error(err))
		}
		allLines = append(allLines, lines...)
	}

	// Check for rotation
	rotated, err := f.checkRotation()
	if err != nil {
		f.logger.Warn("Error checking rotation", zap.Error(err))
	}

	if rotated {
		// Drain remaining data from old fd
		if f.serviceFile != nil {
			lines, _ := readLines(f.serviceFile)
			allLines = append(allLines, lines...)
			f.serviceFile.Close()
			f.serviceFile = nil
		}

		// Open new file
		file, ino, err := openAndGetInode(f.cfg.ServicePerfdataFile)
		if err != nil {
			if os.IsNotExist(err) {
				f.logger.Debug("Rotated file not yet created")
				return allLines, nil
			}
			return allLines, fmt.Errorf("opening rotated file: %w", err)
		}

		f.serviceFile = file
		f.serviceIno = ino

		// Read from new file
		lines, err := readLines(f.serviceFile)
		if err != nil {
			f.logger.Warn("Error reading from new file", zap.Error(err))
		}
		allLines = append(allLines, lines...)
	}

	return allLines, nil
}

// checkRotation checks if the file at the configured path has a different inode
// than our currently held fd.
func (f *fileTailer) checkRotation() (bool, error) {
	stat, err := os.Stat(f.cfg.ServicePerfdataFile)
	if err != nil {
		if os.IsNotExist(err) {
			// File was removed (rotation in progress)
			return f.serviceFile != nil, nil
		}
		return false, err
	}

	sysStat, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}

	newIno := sysStat.Ino

	if f.serviceFile == nil {
		// We didn't have a file open, but one exists now
		return true, nil
	}

	return newIno != f.serviceIno, nil
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

// parseDefaultLine parses a tab-delimited default Nagios perfdata line.
// Format: [SERVICEPERFDATA]\t<timestamp>\t<hostname>\t<servicedesc>\t<state>\t<output>\t<perfdata>
func parseDefaultLine(line string) (NagiosCheckResult, error) {
	if !strings.HasPrefix(line, "[SERVICEPERFDATA]") {
		return NagiosCheckResult{}, fmt.Errorf("line does not start with [SERVICEPERFDATA]")
	}

	parts := strings.Split(line, "\t")
	if len(parts) < 7 {
		return NagiosCheckResult{}, fmt.Errorf("expected at least 7 tab-delimited fields, got %d", len(parts))
	}

	state := parseNagiosState(parts[4])

	return NagiosCheckResult{
		HostName:           parts[2],
		ServiceDescription: parts[3],
		State:              state,
		PluginOutput:       parts[5],
		PerfData:           parts[6],
	}, nil
}

// parsePNP4NagiosLine parses a tab-delimited PNP4Nagios format perfdata line.
// Format: KEY::VALUE pairs separated by tabs.
func parsePNP4NagiosLine(line string) (NagiosCheckResult, error) {
	fields := make(map[string]string)
	parts := strings.Split(line, "\t")
	for _, part := range parts {
		idx := strings.Index(part, "::")
		if idx < 0 {
			continue
		}
		key := part[:idx]
		value := part[idx+2:]
		fields[key] = value
	}

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

	// PNP4Nagios format includes check command
	if cmd, ok := fields["SERVICECHECKCOMMAND"]; ok && cmd != "" {
		// Strip arguments after '!'
		if idx := strings.Index(cmd, "!"); idx >= 0 {
			cmd = cmd[:idx]
		}
		result.CheckCommand = cmd
	}

	return result, nil
}

func parseNagiosState(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "OK", "0":
		return 0
	case "WARNING", "1":
		return 1
	case "CRITICAL", "2":
		return 2
	default:
		return 3 // UNKNOWN
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
