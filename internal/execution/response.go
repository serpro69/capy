package execution

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sanitize"
)

// Budget measures the complete transport encoding, including fallback text and
// escaping. Builders must preserve essential fields and use the allowance only
// for optional text or a recoverable page. At most 18 bounded builds are needed.
func Budget[T any](limit int, build func(int) (T, error), encode func(any) ([]byte, error)) (T, error) {
	var zero T
	if limit < 2048 || limit > 65536 {
		return zero, errors.New("invalid response budget")
	}
	if encode == nil {
		encode = json.Marshal
	}
	for allowance := limit / 2; ; allowance /= 2 {
		result, err := build(allowance)
		if err != nil {
			return zero, err
		}
		data, err := encode(result)
		if err != nil {
			return zero, err
		}
		if len(data) <= limit {
			return result, nil
		}
		if allowance == 0 {
			return zero, errors.New("essential response exceeds budget")
		}
	}
}

// SafeMessage redacts the complete value before shortening it. Never truncate
// the input before recognition, or a partial credential could become public.
func SafeMessage(message string, allowance int) string {
	safe := sanitize.StripSecrets(message)
	if len(safe) <= allowance {
		return safe
	}
	for allowance > 0 && !utf8.RuneStart(safe[allowance]) {
		allowance--
	}
	return safe[:allowance]
}

func BoundResult(result runstore.Result, allowance int) runstore.Result {
	if result.Capacity != nil {
		capacity := *result.Capacity
		count := min(len(capacity.Runs), allowance/128)
		capacity.Runs = capacity.Runs[:count]
		capacity.Omitted = count < len(result.Capacity.Runs)
		result.Capacity = &capacity
	}
	if len(result.Preview) > allowance {
		end := allowance
		for end > 0 && !utf8.RuneStart(result.Preview[end]) {
			end--
		}
		result.Preview = result.Preview[:end]
		result.MetadataOmitted = true
	}
	if result.Error != nil {
		safe := *result.Error
		safe.Message = SafeMessage(safe.Message, allowance)
		if safe.Message != result.Error.Message {
			result.MetadataOmitted = true
		}
		result.Error = &safe
	}
	if result.Index.Error != nil {
		safe := *result.Index.Error
		safe.Message = SafeMessage(safe.Message, allowance)
		if safe.Message != result.Index.Error.Message {
			result.MetadataOmitted = true
		}
		result.Index.Error = &safe
	}
	return result
}

func ExecutionFailed(result runstore.Result) bool {
	switch result.State {
	case "admitted", "queued", "running":
		return result.Capture.State == "incomplete"
	case "exited":
		return result.ExitCode == nil || *result.ExitCode != 0 || result.Signal != nil || result.Capture.State != "complete"
	default:
		return true
	}
}
