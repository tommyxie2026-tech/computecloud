package conversation

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkDecodeMessagesPayload(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 256 << 10} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			body := []byte(`{"model":"m","max_tokens":128,"messages":[{"role":"user","content":"` + strings.Repeat("a", size-100) + `"}]}`)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := DecodeMessages(body, 256<<10, 128); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
