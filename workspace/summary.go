package workspace

// PatchSummary is a compact, model- and host-facing summary of file-level
// changes. It is suitable for tool output metadata, approval prompts, event
// payloads, and telemetry.
type PatchSummary struct {
	Files       int
	Added       int
	Modified    int
	Deleted     int
	BytesBefore int
	BytesAfter  int
	ByteDelta   int
	Paths       []string
}

// SummarizeChanges returns deterministic aggregate counts for changes.
func SummarizeChanges(changes []Change) PatchSummary {
	summary := PatchSummary{
		Files: len(changes),
		Paths: make([]string, 0, len(changes)),
	}
	for _, change := range changes {
		summary.Paths = append(summary.Paths, change.Path)
		summary.BytesBefore += len(change.Before)
		summary.BytesAfter += len(change.After)
		switch change.Kind {
		case ChangeAdded:
			summary.Added++
		case ChangeModified:
			summary.Modified++
		case ChangeDeleted:
			summary.Deleted++
		}
	}
	summary.ByteDelta = summary.BytesAfter - summary.BytesBefore
	return summary
}
