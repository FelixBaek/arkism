//go:build !unix

package memory

// processCPUSeconds is not measured off Unix; the metric is left out.
func processCPUSeconds() (float64, bool) { return 0, false }
