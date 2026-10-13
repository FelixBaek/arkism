// Copied unchanged (package name aside) from Go 1.27.1 src/internal/zstd, the standard library's
// zstd decoder, under the Go BSD license in LICENSE. DeepSeek CLI writes its session logs as
// zstd frames by default; the standard library keeps this decoder internal, so capture carries
// its own copy instead of a new module dependency.

package dshzstd
