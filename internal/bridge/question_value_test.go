package bridge

import (
	"strings"
	"testing"
)

func TestFlowQuestionValueFitsNativeTransport(t *testing.T) {
	for _, id := range []string{strings.Repeat("a", 64), "c6db32d9-b155-4bde-8d5f-45f1d7e37a6e"} {
		for _, index := range []int{0, 9999} {
			token, err := FlowQuestionValue(id, index)
			if err != nil || len(token) > 60 {
				t.Fatalf("token=%q err=%v", token, err)
			}
			got, n, err := DecodeFlowQuestionValue(token)
			if err != nil || got != id || n != index {
				t.Fatalf("decoded=%s %d %v", got, n, err)
			}
		}
	}
	for _, token := range []string{"fq1:bad:0", "fq0::0", "fq0:YWJj:-1", "fq0:YWJj:10000"} {
		if _, _, err := DecodeFlowQuestionValue(token); err == nil {
			t.Fatalf("accepted malformed %s", token)
		}
	}
}
