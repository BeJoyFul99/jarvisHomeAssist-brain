package bills

import "strings"

// utility-specific vocabulary — hits trigger intent alone.
var utilityStrong = []string{
	"bill", "bills", "electricity", "electric", "water", "hvac", "meter",
	"utility", "utilities", "kwh", "usage", "consumption", "peak", "off-peak",
	"tier 1", "tier 2", "powerstream", "statement",
}

// domain-adjacent words that need a utility cue to trigger.
var utilityContextual = []string{"budget", "payment", "due", "overdue", "late fee", "cost", "save", "savings"}
var utilityContextCues = []string{"bill", "electric", "water", "hvac", "utility", "meter", "kwh", "gas", "energy"}

// DetectUtilityIntent returns true when the message is clearly about utilities.
func DetectUtilityIntent(message string) bool {
	m := strings.ToLower(message)
	if m == "" {
		return false
	}
	for _, w := range utilityStrong {
		if containsWord(m, w) {
			return true
		}
	}
	contextual := false
	for _, w := range utilityContextual {
		if containsWord(m, w) {
			contextual = true
			break
		}
	}
	if !contextual {
		return false
	}
	for _, w := range utilityContextCues {
		if containsWord(m, w) {
			return true
		}
	}
	return false
}

// containsWord matches whole words and multi-word phrases.
func containsWord(haystack, needle string) bool {
	if !strings.Contains(haystack, needle) {
		return false
	}
	if strings.Contains(needle, " ") {
		return true
	}
	i := strings.Index(haystack, needle)
	for i != -1 {
		left := i == 0 || !isWordRune(haystack[i-1])
		end := i + len(needle)
		right := end == len(haystack) || !isWordRune(haystack[end])
		if left && right {
			return true
		}
		next := strings.Index(haystack[end:], needle)
		if next == -1 {
			return false
		}
		i = end + next
	}
	return false
}

func isWordRune(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
