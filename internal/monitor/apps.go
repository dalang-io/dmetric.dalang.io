package monitor

import (
	"strings"

	"dmetric.dalang.io/internal/model"
)

// appStats sums every process whose name contains one of the patterns, so
// Chromeleon's many processes and SQL Server show up as one workload.
func appStats(procs []model.ProcessInfo, patterns []string) []model.AppStat {
	out := make([]model.AppStat, 0, len(patterns))
	for _, pattern := range patterns {
		needle := strings.ToLower(strings.TrimSpace(pattern))
		if needle == "" {
			continue
		}
		stat := model.AppStat{Pattern: pattern}
		topCPU := -1.0
		for _, p := range procs {
			if !strings.Contains(strings.ToLower(p.Name), needle) {
				continue
			}
			stat.Processes++
			stat.CPUPercent += p.CPUPercent
			stat.MemoryRSS += p.MemoryRSS
			if p.DiskReadBytesPerSec != nil {
				stat.DiskReadBytesPerSec += *p.DiskReadBytesPerSec
			}
			if p.DiskWriteBytesPerSec != nil {
				stat.DiskWriteBytesPerSec += *p.DiskWriteBytesPerSec
			}
			if p.CPUPercent > topCPU {
				topCPU = p.CPUPercent
				stat.TopProcess = p.Name
			}
		}
		out = append(out, stat)
	}
	return out
}
