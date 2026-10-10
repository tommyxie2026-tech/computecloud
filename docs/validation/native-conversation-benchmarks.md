# Native conversation local microbenchmarks

Observed 2026-10-11 on macOS arm64 / Apple M1 with Go 1.26.0. Commands used `-benchtime=200ms -benchmem`; values are local microbenchmark averages, not percentile SLOs.

| Path | Payload/connection | Time | Allocated |
|---|---:|---:|---:|
| Messages JSON decode + duplicate-key validation | 1 KiB | 14.7 µs/op | 9.7 KiB/op |
| Messages JSON decode + duplicate-key validation | 64 KiB | 0.77 ms/op | 702.5 KiB/op |
| Messages JSON decode + duplicate-key validation | 256 KiB | 3.08 ms/op | 2.75 MiB/op |
| Authenticated HTTPS model discovery | TLS 1.3 cold connection | 401 µs/op | 143.1 KiB/op |
| Authenticated HTTPS model discovery | keepalive connection reused | 62.6 µs/op | 26.3 KiB/op |

Reproduce with:

```sh
go test ./internal/conversation -run '^$' -bench BenchmarkDecodeMessagesPayload -benchmem -benchtime=200ms
go test ./internal/server -run '^$' -bench BenchmarkConversationHTTPS -benchmem -benchtime=200ms
```

The decoder case includes JSON validation and canonical transcript allocation. The HTTPS case authenticates and returns model discovery; it excludes task submission, SSH forwarding, host-to-host RTT, Worker scheduling, Runtime startup, and model latency. The run does not establish a cache P50 or any production latency target. CI keeps fixture correctness separate from performance targets; remeasure on target hardware and include network/tunnel conditions before setting an SLO.
