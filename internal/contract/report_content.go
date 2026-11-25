package contract

import "embed"

// ReportContentFiles contains the independently selected provider contract,
// report schema and wire fixtures for getRunAnalysisReportContent.
//
//go:embed report-content
var ReportContentFiles embed.FS
