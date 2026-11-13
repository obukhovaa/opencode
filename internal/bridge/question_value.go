package bridge

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// FlowQuestionValue carries request identity instead of an ambiguous label.
// Hash IDs encode to 43 bytes; UUID fallback IDs to 48. With index and version
// the result stays below Telegram's 64-byte callback-data bound (and our 60 cap).
func FlowQuestionValue(requestID string, index int) (string, error) {
	if index < 0 || index > 9999 {
		return "", fmt.Errorf("invalid question choice index")
	}
	version, data := "fq0", []byte(requestID)
	if decoded, err := hex.DecodeString(requestID); err == nil && len(decoded) == 32 {
		version, data = "fq1", decoded
	}
	value := version + ":" + base64.RawURLEncoding.EncodeToString(data) + ":" + strconv.Itoa(index)
	if requestID == "" || len(value) > 60 {
		return "", fmt.Errorf("question callback identity exceeds transport limit")
	}
	return value, nil
}
func IsFlowQuestionValue(value string) bool {
	return strings.HasPrefix(value, "fq0:") || strings.HasPrefix(value, "fq1:")
}
func DecodeFlowQuestionValue(value string) (string, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || !IsFlowQuestionValue(value) {
		return "", 0, fmt.Errorf("invalid question callback")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, err
	}
	id := string(data)
	if parts[0] == "fq1" {
		if len(data) != 32 {
			return "", 0, fmt.Errorf("invalid question hash")
		}
		id = hex.EncodeToString(data)
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 || index > 9999 || id == "" || len(value) > 60 {
		return "", 0, fmt.Errorf("invalid question choice")
	}
	return id, index, nil
}
