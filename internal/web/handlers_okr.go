package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bhs/mendelbuild/internal/domain"
)

// What is left of this file after the second OKR editor was removed: the JSON
// error helper the API handlers share, and the one place a key result's target
// is read out of a form.

func jsonError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": message,
	})
}

func keyResultTargetFields(rawComparator, rawValue, rawUnit string) (comparator string, value float64, unit string, err error) {
	comparator = strings.TrimSpace(rawComparator)
	switch comparator {
	case domain.TargetAtLeast, domain.TargetAtMost:
	case domain.TargetDone:
		// A boolean Key Result has no number to supply and no unit to name it
		// in, so whatever the form carried in those fields is discarded rather
		// than stored as decoration.
		return domain.TargetDone, 1, "", nil
	case "":
		// The commonest target by far is "at least this much", and defaulting
		// spares every form a required select.
		comparator = domain.TargetAtLeast
	default:
		return "", 0, "", fmt.Errorf("%q is not a way Mendel knows how to judge a key result", comparator)
	}

	raw := strings.TrimSpace(rawValue)
	if raw == "" {
		return "", 0, "", fmt.Errorf("a key result needs a target number")
	}
	// Tolerate the separators people type; refuse anything that is not a number,
	// because a target nobody can compare against is the thing this replaced.
	raw = strings.ReplaceAll(raw, ",", "")
	raw = strings.TrimPrefix(raw, "$")
	value, parseErr := strconv.ParseFloat(raw, 64)
	if parseErr != nil {
		return "", 0, "", fmt.Errorf("%q is not a number", strings.TrimSpace(rawValue))
	}

	return comparator, value, strings.TrimSpace(rawUnit), nil
}
