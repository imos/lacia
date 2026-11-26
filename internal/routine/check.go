package routine

import (
	"errors"
	"strings"
)

// Check records one expected scope, whether it was read, and a verified cause.
// CauseKey is shared only when the producer established the same cause.
type Check struct {
	Scope    string `json:"scope"`
	Status   string `json:"status"` // verified or unverified
	Reason   string `json:"reason,omitempty"`
	CauseKey string `json:"cause_key,omitempty"`
}

// NormalizeChecks validates explicit check coverage without interpreting prose.
// Empty CheckStatus keeps older producers compatible; Unverified still prevents
// an older partial result from being counted as a full success.
func (r *Result) NormalizeChecks() error {
	if r.Version != 0 && r.Version != 1 {
		return errors.New("結果の形式の版を読めません")
	}
	for _, scope := range r.Unverified {
		if strings.TrimSpace(scope) == "" || len(scope) > 1024 {
			return errors.New("未確認の範囲が正しくありません")
		}
	}
	if len(r.Checks) > 500 {
		return errors.New("確認範囲が多すぎます")
	}
	if r.CheckStatus == "" {
		if len(r.Checks) > 0 {
			return errors.New("確認結果の種類が必要です")
		}
		if len(r.Unverified) > 0 {
			r.CheckStatus = "partial"
		}
		return nil
	}
	verified, unverified := 0, 0
	seen := map[string]bool{}
	var scopes []string
	for _, check := range r.Checks {
		if strings.TrimSpace(check.Scope) == "" || len(check.Scope) > 1024 || seen[check.Scope] || len(check.Reason) > 128 || len(check.CauseKey) > 1024 {
			return errors.New("確認範囲が正しくありません")
		}
		seen[check.Scope] = true
		switch check.Status {
		case "verified":
			verified++
		case "unverified":
			unverified++
			scopes = append(scopes, check.Scope)
		default:
			return errors.New("確認範囲の結果を読めません")
		}
	}
	switch r.CheckStatus {
	case "complete":
		if verified == 0 || unverified > 0 || len(r.Unverified) > 0 {
			return errors.New("未確認の範囲が残っています")
		}
	case "partial":
		if len(r.Checks) > 0 && (verified == 0 || unverified == 0) || len(r.Checks) == 0 && len(r.Unverified) == 0 {
			return errors.New("一部を確認した範囲が必要です")
		}
	case "unavailable":
		if verified > 0 || len(r.Matters)+len(r.Handoffs) > 0 || len(r.Checks) == 0 && len(r.Unverified) == 0 {
			return errors.New("確認できなかった範囲が必要です")
		}
	default:
		return errors.New("確認結果の種類を読めません")
	}
	if len(r.Checks) > 0 {
		r.Unverified = scopes
	}
	return nil
}

// HasFindings controls the review queue independently of coverage health.
func (r Result) HasFindings(explicit bool) bool {
	if r.CheckStatus == "unavailable" {
		return false
	}
	if explicit || len(r.Handoffs) > 0 {
		return len(r.Matters) > 0
	}
	return !r.Nothing || len(r.Matters) > 0
}

// RequireScopes binds a report to the immutable checklist captured at enqueue.
func (r Result) RequireScopes(expected []string) error {
	if len(expected) == 0 {
		return nil
	}
	if len(r.Checks) != len(expected) {
		return errors.New("予定した確認範囲がそろっていません")
	}
	seen := map[string]bool{}
	for _, check := range r.Checks {
		seen[check.Scope] = true
	}
	for _, scope := range expected {
		if !seen[scope] {
			return errors.New("予定した確認範囲がそろっていません")
		}
	}
	return nil
}
